// Package audit writes a JSONL trail of every executed MyComputer
// action to $XDG_STATE_HOME/mycomputer/audit/YYYY-MM-DD.jsonl
// (falling back to ~/.local/state/mycomputer/audit). One line per
// action; clipboard content is redacted to bytes+mime only.
//
// Writes are best-effort: a failure to write the audit line MUST
// never block the action itself. Callers log the failure to stderr
// (or pass a logger) and continue.
//
// Optional opt-in: when Writer.FullPayloads is true the writer also
// stores per-batch payload JSON under <Dir>/payloads/<batch_id>.json
// (mode 0600) so `mycomputer audit replay` can reconstruct the
// original ActionBatch instead of the type-only fallback. Clipboard
// content is STILL redacted inside payload files — the opt-in only
// removes redaction on non-clipboard inputs (click coords, selectors,
// find queries, etc.). The privacy invariant on clipboard content is
// non-negotiable: it never reaches disk under any flag.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultRetentionDays is the number of days of JSONL files retained
// in the audit directory. Configurable per-Writer.
const DefaultRetentionDays = 14

// Record is one line of the audit JSONL stream. Keep field names
// stable — agents may grep/jq this file.
type Record struct {
	TS             time.Time      `json:"ts"`
	BatchID        string         `json:"batch_id,omitempty"`
	ActionIndex    int            `json:"action_index"`
	Type           string         `json:"type"`
	Target         map[string]any `json:"target,omitempty"`
	ResolvedCoords *Point         `json:"resolved_coords,omitempty"`
	Backend        string         `json:"backend,omitempty"`
	OK             bool           `json:"ok"`
	Error          string         `json:"error,omitempty"`
	ElapsedMS      int64          `json:"elapsed_ms"`
	// DryRun is OMIT-WHEN-FALSE by design: dry-run is rare in routine
	// audit streams, and keeping the JSONL dense matters more than
	// always-present bool symmetry. Callers MUST treat a missing field
	// as false; never emit dry_run:null or dry_run:false. This shape
	// is the single source of truth for the audit-record schema.
	DryRun  bool `json:"dry_run,omitempty"`
	Skipped bool `json:"skipped,omitempty"`
	// ScreenshotBefore / After hold optional paths when the writer
	// is configured to record screenshots. Off by default; expensive.
	ScreenshotBefore string `json:"screenshot_before,omitempty"`
	ScreenshotAfter  string `json:"screenshot_after,omitempty"`
	// Clipboard mirrors task-5 redaction rules: bytes + mime only,
	// content NEVER appears.
	Clipboard *ClipboardSummary `json:"clipboard,omitempty"`
	// YieldEvent, when set, marks this record as a yield observation
	// (no action ran). Used for the "user input observed" trail when
	// --respect-user=false leaves execution running anyway.
	YieldEvent *YieldSummary `json:"yield_event,omitempty"`
	// PayloadRef, when set, names the per-batch payload manifest under
	// <Dir>/payloads/<batch_id>.json that contains the full ActionBatch
	// inputs (clipboard content scrubbed). Present only when the writer
	// was configured with FullPayloads=true. Absent in default v0.2
	// behavior. The string value equals the BatchID.
	PayloadRef string `json:"payload_ref,omitempty"`
}

// Point is a tiny screen-space coord written into the audit log.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// ClipboardSummary is the redacted clipboard envelope written to
// audit. Bytes and Mime only — never Content.
type ClipboardSummary struct {
	Selection string `json:"selection,omitempty"`
	Mime      string `json:"mime,omitempty"`
	Bytes     int    `json:"bytes"`
}

// YieldSummary is the per-line yield observation envelope.
type YieldSummary struct {
	DeviceID  int    `json:"device_id"`
	SourceID  int    `json:"source_id"`
	EventType string `json:"event_type"`
}

// Writer appends Records to today's JSONL file. Safe for concurrent
// use; serializes writes under a mutex.
type Writer struct {
	// Dir is the audit directory. When empty, DefaultDir() is used.
	Dir string
	// RetentionDays controls rotation. <= 0 means use the default.
	RetentionDays int
	// FullPayloads, when true, enables the opt-in per-batch payload
	// manifest under <Dir>/payloads/<batch_id>.json. JSONL records
	// written via Append still go through the standard scrubRecord
	// path. WriteBatchPayload is a no-op when this is false.
	// Clipboard content is redacted in payload files regardless of
	// this flag — see PayloadScrubBytes.
	FullPayloads bool

	mu      sync.Mutex
	rotated time.Time // ymd of the last rotation sweep
}

