package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendWritesRecordShape(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir, RetentionDays: 14}
	rec := Record{
		BatchID:        "batch-1",
		ActionIndex:    0,
		Type:           "click",
		ResolvedCoords: &Point{X: 100, Y: 200},
		Backend:        "XTest",
		OK:             true,
		ElapsedMS:      12,
	}
	if err := w.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := w.FileFor(time.Now())
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var got Record
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "click" || !got.OK || got.Backend != "XTest" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.ResolvedCoords == nil || got.ResolvedCoords.X != 100 {
		t.Fatalf("resolved coords missing")
	}
	if got.TS.IsZero() {
		t.Fatalf("ts must be set automatically")
	}
}

func TestAppendRedactsClipboardContent(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir}
	// A malicious caller stuffs clipboard content into multiple
	// fields. None of them must survive into the on-disk record.
	probe := "AUDIT-PROBE-XYZ-DO-NOT-LOG"
	rec := Record{
		Type:        "clipboard_write",
		ActionIndex: 0,
		OK:          true,
		Clipboard: &ClipboardSummary{
			Selection: "clipboard",
			Mime:      "text/plain",
			Bytes:     len(probe),
		},
		Target: map[string]any{
			"content": probe, // must be scrubbed
			"safe":    "ok",
		},
	}
	if err := w.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	b, err := os.ReadFile(w.FileFor(time.Now()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(b), probe) {
		t.Fatalf("clipboard content leaked into audit log:\n%s", b)
	}
	// Bytes count must still be recorded.
	if !strings.Contains(string(b), `"bytes":26`) {
		t.Fatalf("expected bytes count, got: %s", b)
	}
}

func TestRotationRemovesExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	// Create files for 30, 20, and 1 day ago.
	now := time.Now().UTC()
	old := []string{
		now.Add(-30*24*time.Hour).Format("2006-01-02") + ".jsonl",
		now.Add(-20*24*time.Hour).Format("2006-01-02") + ".jsonl",
		now.Add(-1*24*time.Hour).Format("2006-01-02") + ".jsonl",
	}
	for _, name := range old {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"old":true}`+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	w := &Writer{Dir: dir, RetentionDays: 14}
	// Trigger a rotation sweep via a normal Append today.
	if err := w.Append(Record{Type: "noop", OK: true}); err != nil {
		t.Fatalf("append: %v", err)
	}
	for i, name := range old {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		switch i {
		case 0, 1:
			if err == nil {
				t.Errorf("expected %s to be rotated out", name)
			}
		case 2:
			if err != nil {
				t.Errorf("expected %s to remain (within retention): %v", name, err)
			}
		}
	}
}

func TestProbeReportsDirectoryHealth(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir}
	res := w.Probe(time.Now())
	if !res.Writable {
		t.Fatalf("expected writable, got %+v", res)
	}
	if res.RetentionDays != DefaultRetentionDays {
		t.Fatalf("default retention not applied: %d", res.RetentionDays)
	}

	// Append one record and verify TodayBytes grows.
	if err := w.Append(Record{Type: "noop", OK: true}); err != nil {
		t.Fatalf("append: %v", err)
	}
	res2 := w.Probe(time.Now())
	if res2.TodayBytes <= 0 {
		t.Fatalf("today_bytes should be > 0 after append, got %d", res2.TodayBytes)
	}
}

// TestFullPayloadsClipboardRedacted enforces task-14's privacy
// invariant: even when --audit-full-payloads is on, clipboard content
// must NEVER appear on disk. We round-trip an ActionBatch containing
// a clipboard_write action through the writer, then grep the entire
// audit tree (JSONL + payload manifest) for the probe string.
func TestFullPayloadsClipboardRedacted(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir, RetentionDays: 14, FullPayloads: true}
	const probe = "SECRET-PAYLOAD-PROBE"
	batchID := "batch-clip-1"
	raw := []byte(`{
		"schema_version": "0.2",
		"batch_id": "` + batchID + `",
		"actions": [
			{"type":"clipboard_write","selection":"clipboard","mime":"text/plain","content":"` + probe + `"},
			{"type":"click","point":{"x":100,"y":200}}
		]
	}`)
	if err := w.WriteBatchPayload(batchID, raw); err != nil {
		t.Fatalf("WriteBatchPayload: %v", err)
	}
	// Also append a JSONL record that mirrors what the pipeline would
	// emit alongside the manifest (with payload_ref set).
	if err := w.Append(Record{
		BatchID:     batchID,
		ActionIndex: 0,
		Type:        "clipboard_write",
		PayloadRef:  batchID,
		OK:          true,
		Clipboard:   &ClipboardSummary{Selection: "clipboard", Mime: "text/plain", Bytes: len(probe)},
		Target:      map[string]any{"content": probe}, // belt-and-suspenders smuggle attempt
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Walk the audit tree. The probe MUST NOT appear in ANY file.
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), probe) {
			t.Fatalf("clipboard content leaked into %s:\n%s", p, b)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Non-clipboard inputs SHOULD survive in the manifest. The
	// payload file must exist with mode 0600 and reference the
	// scrubbed clipboard envelope.
	manifestPath := w.PayloadFileFor(batchID)
	fi, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("payload manifest missing: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("payload manifest must be mode 0600, got %o", perm)
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(body), `"x": 100`) && !strings.Contains(string(body), `"x":100`) {
		t.Fatalf("non-clipboard input dropped from manifest; expected click coord, got:\n%s", body)
	}
	if !strings.Contains(string(body), `"content_redacted"`) {
		t.Fatalf("expected content_redacted sentinel in manifest, got:\n%s", body)
	}
	if !strings.Contains(string(body), `"bytes"`) {
		t.Fatalf("expected bytes count in scrubbed clipboard envelope, got:\n%s", body)
	}

	// PayloadsDir itself must be 0700.
	pdi, err := os.Stat(w.PayloadsDir())
	if err != nil {
		t.Fatalf("payloads dir missing: %v", err)
	}
	if perm := pdi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("payloads dir must be mode 0700, got %o", perm)
	}
}

// TestReplayWithPayload confirms that a payload manifest written by
// WriteBatchPayload can be read back through ReadBatchPayload and
// decoded into the same shape. Absent manifest returns os.ErrNotExist.
func TestReplayWithPayload(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir, FullPayloads: true}
	batchID := "replay-1"
	raw := []byte(`{"schema_version":"0.2","batch_id":"` + batchID + `","actions":[{"type":"click","point":{"x":42,"y":7}}]}`)
	if err := w.WriteBatchPayload(batchID, raw); err != nil {
		t.Fatalf("WriteBatchPayload: %v", err)
	}
	got, err := w.ReadBatchPayload(batchID)
	if err != nil {
		t.Fatalf("ReadBatchPayload: %v", err)
	}
	if !strings.Contains(string(got), `"x": 42`) && !strings.Contains(string(got), `"x":42`) {
		t.Fatalf("payload missing click coord:\n%s", got)
	}
	// Missing batch surfaces os.ErrNotExist so callers can fall back.
	if _, err := w.ReadBatchPayload("nope"); !os.IsNotExist(err) {
		t.Fatalf("expected IsNotExist for missing batch, got %v", err)
	}
}

// TestFullPayloadsDisabledNoFile asserts default behavior: when
// FullPayloads is off, WriteBatchPayload is a no-op and no payload
// file or directory is created.
func TestFullPayloadsDisabledNoFile(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir, FullPayloads: false}
	raw := []byte(`{"schema_version":"0.2","actions":[]}`)
	if err := w.WriteBatchPayload("never", raw); err != nil {
		t.Fatalf("WriteBatchPayload: %v", err)
	}
	if _, err := os.Stat(w.PayloadsDir()); !os.IsNotExist(err) {
		t.Fatalf("payloads dir must NOT be created when FullPayloads=false, got err=%v", err)
	}
}

// TestPayloadRetention extends the retention sweep to payload files:
// payload manifests older than RetentionDays are removed when a normal
// Append triggers the sweep.
func TestPayloadRetention(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir, RetentionDays: 14, FullPayloads: true}
	// Seed an "old" manifest by writing one then back-dating its mtime.
	oldID := "old-batch"
	if err := w.WriteBatchPayload(oldID, []byte(`{"actions":[]}`)); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	oldPath := w.PayloadFileFor(oldID)
	past := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	freshID := "fresh-batch"
	if err := w.WriteBatchPayload(freshID, []byte(`{"actions":[]}`)); err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	// Trigger a rotation sweep via a normal Append.
	if err := w.Append(Record{Type: "noop", OK: true}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected old payload to be rotated out, stat err=%v", err)
	}
	if _, err := os.Stat(w.PayloadFileFor(freshID)); err != nil {
		t.Fatalf("fresh payload disappeared: %v", err)
	}
}

func TestAppendIsScannerFriendlyJSONL(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Dir: dir}
	for i := 0; i < 5; i++ {
		if err := w.Append(Record{Type: "click", ActionIndex: i, OK: true}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	f, err := os.Open(w.FileFor(time.Now()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v", count, err)
		}
		count++
	}
	if count != 5 {
		t.Fatalf("expected 5 lines, got %d", count)
	}
}
