package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/diagnostic"
	"github.com/1broseidon/mc/internal/mcpserver"
)

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report desktop readiness and blockers",
		Example: `  mycomputer doctor
  mycomputer doctor --json | jq '.readiness.status'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			started := time.Now()
			// Instantiate the MCP server only to populate the package-level
			// tool catalog. We discard the server itself — diagnostic.Doctor
			// can't import mcpserver (cycle), so the CLI bridges the two by
			// passing the catalog names in. Single source of truth: add()
			// calls inside mcpserver.New().
			_ = mcpserver.New(mcpserver.Options{Version: versionInfo(), Config: cfg})
			catalog := mcpserver.Catalog()
			toolNames := make([]string, len(catalog))
			for i, t := range catalog {
				toolNames[i] = t.Name
			}
			report := diagnostic.Doctor(versionInfo(), cfg, toolNames)
			if rootOpts.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "mycomputer: doctor probed %d backends in %s\n", len(report.Backends), time.Since(started).Round(time.Millisecond))
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Readiness: %s\n", report.Readiness.Status)
			if len(report.Readiness.Blockers) > 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Blockers:")
				for _, blocker := range report.Readiness.Blockers {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", blocker)
				}
			}
			if len(report.Readiness.Warnings) > 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
				for _, warning := range report.Readiness.Warnings {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Next: %s\n", report.Readiness.NextAction)
			return nil
		},
	}
}
