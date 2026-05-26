package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/pipeline"
)

func newObserveCommand() *cobra.Command {
	var screenshot bool
	cmd := &cobra.Command{
		Use:   "observe",
		Short: "Return combined desktop state",
		Example: `  mycomputer observe --json
  mycomputer observe --screenshot --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := pipeline.Observe(cmd.Context(), screenshot)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "screen\t%d,%d,%d,%d\n", out.Screen.Bounds.X, out.Screen.Bounds.Y, out.Screen.Bounds.Width, out.Screen.Bounds.Height)
			if out.FocusedWindow != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "focused\t%s\t%s\n", out.FocusedWindow.ID, out.FocusedWindow.Title)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "windows\t%d\n", len(out.Windows))
			if out.Cursor != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cursor\t%d,%d\n", out.Cursor.X, out.Cursor.Y)
			}
			if out.Screenshot != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "screenshot\t%s\t%s\n", out.Screenshot.ImagePath, out.Screenshot.CoordMap)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&screenshot, "screenshot", false, "include screenshot metadata in observation")
	return cmd
}
