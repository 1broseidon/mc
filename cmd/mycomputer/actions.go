package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/pipeline"
)

func newActionsCommand() *cobra.Command {
	var inputFile string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "actions",
		Short: "Execute a validated JSON action batch",
		Example: `  mycomputer actions --input-file actions.json --json
  cat actions.json | mycomputer actions --input-file - --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if inputFile == "" {
				return contract.Validation("INPUT_FILE_REQUIRED", "actions requires --input-file", nil)
			}
			// Resolve config once so --audit-full-payloads (and other
			// persistent flags routed through config precedence) is
			// applied to the pipeline's audit writer before Run.
			if _, err := effectiveConfig(); err != nil {
				return err
			}
			var batch pipeline.ActionBatch
			if err := readJSONFile(inputFile, &batch); err != nil {
				return err
			}
			// Bridge CLI --allow-close into the batch so the pipeline's
			// window_close gate honors the advertised "CLI flag OR
			// per-batch field" semantics.
			if rootOpts.AllowClose {
				batch.AllowClose = true
			}
			// Bridge --dry-run from the CLI into the batch. Either
			// the per-batch DryRun field OR the flag is sufficient.
			if rootOpts.DryRun {
				batch.DryRun = true
			}
			// Bridge --respect-user. Default on interactive sessions
			// (TTY on stdin OR stdout); off under non-interactive
			// (MCP server / piped). The actions CLI inherits
			// rootOpts.RespectUser which is true by default, so this
			// is just the CLI surface for the resolved value.
			if rootOpts.RespectUser && isInteractive() {
				batch.RespectUser = true
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel func()
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			out, err := pipeline.Run(ctx, batch)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, result := range out.Results {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%t\t%s\n", result.Action, result.OK, result.Backend)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&inputFile, "input-file", "", "read JSON action batch from path; - means stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "cancel the action batch after this duration")
	return cmd
}

func readJSONFile(path string, out any) error {
	var (
		b   []byte
		err error
	)
	if path == "-" {
		b, err = os.ReadFile("/dev/stdin")
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return contract.Dependency("INPUT_READ_FAILED", "failed to read input file", map[string]any{"path": path, "error": err.Error()})
	}
	if err := json.Unmarshal(b, out); err != nil {
		return contract.Validation("INPUT_INVALID_JSON", "input file is not valid JSON", map[string]any{"path": path, "error": err.Error()})
	}
	return nil
}
