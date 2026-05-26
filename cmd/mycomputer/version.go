package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build metadata",
		Example: `  mycomputer version
  mycomputer version --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := versionInfo()
			if rootOpts.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "mycomputer: go=%s commit=%s built=%s\n", info.Go, info.Commit, info.Built)
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mycomputer %s (commit %s, built %s)\n", info.Version, info.Commit, info.Built)
			return nil
		},
	}
}
