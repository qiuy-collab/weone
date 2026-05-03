package materials

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type OCRResult struct {
	ExtractedText
	Backend string
	Skipped bool
	Reason  string
}

func extractImageText(ctx context.Context, fileName string, data []byte) (OCRResult, error) {
	backendPath, err := resolveTesseractPath()
	if err != nil {
		return OCRResult{Skipped: true, Reason: "backend-unavailable"}, nil
	}
	if len(data) == 0 {
		return OCRResult{Skipped: true, Reason: "empty-image"}, nil
	}

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(fileName)))
	if ext == "" {
		ext = ".img"
	}
	tmp, err := os.CreateTemp("", "weone-ocr-*"+ext)
	if err != nil {
		return OCRResult{}, fmt.Errorf("create temp image: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return OCRResult{}, fmt.Errorf("write temp image: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return OCRResult{}, fmt.Errorf("close temp image: %w", err)
	}

	cmd := exec.CommandContext(ctx, backendPath, tmpPath, "stdout", "-l", "chi_sim+eng")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(bytes.TrimSpace(exitErr.Stderr)))
			if stderr == "" {
				stderr = strings.TrimSpace(exitErr.Error())
			}
			return OCRResult{}, fmt.Errorf("tesseract failed: %s", stderr)
		}
		return OCRResult{}, fmt.Errorf("run tesseract: %w", err)
	}

	text := strings.TrimSpace(string(bytes.ToValidUTF8(output, []byte(""))))
	if text == "" {
		return OCRResult{Backend: "tesseract", Skipped: true, Reason: "empty-result"}, nil
	}
	return OCRResult{
		ExtractedText: ExtractedText{
			FullText: text,
			Sample:   truncateRunes(text, maxImportSampleRunes),
		},
		Backend: "tesseract",
	}, nil
}

func resolveTesseractPath() (string, error) {
	candidates := []string{
		"tesseract",
		`C:\Program Files\Tesseract-OCR\tesseract.exe`,
		`C:\Program Files (x86)\Tesseract-OCR\tesseract.exe`,
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
		if candidate != "tesseract" {
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate, nil
			}
		}
	}
	return "", exec.ErrNotFound
}
