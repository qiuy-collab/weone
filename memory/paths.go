package memory

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/qiuy-collab/weone/config"
)

func stateDir() (string, error) {
	return config.StateDir()
}

func memoryDir() (string, error) {
	root, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "memory"), nil
}

func profilesDir() (string, error) {
	root, err := memoryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "profiles"), nil
}

func shortTermDBPath() (string, error) {
	root, err := memoryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "short_term.db"), nil
}

func ensureMemoryDirs() error {
	root, err := memoryDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	profiles, err := profilesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		return fmt.Errorf("create profiles dir: %w", err)
	}
	return nil
}
