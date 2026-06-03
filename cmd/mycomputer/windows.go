package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/window"
)

func newWindowsCommand() *cobra.Command {
	var detectRendered bool
	cmd := &cobra.Command{
		Use:   "windows",
		Short: "List top-level windows",
		Example: `  mycomputer windows
  mycomputer windows --json
  mycomputer windows --minimal
  mycomputer windows --detect-rendered --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wins, err := window.List(cmd.Context())
			if err != nil {
				return err
			}
			if detectRendered {
				return runWindowsWithDetect(cmd, wins)
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
	cmd.Flags().BoolVar(&detectRendered, "detect-rendered", false, "estimate each window's actually-rendered surface (extra XGetImage per window; default off)")
	return cmd
}

// renderedWindow extends contract.WindowInfo with the optional
// rendered_bounds_estimate field surfaced by --detect-rendered. The
// embedded struct keeps the existing wire fields intact; the extra
// field is omitted when the estimator declined to run for this window
// (e.g., empty bounds, sampling failure, no detected divergence).
type renderedWindow struct {
	contract.WindowInfo
	RenderedBoundsEstimate *contract.Bounds `json:"rendered_bounds_estimate,omitempty"`
}

func runWindowsWithDetect(cmd *cobra.Command, wins []contract.WindowInfo) error {
	enriched := make([]renderedWindow, 0, len(wins))
	for _, win := range wins {
		rec := renderedWindow{WindowInfo: win}
		if estimate, ok := window.EstimateRenderedBounds(win); ok {
			b := estimate
			rec.RenderedBoundsEstimate = &b
		}
		enriched = append(enriched, rec)
	}
	if rootOpts.JSON {
		return writeJSON(cmd.OutOrStdout(), struct {
			Windows any `json:"windows"`
		}{Windows: enriched})
	}
	if rootOpts.Minimal {
		for _, win := range enriched {
			rendered := "-"
			if win.RenderedBoundsEstimate != nil {
				b := *win.RenderedBoundsEstimate
				rendered = fmt.Sprintf("%d,%d,%d,%d", b.X, b.Y, b.Width, b.Height)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d\t%d,%d,%d,%d\t%s\n",
				win.ID, win.Class, win.Title, win.PID,
				win.Bounds.X, win.Bounds.Y, win.Bounds.Width, win.Bounds.Height,
				rendered)
		}
		return nil
	}
	rows := [][]string{{"ID", "FOCUSED", "PID", "CLASS", "TITLE", "BOUNDS", "CLIENT_BOUNDS", "RENDERED_EST"}}
	for _, win := range enriched {
		rendered := "-"
		if win.RenderedBoundsEstimate != nil {
			b := *win.RenderedBoundsEstimate
			rendered = fmt.Sprintf("%d,%d,%d,%d", b.X, b.Y, b.Width, b.Height)
		}
		rows = append(rows, []string{
			win.ID,
			fmt.Sprint(win.Focused),
			fmt.Sprint(win.PID),
			win.Class,
			truncate(win.Title, rootOpts.MaxChars),
			fmt.Sprintf("%d,%d,%d,%d", win.Bounds.X, win.Bounds.Y, win.Bounds.Width, win.Bounds.Height),
			fmt.Sprintf("%d,%d,%d,%d", win.ClientBounds.X, win.ClientBounds.Y, win.ClientBounds.Width, win.ClientBounds.Height),
			rendered,
		})
	}
	printTable(cmd.OutOrStdout(), rows)
	return nil
}
