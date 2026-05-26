package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/screen"
)

// newScreenInfoCommand exposes the screen.Info / get_screen_info MCP
// tool as a CLI command. Surfaces the full monitor list including
// per-monitor scale, primary flag, and refresh rate so agents can
// drive point.space="monitor" without needing the MCP transport. The
// top-level screen.bounds field stays the bounding box of all
// monitors for v0.1 wire compatibility.
func newScreenInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get-screen-info",
		Aliases: []string{"screen-info"},
		Short:   "Return screen bounds and per-monitor geometry",
		Example: `  mycomputer get-screen-info --json
  mycomputer get-screen-info --json | jq '.monitors[] | {index, name, primary, scale, refresh_hz}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := screen.Info(cmd.Context())
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "screen\t%d,%d,%d,%d\n", info.Bounds.X, info.Bounds.Y, info.Bounds.Width, info.Bounds.Height)
			rows := [][]string{{"index", "name", "bounds", "scale", "primary", "refresh_hz"}}
			for _, mon := range info.Monitors {
				rows = append(rows, []string{
					strconv.Itoa(mon.Index),
					mon.Name,
					fmt.Sprintf("%d,%d,%d,%d", mon.Bounds.X, mon.Bounds.Y, mon.Bounds.Width, mon.Bounds.Height),
					strconv.FormatFloat(mon.Scale, 'f', 3, 64),
					strconv.FormatBool(mon.Primary),
					strconv.Itoa(mon.RefreshHz),
				})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	return cmd
}
