package materials

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxImportTextBytes   = 256 * 1024
	maxImportSampleRunes = 4000
)

type ExtractedText struct {
	FullText string
	Sample   string
}

func detectImportKind(fileName, mimeType string) Kind {
	name := strings.ToLower(strings.TrimSpace(fileName))
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	ext := strings.ToLower(filepath.Ext(name))

	if strings.HasPrefix(mimeType, "image/") || isImportImageExt(name) {
		return KindImage
	}
	if strings.HasPrefix(mimeType, "video/") || isImportVideoExt(name) {
		return KindVideo
	}
	if isTextLikeExtension(ext) || strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "json") || strings.Contains(mimeType, "xml") {
		return KindText
	}
	return KindFile
}

func extractImportText(fileName, mimeType string, data []byte) ExtractedText {
	if detectImportKind(fileName, mimeType) != KindText {
		return ExtractedText{}
	}
	if len(data) == 0 {
		return ExtractedText{}
	}

	chunk := data
	if len(chunk) > maxImportTextBytes {
		chunk = chunk[:maxImportTextBytes]
	}
	if !utf8.Valid(chunk) {
		chunk = bytes.ToValidUTF8(chunk, []byte(""))
	}
	text := strings.TrimSpace(string(chunk))
	if text == "" {
		return ExtractedText{}
	}
	return ExtractedText{
		FullText: text,
		Sample:   truncateRunes(text, maxImportSampleRunes),
	}
}

func isImportImageExt(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	default:
		return false
	}
}

func isImportVideoExt(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	default:
		return false
	}
}

func isTextLikeExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".xml", ".yaml", ".yml", ".log":
		return true
	default:
		return false
	}
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n..."
}
