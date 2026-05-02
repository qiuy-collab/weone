package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background weone process",
	RunE: func(cmd *cobra.Command, args []string) error {
		if stopAllWeclaw() {
			fmt.Println("weone stopped")
			return nil
		}
		fmt.Println("weone is not running")
		return nil
	},
}
