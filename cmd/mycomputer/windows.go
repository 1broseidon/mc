package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/window"
)

func newWindowsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "windows",
		Short: "List X11 top-level windows",
		Example: `  mycomputer windows
  mycomputer windows --json
  mycomputer windows --minimal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wins, err := window.List(cmd.Context())
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), struct {
					Windows any `json:"windows"`
				}{Windows: wins})
			}
			if rootOpts.Minimal {
				for _, win := range wins {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d\t%d,%d,%d,%d\n", win.ID, win.Class, win.Title, win.PID, win.Bounds.X, win.Bounds.Y, win.Bounds.Width, win.Bounds.Height)
				}
				return nil
			}
			rows := [][]string{{"ID", "FOCUSED", "PID", "CLASS", "TITLE", "BOUNDS", "CLIENT_BOUNDS"}}
			for _, win := range wins {
				rows = append(rows, []string{
					win.ID,
					fmt.Sprint(win.Focused),
					fmt.Sprint(win.PID),
					win.Class,
					truncate(win.Title, rootOpts.MaxChars),
					fmt.Sprintf("%d,%d,%d,%d", win.Bounds.X, win.Bounds.Y, win.Bounds.Width, win.Bounds.Height),
					fmt.Sprintf("%d,%d,%d,%d", win.ClientBounds.X, win.ClientBounds.Y, win.ClientBounds.Width, win.ClientBounds.Height),
				})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
}
