package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/audit"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/pipeline"
)

func newAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect and replay the MyComputer audit log",
		Long: `The audit log is a JSONL trail of every action MyComputer executes,
stored under $XDG_STATE_HOME/mycomputer/audit/YYYY-MM-DD.jsonl (or
~/.local/state/mycomputer/audit/...). Clipboard content is never logged
— only bytes count + MIME.`,
	}
	cmd.AddCommand(newAuditTailCommand())
	cmd.AddCommand(newAuditGrepCommand())
	cmd.AddCommand(newAuditReplayCommand())
	return cmd
}

func auditDir() string { return audit.DefaultDir() }

func newAuditTailCommand() *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Print the last N records from today's audit log (or follow with --follow)",
		Example: `  mycomputer audit tail --lines 20
  mycomputer audit tail --follow`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := filepath.Join(auditDir(), time.Now().UTC().Format("2006-01-02")+".jsonl")
			if !follow {
				return tailFile(cmd.OutOrStdout(), path, lines)
			}
			return followFile(cmd.Context(), cmd.OutOrStdout(), path)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new records as they are appended")
	cmd.Flags().IntVarP(&lines, "lines", "n", 20, "show the last N records (ignored with --follow)")
	return cmd
}

func tailFile(w io.Writer, path string, n int) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return contract.Dependency("AUDIT_OPEN_FAILED", "failed to open audit file", map[string]any{"path": path, "error": err.Error()})
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var ring []string
	for scanner.Scan() {
		ring = append(ring, scanner.Text())
		if len(ring) > n {
			ring = ring[len(ring)-n:]
		}
	}
	for _, line := range ring {
		_, _ = fmt.Fprintln(w, line)
	}
	return nil
}

func followFile(ctx context.Context, w io.Writer, path string) error {
	// Open the file (creating an empty placeholder if missing so we
	// can sit on the seek pointer waiting for first writes).
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if f, ferr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			_ = f.Close()
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return contract.Dependency("AUDIT_OPEN_FAILED", "failed to open audit file", map[string]any{"path": path, "error": err.Error()})
	}
	defer func() { _ = f.Close() }()
	// Start at EOF so we only stream new content.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	reader := bufio.NewReader(f)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			_, _ = fmt.Fprint(w, line)
		}
		if err == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
	}
}

func newAuditGrepCommand() *cobra.Command {
	var query string
	var days int
	cmd := &cobra.Command{
		Use:   "grep",
		Short: "Search the audit log for records matching --query (regex)",
		Example: `  mycomputer audit grep --query 'click_text'
  mycomputer audit grep --query '\"ok\":false' --days 7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if query == "" {
				return contract.Validation("AUDIT_QUERY_REQUIRED", "audit grep requires --query", nil)
			}
			re, err := regexp.Compile(query)
			if err != nil {
				return contract.Validation("AUDIT_QUERY_INVALID", "regex compile failed", map[string]any{"query": query, "error": err.Error()})
			}
			files, err := auditFilesNewestFirst(auditDir())
			if err != nil {
				return err
			}
			cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
			for _, path := range files {
				stamp := strings.TrimSuffix(filepath.Base(path), ".jsonl")
				if t, terr := time.Parse("2006-01-02", stamp); terr == nil {
					if days > 0 && t.Before(cutoff) {
						continue
					}
				}
				if err := grepFile(cmd.OutOrStdout(), path, re); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "regex to match against each record (required)")
	cmd.Flags().IntVar(&days, "days", 14, "only search files within the last N days (0 = all)")
	return cmd
}

func grepFile(w io.Writer, path string, re *regexp.Regexp) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if re.MatchString(line) {
			_, _ = fmt.Fprintln(w, line)
		}
	}
	return nil
}

func auditFilesNewestFirst(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	return paths, nil
}

func newAuditReplayCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "replay <batch_id>",
		Short:   "Replay every action recorded under batch_id in dry-run for inspection",
		Args:    cobra.ExactArgs(1),
		Example: `  mycomputer audit replay 7af3c1029b4f2e60 --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			batchID := args[0]
			// task-14: prefer the per-batch payload manifest written
			// under <audit_dir>/payloads/<batch_id>.json when
			// --audit-full-payloads is on. The manifest carries every
			// non-clipboard input (click coords, selectors, queries,
			// etc.) so the dry-run replay reflects the agent's actual
			// intent. Clipboard content is scrubbed in the manifest;
			// the live replay receives the redacted shape, never the
			// original bytes. Absent manifest → fall back to v0.2
			// type-only reconstruction with a note.
			if payload, perr := pipeline.AuditWriter().ReadBatchPayload(batchID); perr == nil {
				var batch pipeline.ActionBatch
				if jerr := json.Unmarshal(payload, &batch); jerr != nil {
					return contract.Validation("AUDIT_PAYLOAD_INVALID", "stored payload manifest is not a valid ActionBatch", map[string]any{"batch_id": batchID, "error": jerr.Error()})
				}
				if batch.SchemaVersion == "" {
					batch.SchemaVersion = contract.SchemaVersion
				}
				batch.BatchID = batchID + "-replay"
				batch.DryRun = dryRun
				out, err := pipeline.Run(cmd.Context(), batch)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"source": "payload_manifest",
					"note":   "reconstructed from sealed payload (clipboard content redacted in manifest)",
					"batch":  out,
				})
			} else if !os.IsNotExist(perr) {
				return contract.Dependency("AUDIT_PAYLOAD_READ_FAILED", "failed to read payload manifest", map[string]any{"batch_id": batchID, "error": perr.Error()})
			}

			files, err := auditFilesNewestFirst(auditDir())
			if err != nil {
				return err
			}
			var recs []audit.Record
			for _, path := range files {
				rs, err := readRecords(path)
				if err != nil {
					return err
				}
				for _, r := range rs {
					if r.BatchID == batchID {
						recs = append(recs, r)
					}
				}
				if len(recs) > 0 {
					break
				}
			}
			if len(recs) == 0 {
				return contract.NotFound("AUDIT_BATCH_NOT_FOUND", "no audit records match batch_id", map[string]any{"batch_id": batchID})
			}
			sort.Slice(recs, func(i, j int) bool { return recs[i].ActionIndex < recs[j].ActionIndex })

			// Build a minimal ActionBatch from the recorded action
			// types. The agent's exact original payload isn't stored
			// in audit (by design — clipboard content etc. must not
			// be reproducible from the log). Replay therefore only
			// reconstructs action TYPES; full re-execution requires
			// the agent to keep its source-of-truth batch around.
			batch := pipeline.ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				BatchID:       batchID + "-replay",
				DryRun:        dryRun,
			}
			for _, r := range recs {
				batch.Actions = append(batch.Actions, pipeline.Action{Type: r.Type})
			}
			out, err := pipeline.Run(cmd.Context(), batch)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"source": "type_only_fallback",
				"note":   "no sealed payload manifest found for batch_id; reconstructed action TYPES only (run with --audit-full-payloads to capture inputs)",
				"batch":  out,
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "replay in dry-run mode (default true)")
	return cmd
}

func readRecords(path string) ([]audit.Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []audit.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var rec audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
