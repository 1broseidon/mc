package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/audit"
	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/input"
	"github.com/1broseidon/mc/internal/pipeline"
)

// clipboardOwnerEnv is the environment-variable handshake that switches
// a self-spawned child process into "owner daemon" mode. We use an env
// var (not a CLI flag) so the daemon does not appear in `ps` output as
// having any user-visible flag — it is an internal implementation
// detail, not a public surface. The CLI flag set is unchanged.
const clipboardOwnerEnv = "MYCOMPUTER_CLIPBOARD_OWNER_DAEMON"

// clipboardDaemonArgv0 is the argv[0] the daemon advertises to procfs
// after exec. We rewrite Args[0] in the parent before Start so `ps
// auxf` and `pgrep` show a human-friendly process name instead of the
// binary's full path. This is purely cosmetic — the kernel's view of
// the executable (/proc/<pid>/exe) is unchanged.
const clipboardDaemonArgv0 = "mycomputer-clipboard-daemon"

func newClipboardReadCommand() *cobra.Command {
	var selection, mime string
	cmd := &cobra.Command{
		Use:   "clipboard-read",
		Short: "Read the current value of an X11 selection (clipboard or primary)",
		Example: `  mycomputer clipboard-read --json
  mycomputer clipboard-read --selection primary --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := clipboard.Read(cmd.Context(), selection, mime)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), out.Content)
			return nil
		},
	}
	cmd.Flags().StringVar(&selection, "selection", "clipboard", "selection: clipboard or primary")
	cmd.Flags().StringVar(&mime, "mime", "text/plain", "MIME: text/plain or text/uri-list")
	return cmd
}

func newClipboardWriteCommand() *cobra.Command {
	var selection, mime, content string
	cmd := &cobra.Command{
		Use:   "clipboard-write",
		Short: "Write content to an X11 selection; spawns a detached owner daemon so the selection persists past command exit",
		Long: `clipboard-write takes ownership of the given X11 selection (CLIPBOARD by
default; PRIMARY or both also supported) and detaches a small owner
daemon so the selection survives the CLI exit. The daemon exits as
soon as another X11 client takes ownership of the same selection (e.g.
when the user copies something else), or when it receives SIGTERM.

Content is treated as private local data — it is NEVER echoed to logs
unless --verbose AND --log-clipboard are both set (currently a no-op
in v0.2; audit-log integration lands in task-6). The JSON result body
carries only byte count, MIME, and selection name — never the content
itself.`,
		Example: `  mycomputer clipboard-write --content 'hello world' --json
  cat file.txt | mycomputer clipboard-write --content "$(cat file.txt)" --selection both --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Daemon mode: content arrives on stdin (never on argv, to
			// avoid leaking clipboard contents through /proc/<pid>/cmdline).
			// Take ownership, block until SIGTERM/SIGINT or until
			// SelectionClear fires (another client takes ownership).
			if os.Getenv(clipboardOwnerEnv) == "1" {
				stdinBytes, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return contract.Dependency("CLIPBOARD_DAEMON_STDIN_READ_FAILED", "daemon failed to read content from stdin", map[string]any{"error": err.Error()})
				}
				return runClipboardOwnerDaemon(cmd, selection, mime, string(stdinBytes))
			}
			if content == "" {
				return contract.Validation("CONTENT_REQUIRED", "clipboard-write requires --content", nil)
			}
			// Resolve config once so --audit-full-payloads is applied
			// to the pipeline's audit writer before the write+audit
			// pair below. No-op when the flag is off.
			if _, err := effectiveConfig(); err != nil {
				return err
			}
			// Foreground mode: validate the request synchronously, then
			// spawn a detached child to hold ownership while we exit.
			if _, err := clipboard.Write(cmd.Context(), selection, content, mime); err != nil {
				return err
			}
			// We took ownership inside this process. To make the owner
			// persistent past CLI exit we hand the role to a daemon.
			out, err := spawnClipboardOwnerDaemon(selection, mime, content)
			if err != nil {
				return err
			}
			// Audit the write. Redaction is paranoid: bytes + mime +
			// selection only — the content itself is never logged.
			// See task-6 contract acceptance: grep for content probe
			// against the audit log MUST return zero matches.
			writer := pipeline.AuditWriter()
			batchID := newCLIBatchID()
			rec := audit.Record{
				BatchID: batchID,
				Type:    "clipboard_write",
				OK:      true,
				Clipboard: &audit.ClipboardSummary{
					Selection: out.Selection,
					Mime:      out.Mime,
					Bytes:     out.Bytes,
				},
			}
			// task-14: when --audit-full-payloads is on, also dump a
			// per-batch manifest. Clipboard content is scrubbed inside
			// the audit package; the manifest only retains selection,
			// mime, and byte count for this CLI surface.
			if writer != nil && writer.FullPayloads {
				rec.PayloadRef = batchID
				manifest := map[string]any{
					"schema_version": "0.2",
					"batch_id":       batchID,
					"actions": []map[string]any{{
						"type":      "clipboard_write",
						"selection": out.Selection,
						"mime":      out.Mime,
						"content":   content, // scrubbed before disk
					}},
				}
				if raw, err := json.Marshal(manifest); err == nil {
					_ = writer.WriteBatchPayload(batchID, raw)
				}
			}
			_ = writer.Append(rec)
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %d bytes to %s (%s)\n", out.Bytes, out.Selection, out.Mime)
			return nil
		},
	}
	cmd.Flags().StringVar(&selection, "selection", "clipboard", "selection: clipboard, primary, or both")
	cmd.Flags().StringVar(&mime, "mime", "text/plain", "MIME: text/plain or text/uri-list")
	cmd.Flags().StringVar(&content, "content", "", "content to write (required)")
	return cmd
}

