package messaging

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/qiuy-collab/weone/ilink"
)

// reMarkdownImage matches markdown image syntax: ![alt](url)
var reMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
var reMaterialDirective = regexp.MustCompile(`(?m)^\[\[material:id=([^;\]]+);kind=([^;\]]+);title=([^\]]+)\]\]$`)

type MaterialDirective struct {
	ID    string
	Kind  string
	Title string
}

// ExtractImageURLs extracts image URLs from markdown text.
func ExtractImageURLs(text string) []string {
	matches := reMarkdownImage.FindAllStringSubmatch(text, -1)
	var urls []string
	for _, m := range matches {
		url := strings.TrimSpace(m[1])
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			urls = append(urls, url)
		}
	}
	return urls
}

func ExtractMaterialDirectives(text string) []MaterialDirective {
	matches := reMaterialDirective.FindAllStringSubmatch(text, -1)
	directives := make([]MaterialDirective, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		directive := MaterialDirective{
			ID:    strings.TrimSpace(m[1]),
			Kind:  strings.TrimSpace(strings.ToLower(m[2])),
			Title: strings.TrimSpace(m[3]),
		}
		if directive.ID == "" || directive.Kind == "" {
			continue
		}
		directives = append(directives, directive)
	}
	return directives
}

func StripMaterialDirectives(text string) string {
	cleaned := reMaterialDirective.ReplaceAllString(text, "")
	cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	return strings.TrimSpace(cleaned)
}

// SendMediaFromURL downloads a file from a URL and sends it as a media message.
func SendMediaFromURL(ctx context.Context, client *ilink.Client, toUserID, mediaURL, contextToken string) error {
	data, contentType, err := downloadFile(ctx, mediaURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", mediaURL, err)
	}

	return sendMediaData(ctx, client, toUserID, filenameFromURL(mediaURL), mediaURL, data, contentType, contextToken)
}

// SendMediaFromPath reads a local file and sends it as a media message.
func SendMediaFromPath(ctx context.Context, client *ilink.Client, toUserID, path, contextToken string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	return sendMediaData(ctx, client, toUserID, filepath.Base(path), path, data, inferContentType(path), contextToken)
}

func sendMediaData(ctx context.Context, client *ilink.Client, toUserID, fileName, source string, data []byte, contentType, contextToken string) error {
	if fileName == "" {
		fileName = "file"
	}

	start := time.Now()
	cdnMediaType, itemType := classifyMedia(contentType, source)
	log.Printf("[media] to=%s stage=prepare state=start file=%q source=%s content_type=%s raw_bytes=%d cdn_media_type=%d item_type=%d has_context_token=%t", toUserID, fileName, source, contentType, len(data), cdnMediaType, itemType, strings.TrimSpace(contextToken) != "")

	uploaded, err := UploadFileToCDN(ctx, client, data, toUserID, cdnMediaType)
	if err != nil {
		log.Printf("[media] to=%s stage=upload state=failed elapsed=%s source=%s err=%v", toUserID, time.Since(start), source, err)
		return fmt.Errorf("upload to CDN: %w", err)
	}
	log.Printf("[media] to=%s stage=upload state=finished elapsed=%s source=%s cipher_bytes=%d download_param_chars=%d", toUserID, time.Since(start), source, uploaded.CipherSize, len(uploaded.DownloadParam))

	media := &ilink.MediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            AESKeyToBase64(uploaded.AESKeyHex),
		EncryptType:       1,
	}

	var item ilink.MessageItem
	switch itemType {
	case ilink.ItemTypeImage:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{
				Media:   media,
				MidSize: uploaded.CipherSize,
			},
		}
	case ilink.ItemTypeVideo:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeVideo,
			VideoItem: &ilink.VideoItem{
				Media:     media,
				VideoSize: uploaded.CipherSize,
			},
		}
	default:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{
				Media:    media,
				FileName: fileName,
				Len:      fmt.Sprintf("%d", uploaded.FileSize),
			},
		}
	}

	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     NewClientID(),
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList:     []ilink.MessageItem{item},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	log.Printf("[media] to=%s stage=send-message state=start source=%s item_type=%d", toUserID, source, itemType)
	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		log.Printf("[media] to=%s stage=send-message state=failed elapsed=%s source=%s err=%v", toUserID, time.Since(start), source, err)
		return fmt.Errorf("send media message: %w", err)
	}
	if resp.Ret != 0 {
		err := fmt.Errorf("send media failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
		log.Printf("[media] to=%s stage=send-message state=failed elapsed=%s source=%s ret=%d errmsg=%q", toUserID, time.Since(start), source, resp.Ret, resp.ErrMsg)
		return err
	}

	log.Printf("[media] to=%s stage=send-message state=sent elapsed=%s source=%s", toUserID, time.Since(start), source)
	log.Printf("[media] sent %s to %s from %s", contentType, toUserID, source)
	return nil
}

func downloadFile(ctx context.Context, url string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = inferContentType(url)
	}

	return data, contentType, nil
}

func classifyMedia(contentType, url string) (cdnMediaType int, itemType int) {
	ct := strings.ToLower(contentType)

	if strings.HasPrefix(ct, "image/") || isImageExt(url) {
		return ilink.CDNMediaTypeImage, ilink.ItemTypeImage
	}
	if strings.HasPrefix(ct, "video/") || isVideoExt(url) {
		return ilink.CDNMediaTypeVideo, ilink.ItemTypeVideo
	}
	return ilink.CDNMediaTypeFile, ilink.ItemTypeFile
}

func isImageExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

func isVideoExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	}
	return false
}

func inferContentType(url string) string {
	ext := filepath.Ext(stripQuery(url))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func filenameFromURL(rawURL string) string {
	u := stripQuery(rawURL)
	name := filepath.Base(u)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}

func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}
