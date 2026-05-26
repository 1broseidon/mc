package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/mcpserver"
)

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "serve",
		Short:   "Run the stdio MCP server",
		Example: `  mycomputer serve`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			server := mcpserver.New(mcpserver.Options{Version: versionInfo(), Config: cfg})
			return server.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
