package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the background weone process",
	RunE: func(cmd *cobra.Command, args []string) error {
		if running, pid := currentInstancePID(); running {
			fmt.Printf("Stopping weone (pid=%d)...\n", pid)
			if stopProcess(pid) {
				_ = os.Remove(pidFile())
			} else {
				return fmt.Errorf("failed to stop weone (pid=%d)", pid)
			}
		}

		// Start
		fmt.Println("Starting weone...")
		return runDaemon(resolveAPIAddr(""))
	},
}