// DefaultDir returns the audit directory MC writes to by default.
// Precedence: $XDG_STATE_HOME/mycomputer/audit > ~/.local/state/mycomputer/audit
// > /tmp/mycomputer-audit as a last resort.
func DefaultDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "mycomputer", "audit")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "mycomputer", "audit")
	}
	return filepath.Join(os.TempDir(), "mycomputer-audit")
}

// New returns a Writer rooted at DefaultDir() with default retention.
func New() *Writer {
	return &Writer{Dir: DefaultDir(), RetentionDays: DefaultRetentionDays}
}

// resolvedDir returns the directory the writer will append into, with
// defaults applied.
func (w *Writer) resolvedDir() string {
	if w.Dir == "" {
		return DefaultDir()
	}
	return w.Dir
}

func (w *Writer) retention() int {
	if w.RetentionDays <= 0 {
		return DefaultRetentionDays
	}
	return w.RetentionDays
}

// FileFor returns the JSONL path used for records on day d (UTC).
func (w *Writer) FileFor(d time.Time) string {
	return filepath.Join(w.resolvedDir(), d.UTC().Format("2006-01-02")+".jsonl")
}

// PayloadsDir returns the directory where per-batch payload manifests
// live: <Dir>/payloads/. Callers should treat absence as "no payload
// was written for this batch" and fall back to type-only behavior.
func (w *Writer) PayloadsDir() string {
	return filepath.Join(w.resolvedDir(), "payloads")
}

// PayloadFileFor returns the per-batch payload manifest path for the
// given batch id: <Dir>/payloads/<batch_id>.json. The path is
// deterministic; callers can probe os.Stat to decide whether full
// replay is available.
func (w *Writer) PayloadFileFor(batchID string) string {
	return filepath.Join(w.PayloadsDir(), batchID+".json")
}

// Append writes one record and rotates expired files. Best-effort:
// returns the underlying error but callers should continue regardless.
func (w *Writer) Append(rec Record) error {
	if rec.TS.IsZero() {
		rec.TS = time.Now().UTC()
	}
	// Final-pass redaction: defense in depth. The Record schema does
	// not have a "Content" string anywhere — but if a careless caller
	// stuffed clipboard content into Target or ResolvedCoords or
	// Error we don't want the audit log to leak. The clipboard
	// summary is the only allowed place to mention clipboard data,
	// and it carries bytes/mime only.
	rec = scrubRecord(rec)

	w.mu.Lock()
	defer w.mu.Unlock()

	dir := w.resolvedDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("audit: mkdir %s: %w", dir, err)
	}

	path := w.FileFor(rec.TS)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: write %s: %w", path, err)
	}

	// Rotation: only sweep once per day per writer so we don't
	// readdir on every action. Don't touch the live file mid-day.
	ymd := rec.TS.UTC().Format("2006-01-02")
	if w.rotated.UTC().Format("2006-01-02") != ymd {
		w.rotateLocked(dir, rec.TS)
		w.rotated = rec.TS
	}
	return nil
}

func (w *Writer) rotateLocked(dir string, now time.Time) {
	cutoff := now.UTC().Add(-time.Duration(w.retention()) * 24 * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		stamp := strings.TrimSuffix(name, ".jsonl")
		t, err := time.Parse("2006-01-02", stamp)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	// Payload-manifest retention: payload files are named by batch_id
	// (no embedded date), so we cull by file mtime against the same
	// retention cutoff. Best-effort; failures are silent.
	payloadsDir := filepath.Join(dir, "payloads")
	pents, err := os.ReadDir(payloadsDir)
	if err != nil {
		return
	}
	for _, e := range pents {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(payloadsDir, e.Name()))
		}
	}
}

