package main

import (
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/mcpserver"
)

func newServeCommand() *cobra.Command {
	// displayOverride is bound to --display. Captured per-command so
	// callers may pass an explicit DISPLAY value when launching from
	// MCP hosts whose env doesn't inherit it (Codex / Claude Code
	// spawned via .desktop launcher, systemd unit, IDE terminal).
	// The override is applied before mcpserver.New() so every backend
	// that consults os.Getenv("DISPLAY") sees the chosen value.
	var displayOverride string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the stdio MCP server",
		Example: `  mycomputer serve
  mycomputer serve --display :1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if displayOverride != "" {
				// Only mutates this process and its children; the
				// parent MCP host's env is never touched.
				_ = os.Setenv("DISPLAY", displayOverride)
			}
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			server := mcpserver.New(mcpserver.Options{Version: versionInfo(), Config: cfg})
			return server.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&displayOverride, "display", "", "explicit display value (e.g. :0, :1) for MCP hosts that don't propagate DISPLAY")
	return cmd
}
