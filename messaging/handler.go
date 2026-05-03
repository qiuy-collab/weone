package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qiuy-collab/weone/config"
	"github.com/qiuy-collab/weone/ilink"
	internalruntime "github.com/qiuy-collab/weone/internal/runtime"
	"github.com/qiuy-collab/weone/materials"
	"github.com/qiuy-collab/weone/memory"
)

type Handler struct {
	mu            sync.RWMutex
	runtimeName   string
	runtimeSvc    *internalruntime.Service
	memorySvc     *memory.Service
	materialsSvc  *materials.Service
	contextTokens sync.Map
	saveDir       string
	seenMsgs      sync.Map
	onInbound     func(botID, userID string, at time.Time)
	onRuntimeTurn func(ctx context.Context, botID, userID, message, reply, memoryContext string)
}

type StatusSnapshot struct {
	RuntimeName   string
	RuntimeActive bool
}

func NewHandler(runtimeName string, runtimeSvc *internalruntime.Service, memorySvc *memory.Service, materialsSvc *materials.Service) *Handler {
	return &Handler{
		runtimeName:  runtimeName,
		runtimeSvc:   runtimeSvc,
		memorySvc:    memorySvc,
		materialsSvc: materialsSvc,
	}
}

func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

func (h *Handler) SetInboundRecorder(recorder func(botID, userID string, at time.Time)) {
	h.onInbound = recorder
}

func (h *Handler) SetRuntimeTurnRecorder(recorder func(ctx context.Context, botID, userID, message, reply, memoryContext string)) {
	h.onRuntimeTurn = recorder
}

func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

func (h *Handler) StatusSnapshot() StatusSnapshot {
	return StatusSnapshot{RuntimeName: h.runtimeName, RuntimeActive: h.runtimeSvc != nil}
}

func (h *Handler) UpdateRuntimeConfig(cfg config.RuntimeConfig) error {
	if h.runtimeSvc == nil {
		return fmt.Errorf("runtime not configured")
	}
	h.runtimeSvc.UpdateConfig(cfg)
	return nil
}

func shortTraceID(msg ilink.WeixinMessage, clientID string) string {
	if msg.MessageID != 0 {
		return fmt.Sprintf("msg-%d", msg.MessageID)
	}
	if clientID != "" {
		if len(clientID) > 8 {
			return clientID[:8]
		}
		return clientID
	}
	return "unknown"
}

func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	if msg.MessageType != ilink.MessageTypeUser || msg.MessageState != ilink.MessageStateFinish {
		return
	}
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		go h.cleanSeenMsgs()
	}
	clientID := NewClientID()
	traceID := shortTraceID(msg, clientID)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] trace=%s user=%s source=voice-transcription text=%q", traceID, msg.FromUserID, truncate(text, 80))
		}
	}
	if text == "" {
		if img := extractImage(msg); img != nil && h.saveDir != "" {
			h.handleImageSave(ctx, client, msg, img)
			return
		}
		log.Printf("[handler] trace=%s user=%s route=skip reason=non-text", traceID, msg.FromUserID)
		return
	}
	log.Printf("[handler] trace=%s user=%s message_id=%d route=runtime-only text=%q", traceID, msg.FromUserID, msg.MessageID, truncate(text, 80))
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	if h.onInbound != nil {
		h.onInbound(client.BotID(), msg.FromUserID, time.Now().UTC())
	}
	trimmed := strings.TrimSpace(text)
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] trace=%s user=%s branch=save-url url=%s", traceID, msg.FromUserID, rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			reply := ""
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}
	if trimmed == "/info" {
		reply := h.buildStatus()
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	} else if trimmed == "/help" {
		reply := buildHelpText()
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		reply := h.resetRuntimeSession(msg.FromUserID)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}
	h.sendToRuntime(ctx, client, msg, text, clientID)
}

func (h *Handler) sendToRuntime(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	traceID := shortTraceID(msg, clientID)
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] trace=%s user=%s stage=typing state=failed err=%v", traceID, msg.FromUserID, typingErr)
		}
	}()
	botID := client.BotID()
	log.Printf("[handler] trace=%s bot=%s user=%s route=runtime input=%q", traceID, botID, msg.FromUserID, truncate(text, 80))
	reply, err := h.chatWithRuntime(ctx, botID, msg.FromUserID, text)
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, h.runtimeName, reply, clientID)
}

