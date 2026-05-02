package materials

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qiuy-collab/weone/config"
)

func stateDir() (string, error) {
	return config.StateDir()
}

func materialsDir() (string, error) {
	root, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "materials"), nil
}

func libraryPath() (string, error) {
	root, err := materialsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "library.json"), nil
}

func ensureMaterialsDir() error {
	root, err := materialsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create materials dir: %w", err)
	}
	return nil
}

func materialMediaDir(kind Kind) (string, error) {
	root, err := materialsDir()
	if err != nil {
		return "", err
	}
	switch normalizeKind(kind) {
	case KindImage:
		return filepath.Join(root, "images"), nil
	case KindVideo:
		return filepath.Join(root, "videos"), nil
	case KindFile:
		return filepath.Join(root, "files"), nil
	default:
		return "", fmt.Errorf("media dir unsupported for kind %q", kind)
	}
}

func ensureMaterialMediaDir(kind Kind) (string, error) {
	dir, err := materialMediaDir(kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create media dir: %w", err)
	}
	return dir, nil
}

func materialStoredPath(kind Kind, materialID string, originalName string) (string, error) {
	dir, err := ensureMaterialMediaDir(kind)
	if err != nil {
		return "", err
	}
	materialID = strings.TrimSpace(materialID)
	if materialID == "" {
		return "", fmt.Errorf("material id is required")
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(originalName)))
	if ext == "" {
		ext = defaultExtensionForKind(kind)
	}
	if ext == "" {
		ext = ".bin"
	}
	return filepath.Join(dir, materialID+ext), nil
}

func defaultExtensionForKind(kind Kind) string {
	switch normalizeKind(kind) {
	case KindImage:
		return ".png"
	case KindVideo:
		return ".mp4"
	case KindFile:
		return ".bin"
	default:
		return ""
	}
}
