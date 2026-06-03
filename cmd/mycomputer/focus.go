package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/window"
)

func newFocusCommand() *cobra.Command {
	var target window.Target
	cmd := &cobra.Command{
		Use:   "focus",
		Short: "Focus a window by id, title, class, or PID",
		Example: `  mycomputer focus --title Firefox
  mycomputer focus --id 0x4200007 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			win, err := window.Focus(cmd.Context(), target)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), win)
			}
			if !rootOpts.Quiet {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "focused %s %s\n", win.ID, win.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target.ID, "id", "", "target window id")
	cmd.Flags().StringVar(&target.Title, "title", "", "target substring in window title")
	cmd.Flags().StringVar(&target.Class, "class", "", "target WM_CLASS")
	cmd.Flags().Uint32Var(&target.PID, "pid", 0, "target process id")
	return cmd
}
