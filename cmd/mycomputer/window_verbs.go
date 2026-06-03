package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/window"
)

// addTargetFlags adds the standard --id/--title/--class/--pid set to a
// cobra command and binds them to the given target pointer.
func addTargetFlags(cmd *cobra.Command, t *window.Target) {
	cmd.Flags().StringVar(&t.ID, "id", "", "target window id")
	cmd.Flags().StringVar(&t.Title, "title", "", "target substring in window title")
	cmd.Flags().StringVar(&t.Class, "class", "", "target WM_CLASS")
	cmd.Flags().Uint32Var(&t.PID, "pid", 0, "target process id")
}

func newWindowMoveCommand() *cobra.Command {
	var target window.Target
	var x, y int
	cmd := &cobra.Command{
		Use:   "window-move",
		Short: "Move a window to (x, y) screen coordinates via the platform window manager",
		Example: `  mycomputer window-move --class fam-ui --x 100 --y 200
  mycomputer window-move --title Firefox --x 0 --y 0 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Move(cmd.Context(), window.MoveRequest{Target: target, X: x, Y: y})
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	cmd.Flags().IntVar(&x, "x", 0, "destination x in screen pixels")
	cmd.Flags().IntVar(&y, "y", 0, "destination y in screen pixels")
	return cmd
}

func newWindowResizeCommand() *cobra.Command {
	var target window.Target
	var width, height int
	cmd := &cobra.Command{
		Use:   "window-resize",
		Short: "Resize a window to width x height via the platform window manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Resize(cmd.Context(), window.ResizeRequest{Target: target, Width: width, Height: height})
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	cmd.Flags().IntVar(&width, "width", 0, "new width in pixels")
	cmd.Flags().IntVar(&height, "height", 0, "new height in pixels")
	return cmd
}

func newWindowRaiseCommand() *cobra.Command {
	var target window.Target
	cmd := &cobra.Command{
		Use:   "window-raise",
		Short: "Activate and stack a window above its siblings",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Raise(cmd.Context(), target)
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	return cmd
}

func newWindowMinimizeCommand() *cobra.Command {
	var target window.Target
	cmd := &cobra.Command{
		Use:   "window-minimize",
		Short: "Iconify a window via WM_CHANGE_STATE",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Minimize(cmd.Context(), target)
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	return cmd
}

func newWindowMaximizeCommand() *cobra.Command {
	var target window.Target
	var axis string
	cmd := &cobra.Command{
		Use:   "window-maximize",
		Short: "Toggle a window's maximized state on the given axis (both, horz, vert)",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Maximize(cmd.Context(), window.MaximizeRequest{Target: target, Axis: axis})
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	cmd.Flags().StringVar(&axis, "axis", "both", "axis to maximize: both, horz, or vert")
	return cmd
}

func newWindowWorkspaceCommand() *cobra.Command {
	var target window.Target
	var index int
	cmd := &cobra.Command{
		Use:   "window-workspace",
		Short: "Move a window to the zero-based workspace index via _NET_WM_DESKTOP",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := window.Workspace(cmd.Context(), window.WorkspaceRequest{Target: target, Index: index})
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	cmd.Flags().IntVar(&index, "index", 0, "zero-based workspace index")
	return cmd
}

func newWindowCloseCommand() *cobra.Command {
	var target window.Target
	cmd := &cobra.Command{
		Use:   "window-close",
		Short: "Request that a window close via _NET_CLOSE_WINDOW (requires --allow-close)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			if !cfg.AllowClose {
				return contract.Precondition("PRECONDITION_CLOSE_NOT_ALLOWED", "window-close requires --allow-close; default is off", map[string]any{"target": target})
			}
			res, err := window.Close(cmd.Context(), target)
			if err != nil {
				return err
			}
			return printVerbResult(cmd, res)
		},
	}
	addTargetFlags(cmd, &target)
	return cmd
}

func printVerbResult(cmd *cobra.Command, res window.VerbResult) error {
	if rootOpts.JSON {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "window %s %s\n", res.Window.ID, res.Window.Title)
	_, _ = fmt.Fprintf(w, "  bounds: %d,%d,%d,%d\n", res.Window.Bounds.X, res.Window.Bounds.Y, res.Window.Bounds.Width, res.Window.Bounds.Height)
	_, _ = fmt.Fprintf(w, "  client_bounds: %d,%d,%d,%d\n", res.Window.ClientBounds.X, res.Window.ClientBounds.Y, res.Window.ClientBounds.Width, res.Window.ClientBounds.Height)
	if res.Warning != nil {
		_, _ = fmt.Fprintf(w, "  warning: %s — %s\n", res.Warning.Code, res.Warning.Message)
	}
	for _, note := range res.Notes {
		_, _ = fmt.Fprintf(w, "  note: %s\n", note)
	}
	return nil
}
