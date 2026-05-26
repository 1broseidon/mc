package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/pipeline"
	"github.com/1broseidon/mc/internal/screen"
)

var (
	rootOpts          config.Options
	errAlreadyPrinted = errors.New("error already printed")
)

func execute() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cmd := newRootCommand()
	return cmd.ExecuteContext(ctx)
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "mycomputer",
		Short:         "Go-native X11 computer use for Linux agents",
		Long:          "MyComputer is a local CLI and MCP server for X11 desktop computer use on Linux.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&rootOpts.JSON, "json", false, "emit machine-readable JSON for data commands")
	root.PersistentFlags().BoolVar(&rootOpts.Minimal, "minimal", false, "emit bounded tab-separated output where supported")
	root.PersistentFlags().IntVar(&rootOpts.MaxChars, "max-chars", 0, "truncate long textual fields to N characters (0 = default)")
	root.PersistentFlags().BoolVar(&rootOpts.NoColor, "no-color", false, "disable ANSI output even on a TTY")
	root.PersistentFlags().StringVar(&rootOpts.Config, "config", "", "load config from an explicit file")
	root.PersistentFlags().BoolVarP(&rootOpts.Quiet, "quiet", "q", false, "suppress non-essential diagnostics")
	root.PersistentFlags().BoolVarP(&rootOpts.Verbose, "verbose", "v", false, "emit extra diagnostics to stderr")
	// --respect-user: yield watcher is wired by task-6. Default true.
	// The resolved value is surfaced via doctor.session.respect_user.
	// Precedence: flag > env MYCOMPUTER_RESPECT_USER > config > default(true).
	root.PersistentFlags().BoolVar(&rootOpts.RespectUser, "respect-user", true, "yield to the human user when they are actively driving the desktop; pauses action batches when real input is detected (default true on interactive sessions)")
	// --allow-close gates window_close (off by default). Surfaces in
	// doctor.session.allow_close. Precedence: flag > env
	// MYCOMPUTER_ALLOW_CLOSE > config > default(false).
	root.PersistentFlags().BoolVar(&rootOpts.AllowClose, "allow-close", false, "permit window_close actions; disabled by default")
	// --logical-coords is an EXPERIMENTAL opt-in HiDPI translation
	// flag (task-7). When set MyComputer divides screenshot output
	// dimensions by the primary monitor's RandR-derived scale and
	// multiplies input coordinates by the same scale before XTest.
	// Off by default. Production agents should stick with physical
	// pixels; this flag is provided so HiDPI-aware agents that report
	// logical coordinates can opt into automatic translation while
	// the underlying transport remains physical-pixel-only.
	root.PersistentFlags().BoolVar(&rootOpts.LogicalCoords, "logical-coords", false, "EXPERIMENTAL: translate between logical (scaled) and physical pixels using the primary monitor's RandR scale. Off by default")
	// --dry-run: validate + resolve every action but skip mutating
	// ops (click/type/paste/window_*/drag/scroll/move_mouse/
	// set_text/perform_action/clipboard_write). Result envelope flags
	// dry_run:true per action and includes the resolved coords that
	// would have been used. find_*/wait_*/observe/screenshot/
	// clipboard_read still execute against the real desktop.
	root.PersistentFlags().BoolVar(&rootOpts.DryRun, "dry-run", false, "validate and resolve actions without mutating the desktop (preview mode)")
	// --audit-screenshots: capture screenshot before/after each
	// mutating action and record paths in the audit log. Off by
	// default (expensive).
	root.PersistentFlags().BoolVar(&rootOpts.AuditScreenshots, "audit-screenshots", false, "capture and record before/after screenshots in the audit log (expensive)")
	// --audit-full-payloads: opt-in per-batch payload manifest stored
	// at <audit_dir>/payloads/<batch_id>.json (mode 0600). The manifest
	// lets `mycomputer audit replay <batch_id>` reconstruct the
	// original ActionBatch instead of the v0.2 type-only fallback.
	// Clipboard `content` is STILL redacted in payload files — the
	// opt-in only removes redaction on non-clipboard inputs (click
	// coords, target selectors, find queries, etc.). The privacy
	// invariant on clipboard content is non-negotiable; this flag does
	// NOT widen it. Off by default.
	root.PersistentFlags().BoolVar(&rootOpts.AuditFullPayloads, "audit-full-payloads", false, "persist a per-batch sealed payload manifest (clipboard content still redacted) so 'audit replay' can reconstruct full inputs")
	cobra.OnInitialize(func() {
		// Capture whether the user set --respect-user explicitly so the
		// config layer can distinguish "explicit false" from "unset"
		// when applying precedence.
		if root.PersistentFlags().Changed("respect-user") {
			rootOpts.RespectUserSet = true
		}
		if root.PersistentFlags().Changed("allow-close") {
			rootOpts.AllowCloseSet = true
		}
		if root.PersistentFlags().Changed("logical-coords") {
			rootOpts.LogicalCoordsSet = true
		}
		if root.PersistentFlags().Changed("dry-run") {
			rootOpts.DryRunSet = true
		}
		if root.PersistentFlags().Changed("audit-screenshots") {
			rootOpts.AuditScreenshotsSet = true
		}
		if root.PersistentFlags().Changed("audit-full-payloads") {
			rootOpts.AuditFullPayloadsSet = true
		}
	})

	root.AddCommand(newVersionCommand())
	root.AddCommand(newConfigCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newServeCommand())
	root.AddCommand(newCaptureCommand())
	root.AddCommand(newScreenInfoCommand())
	root.AddCommand(newWindowsCommand())
	root.AddCommand(newFocusCommand())
	root.AddCommand(newObserveCommand())
	root.AddCommand(newActionsCommand())
	root.AddCommand(newFindTextCommand())
	root.AddCommand(newFindImageCommand())
	root.AddCommand(newFindColorCommand())
	root.AddCommand(newBrowseCommand())
	root.AddCommand(newWindowMoveCommand())
	root.AddCommand(newWindowResizeCommand())
	root.AddCommand(newWindowRaiseCommand())
	root.AddCommand(newWindowMinimizeCommand())
	root.AddCommand(newWindowMaximizeCommand())
	root.AddCommand(newWindowWorkspaceCommand())
	root.AddCommand(newWindowCloseCommand())
	root.AddCommand(newWaitForWindowCommand())
	root.AddCommand(newWaitForPixelChangeCommand())
	root.AddCommand(newWaitForTextCommand())
	root.AddCommand(newClipboardReadCommand())
	root.AddCommand(newClipboardWriteCommand())
	root.AddCommand(newClipboardStatusCommand())
	root.AddCommand(newPasteCommand())
	root.AddCommand(newTypeTextCommand())
	root.AddCommand(newAuditCommand())
	root.AddCommand(newConventionsCommand())
	root.AddCommand(newCompletionCommand(root))
	return root
}