// WriteBatchPayload persists a per-batch payload manifest when
// FullPayloads is enabled. raw is JSON describing the original batch
// inputs; it is passed through PayloadScrubBytes before being written
// so clipboard content is redacted into the {content_redacted,bytes,
// mime} envelope. The file is created with mode 0600 inside a 0700
// directory so the manifest is only readable by the owning user.
//
// When FullPayloads is false this is a no-op (returns nil) and no
// directory is created — the default audit shape is unchanged.
func (w *Writer) WriteBatchPayload(batchID string, raw []byte) error {
	if w == nil || !w.FullPayloads {
		return nil
	}
	if batchID == "" {
		return errors.New("audit: WriteBatchPayload requires batch_id")
	}
	scrubbed, err := PayloadScrubBytes(raw)
	if err != nil {
		return fmt.Errorf("audit: scrub payload: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := w.PayloadsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("audit: mkdir %s: %w", dir, err)
	}
	// Enforce 0700 even when MkdirAll found a pre-existing directory
	// with looser permissions (e.g. from an earlier test run).
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, batchID+".json")
	// Owner-only read/write. We open with O_TRUNC so a re-run with the
	// same batch_id overwrites instead of appending stale bytes.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open payload %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	// Belt-and-suspenders: chmod after open in case the umask widened
	// the file mode on a pre-existing entry.
	_ = os.Chmod(path, 0o600)
	if _, err := f.Write(scrubbed); err != nil {
		return fmt.Errorf("audit: write payload %s: %w", path, err)
	}
	return nil
}

// ReadBatchPayload reads back a payload manifest written by
// WriteBatchPayload. Returns the scrubbed JSON bytes, or os.ErrNotExist
// when the file does not exist (the caller should fall back to v0.2
// type-only replay).
func (w *Writer) ReadBatchPayload(batchID string) ([]byte, error) {
	if batchID == "" {
		return nil, errors.New("audit: ReadBatchPayload requires batch_id")
	}
	return os.ReadFile(w.PayloadFileFor(batchID))
}

// PayloadScrubBytes enforces the clipboard-content redaction invariant
// on a marshaled ActionBatch (or any JSON object containing actions[]).
// Every action with type=="clipboard_write" has its "content" field
// replaced with {"content_redacted":true,"bytes":N,"mime":M}. The
// original byte count and MIME are preserved so debugging tools can
// see "what shape went out" without ever recovering the content.
//
// The function is JSON-shape agnostic on the rest of the payload: it
// only inspects top-level "actions" (an array). Non-clipboard actions
// pass through untouched.
func PayloadScrubBytes(raw []byte) ([]byte, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	if actions, ok := top["actions"].([]any); ok {
		for i := range actions {
			step, ok := actions[i].(map[string]any)
			if !ok {
				continue
			}
			scrubPayloadAction(step)
			actions[i] = step
		}
		top["actions"] = actions
	}
	// Top-level clipboard_write CLI payloads (no "actions" wrapper) are
	// scrubbed in place as well.
	if _, ok := top["type"].(string); ok {
		scrubPayloadAction(top)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scrubPayloadAction redacts clipboard content on a decoded action map.
// Idempotent: safe to call on already-scrubbed records.
func scrubPayloadAction(step map[string]any) {
	t, _ := step["type"].(string)
	if t != "clipboard_write" {
		// Defensive: a careless caller could stash clipboard content
		// under "content" on a non-clipboard action. Drop it.
		delete(step, "content")
		return
	}
	content, _ := step["content"].(string)
	mime, _ := step["mime"].(string)
	delete(step, "content")
	step["content_redacted"] = true
	step["bytes"] = len(content)
	if mime != "" {
		step["mime"] = mime
	}
}

// Probe returns audit-writer health metadata for doctor.
type ProbeResult struct {
	Dir           string
	Writable      bool
	RetentionDays int
	TodayBytes    int64
	Error         string
}

// Probe attempts to mkdir and stat the audit directory and the
// current day's file. Never returns an error — surfaces failure via
// ProbeResult.Error.
func (w *Writer) Probe(now time.Time) ProbeResult {
	dir := w.resolvedDir()
	res := ProbeResult{Dir: dir, RetentionDays: w.retention()}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Writable = true
	// today's bytes
	path := w.FileFor(now)
	if fi, err := os.Stat(path); err == nil {
		res.TodayBytes = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		res.Error = err.Error()
	}
	return res
}

// scrubRecord enforces clipboard-content redaction. It is paranoid
// by design: we only allow Record.Clipboard to mention clipboard
// data, and only via Bytes+Mime+Selection.
func scrubRecord(rec Record) Record {
	if rec.Clipboard != nil {
		rec.Clipboard = &ClipboardSummary{
			Selection: rec.Clipboard.Selection,
			Mime:      rec.Clipboard.Mime,
			Bytes:     rec.Clipboard.Bytes,
		}
	}
	// Strip any "content" key that may have been smuggled into
	// Target or yield/screenshot summaries. Cheap, defensive.
	if rec.Target != nil {
		delete(rec.Target, "content")
	}
	return rec
}
