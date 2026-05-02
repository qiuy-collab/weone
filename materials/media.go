package materials

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

type MediaFile struct {
	Path         string
	OriginalName string
	MimeType     string
	FileSize     int64
}

func (m MediaFile) IsZero() bool {
	return strings.TrimSpace(m.Path) == ""
}

func detectMediaMetadata(path string, originalName string, explicitMimeType string, explicitSize int64) (MediaFile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return MediaFile{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return MediaFile{}, fmt.Errorf("stat media file: %w", err)
	}
	if info.IsDir() {
		return MediaFile{}, fmt.Errorf("media path must be a file")
	}
	mimeType := strings.TrimSpace(explicitMimeType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	}
	if originalName == "" {
		originalName = filepath.Base(path)
	}
	fileSize := explicitSize
	if fileSize <= 0 {
		fileSize = info.Size()
	}
	return MediaFile{
		Path:         path,
		OriginalName: originalName,
		MimeType:     mimeType,
		FileSize:     fileSize,
	}, nil
}