// clipboardRuntimeDir returns the directory MyComputer uses for runtime
// state (PID files, lock files, etc.). Honors $XDG_RUNTIME_DIR per
// XDG Base Directory spec; falls back to /tmp/mycomputer-${UID} when
// the env var is unset so the daemon still has a stable writable path
// inside container/cron contexts. Directory is created with mode 0700
// (single-user state) when missing.
func clipboardRuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	var dir string
	if base != "" {
		dir = filepath.Join(base, "mycomputer")
	} else {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("mycomputer-%d", os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", contract.Dependency("CLIPBOARD_RUNTIME_DIR_FAILED", "failed to create clipboard runtime dir", map[string]any{"dir": dir, "error": err.Error()})
	}
	return dir, nil
}

// clipboardPidFilePath returns the absolute PID-file path used by the
// owner daemon for the canonical form of the given selection name. The
// canonicalization here matches canonSelectionName so the writer and
// reader (clipboard-status) agree even when callers pass shorthand
// like "" or mixed case.
func clipboardPidFilePath(selection string) (string, error) {
	dir, err := clipboardRuntimeDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("clipboard-%s.pid", canonSelectionName(selection))
	return filepath.Join(dir, name), nil
}

// writeClipboardPidFile writes the daemon's PID to its selection's PID
// file with mode 0600 (single-user). Idempotent: a stale file from a
// crashed previous daemon is overwritten. Errors are surfaced so the
// daemon can log them via stderr/journalctl, but the daemon does NOT
// abort on PID-file failure — selection ownership is the contract; the
// PID file is observability.
func writeClipboardPidFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// removeClipboardPidFile deletes the PID file unconditionally. Missing
// file is not an error (best-effort cleanup on shutdown).
func removeClipboardPidFile(path string) {
	_ = os.Remove(path)
}