func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
	traceID := shortTraceID(msg, clientID)
	imageURLs := ExtractImageURLs(reply)
	materialDirectives := ExtractMaterialDirectives(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := []string{defaultAttachmentWorkspace()}
	visibleReply := StripMaterialDirectives(reply)
	log.Printf("[handler] trace=%s user=%s stage=reply-prepare agent=%s text_chars=%d images=%d material_media=%d attachments=%d preview=%q", traceID, msg.FromUserID, agentName, len(visibleReply), len(imageURLs), len(materialDirectives), len(attachmentPaths), truncate(visibleReply, 100))
	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}
	visibleReply = rewriteReplyWithAttachmentResults(visibleReply, sentPaths, failedPaths)
	if err := SendTextReply(ctx, client, msg.FromUserID, visibleReply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] trace=%s user=%s stage=reply-send status=failed err=%v", traceID, msg.FromUserID, err)
	} else {
		log.Printf("[handler] trace=%s user=%s stage=reply-send status=sent", traceID, msg.FromUserID)
	}
	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[materials] bot=%s user=%s 图片素材发送失败：url=%s err=%v", client.BotID(), msg.FromUserID, imgURL, err)
			continue
		}
		log.Printf("[materials] bot=%s user=%s 图片素材已发送：url=%s", client.BotID(), msg.FromUserID, imgURL)
	}
	for _, directive := range materialDirectives {
		if h.materialsSvc == nil {
			continue
		}
		item, mediaPath, err := h.materialsSvc.FindMediaPathByID(directive.ID)
		if err != nil {
			continue
		}
		mediaCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		if err := SendMediaFromPath(mediaCtx, client, msg.FromUserID, mediaPath, msg.ContextToken); err != nil {
			cancel()
			continue
		}
		cancel()
		log.Printf("[materials] bot=%s user=%s 素材已发送：素材=%q 类型=%s id=%s path=%s", client.BotID(), msg.FromUserID, item.Title, item.Kind, item.ID, mediaPath)
	}
}

func (h *Handler) chatWithRuntime(ctx context.Context, botID, userID, message string) (string, error) {
	if h.runtimeSvc == nil {
		return "", fmt.Errorf("runtime not configured")
	}
	memoryContext := ""
	if h.memorySvc != nil {
		memoryState := h.memorySvc.CollectRuntimeContext(botID)
		memoryContext = memoryState.LoadedContext
		if memoryContext == "" {
			log.Printf("[memory] bot=%s user=%s 当前没有可加载记忆", botID, userID)
		} else {
			log.Printf("[memory] bot=%s user=%s 已加载记忆上下文：长期画像 %d 条，短期记忆 %d 条，上下文 %d 字，预览=%q", botID, userID, memoryState.Result.LoadedProfiles, memoryState.Result.LoadedShortTerm, len(memoryContext), truncate(memoryContext, 120))
		}
	}
	materialsContext := ""
	materialsKeywords := []string(nil)
	materialsMatches := []materials.MaterialMatch(nil)
	if h.materialsSvc != nil {
		searchResult, err := h.materialsSvc.SearchForMessage(message, 5)
		if err == nil {
			materialsKeywords = searchResult.Keywords
			materialsMatches = searchResult.Matches
			materialsContext = h.materialsSvc.BuildRuntimeContext(searchResult)
			if len(searchResult.Matches) == 0 {
				if len(searchResult.Keywords) == 0 {
					log.Printf("[materials] bot=%s user=%s 当前没有提取到素材关键词", botID, userID)
				} else {
					log.Printf("[materials] bot=%s user=%s 当前没有命中可用素材，关键词=%q", botID, userID, strings.Join(searchResult.Keywords, ", "))
				}
			} else {
				log.Printf("[materials] bot=%s user=%s 已命中素材候选 %d 条，关键词=%q", botID, userID, len(searchResult.Matches), strings.Join(searchResult.Keywords, ", "))
				for _, match := range searchResult.Matches {
					log.Printf("[materials] bot=%s user=%s 候选素材=%q 类型=%s 分数=%d 命中原因=%s", botID, userID, match.Material.Title, match.Material.Kind, match.Score, formatMaterialReasonsForLog(match.Reasons))
				}
			}
		}
	}
	runtimeContext := joinRuntimeContexts(memoryContext, materialsContext)
	start := time.Now()
	log.Printf("[handler] bot=%s user=%s route=runtime state=start input=%q memory_chars=%d materials_chars=%d", botID, userID, truncate(message, 80), len(memoryContext), len(materialsContext))
	reply, err := h.runtimeSvc.Reply(ctx, userID, message, runtimeContext)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[handler] bot=%s user=%s route=runtime state=failed elapsed=%s err=%v", botID, userID, elapsed, err)
		return "", err
	}
	materialDirectives := ExtractMaterialDirectives(reply)
	imageURLs := ExtractImageURLs(reply)
	if h.materialsSvc != nil && len(materialDirectives) == 0 && len(imageURLs) == 0 {
		replyIntentResult, err := h.materialsSvc.SearchForReplyIntent(StripMaterialDirectives(reply), 1)
		if err == nil && len(replyIntentResult.Keywords) > 0 {
			log.Printf("[materials] bot=%s user=%s reply-intent keywords=%q", botID, userID, strings.Join(replyIntentResult.Keywords, ", "))
			if len(replyIntentResult.Matches) > 0 {
				match := replyIntentResult.Matches[0]
				reply = strings.TrimSpace(reply) + "\n[[material:id=" + match.Material.ID + ";kind=" + string(match.Material.Kind) + ";title=" + match.Material.Title + "]]"
				materialDirectives = ExtractMaterialDirectives(reply)
				log.Printf("[materials] bot=%s user=%s reply-intent 已追加素材指令：素材=%q 类型=%s id=%s", botID, userID, match.Material.Title, match.Material.Kind, match.Material.ID)
			} else {
				log.Printf("[materials] bot=%s user=%s reply-intent 当前没有命中可追加素材", botID, userID)
			}
		}
	}
	if len(materialsMatches) > 0 {
		if len(materialDirectives) == 0 && len(imageURLs) == 0 {
			log.Printf("[materials] bot=%s user=%s 本轮未采用素材候选", botID, userID)
		} else {
			for _, imageURL := range imageURLs {
				log.Printf("[materials] bot=%s user=%s 识别到图片素材使用意图：url=%s", botID, userID, imageURL)
			}
			for _, directive := range materialDirectives {
				log.Printf("[materials] bot=%s user=%s 识别到素材使用意图：素材=%q 类型=%s id=%s", botID, userID, directive.Title, directive.Kind, directive.ID)
			}
		}
	} else if len(materialsKeywords) > 0 {
		log.Printf("[materials] bot=%s user=%s 本轮素材关键词=%q，但无候选可供使用", botID, userID, strings.Join(materialsKeywords, ", "))
	}
	if h.memorySvc != nil {
		memoryResult := h.memorySvc.RecordRuntimeTurn(botID, userID, userID, message, StripMaterialDirectives(reply))
		if memoryResult.ShortTermRecorded {
			log.Printf("[memory] bot=%s user=%s 已写入短期记忆：%s", botID, userID, truncate(memoryResult.ShortTermSummary, 120))
		}
		if len(memoryResult.Profiles) == 0 {
			log.Printf("[memory] bot=%s user=%s 未识别到可写入长期画像的稳定表达，原话=%q", botID, userID, truncate(message, 80))
		} else {
			for _, action := range memoryResult.Profiles {
				actionText := "已写入长期用户画像"
				if action.Action == "updated" {
					actionText = "已更新长期用户画像"
				}
				log.Printf("[memory] bot=%s user=%s 识别到长期记忆意图：触发词=%q，分类=%s，内容=%q；%s", botID, userID, action.Trigger, action.Category, action.Content, actionText)
			}
			reply = appendMemoryConfirmation(reply, memoryResult.Profiles)
		}
	}
	if h.onRuntimeTurn != nil {
		h.onRuntimeTurn(ctx, botID, userID, message, StripMaterialDirectives(reply), memoryContext)
	}
	log.Printf("[handler] bot=%s user=%s route=runtime state=finished elapsed=%s preview=%q", botID, userID, elapsed, truncate(StripMaterialDirectives(reply), 100))
	return reply, nil
}

