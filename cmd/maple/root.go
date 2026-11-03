package main

import "github.com/spf13/cobra"

func newRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "maple",
		Short:   "Local MCP gateway shared by coding agents",
		Version: version,
	}
	cmd.PersistentFlags().Bool("json", false, "print output as JSON")
	return cmd
}