// spawnClipboardOwnerDaemon launches `mycomputer clipboard-write
// --selection <s> --mime <m>` as a detached child process with the
// MYCOMPUTER_CLIPBOARD_OWNER_DAEMON=1 env var set. The clipboard
// content is delivered to the child via stdin — NEVER via argv — so
// it does not appear in /proc/<pid>/cmdline (privacy: the host
// shouldn't be able to ps-grep another user's clipboard payload).
//
// We wait briefly for the child to take ownership before returning so
// subsequent clipboard-read invocations see the new value. If the
// child fails to assume ownership within the handshake deadline we
// surface a Dependency error (CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT)
// rather than silently returning a fake-success WriteResult.
func spawnClipboardOwnerDaemon(selection, mime, content string) (clipboard.WriteResult, error) {
	exe, err := os.Executable()
	if err != nil {
		return clipboard.WriteResult{}, contract.Dependency("CLIPBOARD_DAEMON_SPAWN_FAILED", "failed to resolve own executable path", map[string]any{"error": err.Error()})
	}
	cmd := exec.Command(exe, "clipboard-write",
		"--selection", selection,
		"--mime", mime,
	)
	// Rewrite argv[0] so `ps auxf` shows "mycomputer-clipboard-daemon"
	// instead of the binary's full path. Path field still points at the
	// real exe so the kernel can exec it. exec.Command initialises
	// cmd.Args to [Path, ...]; we mutate the first element.
	if len(cmd.Args) > 0 {
		cmd.Args[0] = clipboardDaemonArgv0
	}
	cmd.Env = append(os.Environ(), clipboardOwnerEnv+"=1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Pipe content to the daemon via stdin. The daemon's RunE detects
	// clipboardOwnerEnv and reads stdin to EOF before taking ownership.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return clipboard.WriteResult{}, contract.Dependency("CLIPBOARD_DAEMON_SPAWN_FAILED", "failed to open stdin pipe to daemon", map[string]any{"error": err.Error()})
	}
	// Detach: new session, no controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return clipboard.WriteResult{}, contract.Dependency("CLIPBOARD_DAEMON_SPAWN_FAILED", "failed to start clipboard owner daemon", map[string]any{"error": err.Error()})
	}
	childPid := cmd.Process.Pid
	// Write content and close to signal EOF; if the write fails (e.g.,
	// child exited before reading), surface a handshake error.
	if _, werr := io.WriteString(stdin, content); werr != nil {
		_ = stdin.Close()
		return clipboard.WriteResult{}, contract.Dependency("CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT", "failed to deliver content to daemon via stdin", map[string]any{"error": werr.Error(), "child_pid": childPid})
	}
	if cerr := stdin.Close(); cerr != nil {
		return clipboard.WriteResult{}, contract.Dependency("CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT", "failed to close stdin pipe to daemon", map[string]any{"error": cerr.Error(), "child_pid": childPid})
	}
	// Release the child PID so it does not become a zombie.
	_ = cmd.Process.Release()
	// Brief settle: poll a few times to confirm the daemon has taken
	// ownership. We don't want the caller's immediate `clipboard-read`
	// to race the daemon's startup.
	const handshakeTimeoutMs = 750
	deadline := time.Now().Add(handshakeTimeoutMs * time.Millisecond)
	for time.Now().Before(deadline) {
		readSel := selection
		if readSel == clipboard.SelectionBoth {
			readSel = clipboard.SelectionClipboard
		}
		res, err := clipboard.Read(context.Background(), readSel, mime)
		if err == nil && res.Content == content {
			return clipboard.WriteResult{
				Selection: canonSelectionName(selection),
				Mime:      canonMime(mime),
				Bytes:     len(content),
			}, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Deadline elapsed without observing our content in the selection.
	// Probe whether the child is still alive (signal 0 is the
	// kill(2) liveness check) and report a Dependency error.
	childAlive := syscall.Kill(childPid, 0) == nil
	return clipboard.WriteResult{}, contract.Dependency(
		"CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT",
		"clipboard owner daemon did not take selection ownership within deadline",
		map[string]any{
			"timeout_ms":  handshakeTimeoutMs,
			"child_pid":   childPid,
			"child_alive": childAlive,
			"selection":   canonSelectionName(selection),
		},
	)
}

// runClipboardOwnerDaemon is the daemon entry point. It takes selection
// ownership and blocks until either SIGTERM/SIGINT/SIGHUP arrives or
// another X11 client takes ownership of the selection we are holding
// (SelectionClear from the owner event loop). Exits 0 on either path.
//
// PID-file lifecycle: written on successful Write (so the file always
// implies a live owner), removed via a deferred cleanup before return.
// Both the signal-shutdown and SelectionClear-shutdown paths fall
// through the same defer, so the file is gone whichever way we exit.
// If the daemon is killed with SIGKILL the file becomes stale; the
// clipboard-status reader is responsible for liveness verification via
// kill(pid, 0).
func runClipboardOwnerDaemon(cmd *cobra.Command, selection, mime, content string) error {
	if _, err := clipboard.Write(cmd.Context(), selection, content, mime); err != nil {
		// Daemon write failed — exit non-zero so the parent process
		// would notice (it won't, since we're detached). Still useful
		// for systemd/journalctl debugging.
		return err
	}
	pidPath, pidErr := clipboardPidFilePath(selection)
	if pidErr == nil {
		// Best-effort PID file write. We don't abort the daemon if the
		// runtime dir is read-only; selection ownership is the user
		// contract, the PID file is just observability for
		// clipboard-status.
		if werr := writeClipboardPidFile(pidPath, os.Getpid()); werr != nil {
			fmt.Fprintf(os.Stderr, "clipboard-daemon: pid-file write failed: %v\n", werr)
		}
		defer removeClipboardPidFile(pidPath)
	} else {
		fmt.Fprintf(os.Stderr, "clipboard-daemon: pid-file path failed: %v\n", pidErr)
	}
	lost, err := clipboard.Done()
	if err != nil {
		// Should not happen — Write() above already initialized the
		// singleton. Treat as a non-fatal degradation and fall back to
		// signal-only wait.
		lost = nil
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	select {
	case <-sigs:
	case <-lost:
		// Another X11 client took ownership; we have nothing left to
		// serve. Exit cleanly so we do not accumulate stale daemons.
	}
	return nil
}

func canonSelectionName(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "clipboard":
		return clipboard.SelectionClipboard
	case "primary":
		return clipboard.SelectionPrimary
	case "both":
		return clipboard.SelectionBoth
	default:
		return s
	}
}

func canonMime(s string) string {
	switch s {
	case "", "text/plain", "text/plain;charset=utf-8":
		return clipboard.MimeTextPlain
	case "text/uri-list":
		return clipboard.MimeTextURIList
	default:
		return s
	}
}

// ClipboardStatus is the JSON envelope returned by `clipboard-status`.
// Running reflects liveness verified via kill(pid, 0); a missing or
// stale PID file surfaces as running:false with pid:0 and
// age_seconds:0. Selection is always the canonical form (clipboard /
// primary / both) so consumers can branch on it without re-parsing.
type ClipboardStatus struct {
	Running    bool   `json:"running"`
	Pid        int    `json:"pid"`
	AgeSeconds int    `json:"age_seconds"`
	Selection  string `json:"selection"`
}

// newClipboardStatusCommand reports whether the detached clipboard
// owner daemon for a given selection is alive. It reads the PID file
// at $XDG_RUNTIME_DIR/mycomputer/clipboard-${selection}.pid and probes
// liveness with kill(pid, 0). Missing PID file or kill(2) returning
// ESRCH both surface as running:false. The age field is the wall-clock
// seconds since the PID file was last written (StartTime of the
// daemon's last reincarnation), capped at 0 if mtime is in the future.
func newClipboardStatusCommand() *cobra.Command {
	var selection string
	cmd := &cobra.Command{
		Use:   "clipboard-status",
		Short: "Report whether the detached clipboard owner daemon is alive",
		Long: `clipboard-status reads the PID file for the requested selection
(default: clipboard) at $XDG_RUNTIME_DIR/mycomputer/clipboard-${selection}.pid
and reports whether the owner daemon is still running. Liveness is
verified by sending signal 0 to the recorded PID. The age_seconds
field is the wall-clock seconds since the PID file mtime so callers
can detect a stale-but-recent record after an abrupt SIGKILL.`,
		Example: `  mycomputer clipboard-status --json
  mycomputer clipboard-status --selection primary --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			canonical := canonSelectionName(selection)
			out := ClipboardStatus{Selection: canonical}
			path, perr := clipboardPidFilePath(selection)
			if perr != nil {
				// Runtime dir unavailable — report not-running. We do
				// not propagate the error: clipboard-status is an
				// observability command, never a hard failure.
				if rootOpts.JSON {
					return writeJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running\tfalse\t%s\n", canonical)
				return nil
			}
			info, ierr := os.Stat(path)
			if ierr != nil {
				if rootOpts.JSON {
					return writeJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running\tfalse\t%s\n", canonical)
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				if rootOpts.JSON {
					return writeJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running\tfalse\t%s\n", canonical)
				return nil
			}
			pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
			if perr != nil || pid <= 0 {
				if rootOpts.JSON {
					return writeJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running\tfalse\t%s\n", canonical)
				return nil
			}
			alive := syscall.Kill(pid, 0) == nil
			age := int(time.Since(info.ModTime()).Seconds())
			if age < 0 {
				age = 0
			}
			out.Pid = pid
			out.AgeSeconds = age
			out.Running = alive
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running\t%t\t%s\tpid=%d\tage=%ds\n", out.Running, canonical, out.Pid, out.AgeSeconds)
			return nil
		},
	}
	cmd.Flags().StringVar(&selection, "selection", "clipboard", "selection: clipboard, primary, or both")
	return cmd
}

func newPasteCommand() *cobra.Command {
	var method, selection string
	cmd := &cobra.Command{
		Use:   "paste",
		Short: "Send a paste shortcut (ctrl+v or shift+insert) to the focused window",
		Example: `  mycomputer paste --json
  mycomputer paste --method insert --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if method == "" {
				method = "key"
			}
			var chord string
			switch method {
			case "key":
				chord = "ctrl+v"
			case "insert":
				chord = "shift+insert"
			default:
				return contract.Validation("PASTE_METHOD_INVALID", "paste method must be key or insert", map[string]any{"method": method})
			}
			if selection != "" && selection != clipboard.SelectionClipboard {
				return contract.Validation("CLIPBOARD_SELECTION_INVALID", "paste only supports the clipboard selection", map[string]any{"selection": selection})
			}
			if err := input.PressKey(cmd.Context(), chord); err != nil {
				return err
			}
			out := contract.ActionResult{Action: "paste", OK: true, Backend: "XTest", Details: map[string]any{"method": method, "chord": chord}}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "paste\ttrue\t%s\n", chord)
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "key", "paste method: key (ctrl+v) or insert (shift+insert)")
	cmd.Flags().StringVar(&selection, "selection", "clipboard", "selection (only clipboard supported)")
	return cmd
}

// newCLIBatchID returns a short random id used to tag standalone CLI
// audit records (clipboard-write etc.) with a synthetic batch_id so
// the opt-in payload manifest can be looked up by `audit replay`.
// Shape matches pipeline.newBatchID: 16 hex chars from 8 random bytes.
func newCLIBatchID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
