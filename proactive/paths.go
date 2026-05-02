package proactive

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/qiuy-collab/weone/config"
)

func stateDir() (string, error) {
	return config.StateDir()
}

func proactiveDir() (string, error) {
	root, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "proactive"), nil
}

func tasksPath() (string, error) {
	root, err := proactiveDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tasks.json"), nil
}

func ensureProactiveDir() error {
	root, err := proactiveDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create proactive dir: %w", err)
	}
	return nil
}