func appendMemoryConfirmation(reply string, actions []memory.MemoryProfileAction) string {
	if len(actions) == 0 {
		return reply
	}
	confirmation := strings.TrimSpace(actions[0].ConfirmationText())
	if confirmation == "" || strings.Contains(reply, confirmation) {
		return reply
	}
	if strings.TrimSpace(reply) == "" {
		return confirmation
	}
	return strings.TrimSpace(reply) + "\n\n" + confirmation
}

func joinRuntimeContexts(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, "\n\n")
}

func formatMaterialReasonsForLog(reasons []materials.MatchReason) string {
	if len(reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		label := string(reason.Source)
		switch reason.Source {
		case materials.MatchSourceTag:
			label = "标签"
		case materials.MatchSourceTitle:
			label = "标题"
		case materials.MatchSourceDescription:
			label = "用途说明"
		case materials.MatchSourceContent:
			label = "正文"
		}
		parts = append(parts, fmt.Sprintf("%s命中%q", label, reason.Keyword))
	}
	return strings.Join(parts, "；")
}

func (h *Handler) resetRuntimeSession(userID string) string {
	if h.runtimeSvc == nil {
		return "Runtime not configured."
	}
	h.runtimeSvc.ResetConversation(userID)
	if h.memorySvc != nil {
		if err := h.memorySvc.ClearShortTerm(userID); err != nil {
			log.Printf("[memory] clear short-term failed user=%s err=%v", userID, err)
		}
	}
	return fmt.Sprintf("已创建新的%s会话", h.runtimeName)
}

func (h *Handler) buildStatus() string {
	if h.runtimeSvc != nil {
		return fmt.Sprintf("agent: %s\ntype: runtime\nmodel: %s", h.runtimeName, "configured by provider")
	}
	return "agent: none (echo mode)"
}

func buildHelpText() string {
	return `Available commands:
/new or /clear - Start a new session
/info - Show current runtime info
/help - Show this help message`
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}


func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func detectImageExt(data []byte) string {
	if len(data) >= 12 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return ".png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	if len(data) >= 6 {
		header := string(data[:6])
		if header == "GIF87a" || header == "GIF89a" {
			return ".gif"
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	return ".img"
}

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	log.Printf("[handler] received image from %s, saving to %s", msg.FromUserID, h.saveDir)
	var data []byte
	var err error
	if img.URL != "" {
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] image has no URL or media info from %s", msg.FromUserID)
		return
	}
	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}
	ext := detectImageExt(data)
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath := filepath.Join(h.saveDir, fileName)
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}
	reply := fmt.Sprintf("Image saved to %s", fileName)
	_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
}
