//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func processExistsPlatform(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	result := strings.TrimSpace(string(out))
	if result == "" || strings.HasPrefix(result, "INFO:") {
		return false
	}
	return strings.Contains(result, fmt.Sprintf(",\"%d\"", pid))
}

func terminateProcess(pid int, force bool) error {
	args := []string{"/PID", fmt.Sprintf("%d", pid)}
	if force {
		args = append([]string{"/F"}, args...)
	}
	cmd := exec.Command("taskkill", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