func effectiveConfig() (config.Effective, error) {
	eff, err := config.Load(rootOpts)
	if err == nil {
		// Propagate the experimental --logical-coords toggle to the
		// screen/input packages. Idempotent across repeated command
		// runs; default false keeps physical-pixel behavior.
		screen.SetLogicalCoords(eff.LogicalCoords)
		// Bridge --audit-full-payloads into the pipeline's audit
		// writer. Idempotent: writes the boolean each call. Default
		// false leaves the v0.2 type-only audit shape intact.
		if w := pipeline.AuditWriter(); w != nil {
			w.FullPayloads = eff.AuditFullPayloads
		}
	}
	return eff, err
}

func versionInfo() contract.VersionInfo {
	return contract.VersionInfo{Version: version, Commit: commit, Built: built, Go: runtime.Version()}
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeError(w io.Writer, jsonMode bool, err error) {
	if err == nil {
		return
	}
	if jsonMode {
		_, _ = fmt.Fprintln(w, string(contract.MarshalError(err)))
		return
	}
	_, _ = fmt.Fprintln(w, err.Error())
}

func printTable(w io.Writer, rows [][]string) {
	widths := []int{}
	for _, row := range rows {
		for i, col := range row {
			if len(widths) <= i {
				widths = append(widths, 0)
			}
			if len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}
	for _, row := range rows {
		for i, col := range row {
			if i > 0 {
				_, _ = fmt.Fprint(w, "  ")
			}
			if i == len(row)-1 {
				_, _ = fmt.Fprint(w, col)
				continue
			}
			_, _ = fmt.Fprintf(w, "%-*s", widths[i], col)
		}
		_, _ = fmt.Fprintln(w)
	}
}

func truncate(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	if maxChars <= 3 {
		return s[:maxChars]
	}
	return s[:maxChars-3] + "..."
}

// isInteractive reports whether MyComputer is running attached to a
// terminal. The yield/respect-user system uses this to decide whether
// to spawn the XInput2 watcher by default: under MCP server mode
// (headless, stdio transport) the server should NOT yield to the
// human, because there's no human at the keyboard MC is currently
// driving. Under interactive CLI use (`mycomputer actions
// --input-file ...`) the watcher IS started.
//
// We treat "TTY on stdin OR stdout" as interactive. Both being non-
// TTY (e.g., MCP stdio transport pipes both ends) is non-interactive.
func isInteractive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		if fi, err := f.Stat(); err == nil {
			if fi.Mode()&os.ModeCharDevice != 0 {
				return true
			}
		}
	}
	return false
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
