package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

// TestConventionsEmitMatchesCheckedInFile is the snapshot test: it
// renders the conventions YAML in-process and compares against the
// repo's conventions.yaml. Any drift (new CLI command, new MCP tool,
// new global flag, contract constant change) fails this test and the
// fix is to re-run `mycomputer conventions emit --out conventions.yaml`.
//
// The test reads conventions.yaml at the repo root (cmd/mycomputer is
// two levels deep, so ../../conventions.yaml).
func TestConventionsEmitMatchesCheckedInFile(t *testing.T) {
	rendered, err := emitForTest(t)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	conventionsPath := filepath.Join("..", "..", "conventions.yaml")
	checked, err := os.ReadFile(conventionsPath)
	if err != nil {
		t.Fatalf("read conventions.yaml: %v", err)
	}
	if !bytes.Equal(rendered, checked) {
		t.Fatalf("conventions.yaml is out of sync with the live binary surface; run 'mycomputer conventions emit --out conventions.yaml' to regenerate.\n--- on-disk ---\n%s\n--- rendered ---\n%s", checked, rendered)
	}
}

// TestConventionsCheckDetectsDrift writes a hand-modified fixture to a
// temp directory and asserts that `conventions emit --check --file <path>`
// returns a CONVENTIONS_DRIFT error with exit code 2.
func TestConventionsCheckDetectsDrift(t *testing.T) {
	rendered, err := emitForTest(t)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	// Build a drifted fixture by truncating a line out of the middle.
	lines := strings.Split(string(rendered), "\n")
	if len(lines) < 10 {
		t.Fatalf("rendered output too short to drift-test: %d lines", len(lines))
	}
	// Remove the second-to-last cli_commands entry by deleting one
	// random list item line — any non-trivial change suffices.
	drifted := strings.Join(append(lines[:5], lines[6:]...), "\n")
	tmpFile := filepath.Join(t.TempDir(), "conventions.yaml")
	if err := os.WriteFile(tmpFile, []byte(drifted), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := runCommand(t, "conventions", "emit", "--check", "--file", tmpFile)
	if err == nil {
		t.Fatalf("expected drift detection to error, got success.\n%s", out)
	}
	var appErr *contract.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *contract.AppError, got %T: %v", err, err)
	}
	if appErr.Code != "CONVENTIONS_DRIFT" {
		t.Fatalf("expected code CONVENTIONS_DRIFT, got %q", appErr.Code)
	}
	if appErr.ExitCode != contract.ExitValidation {
		t.Fatalf("expected exit code %d, got %d", contract.ExitValidation, appErr.ExitCode)
	}
	if !strings.Contains(out, "---") || !strings.Contains(out, "+++") {
		t.Fatalf("expected unified diff in output, got: %s", out)
	}
}

// TestConventionsCheckPassesOnCleanTree confirms that --check against
// the real conventions.yaml exits cleanly when no drift exists. This is
// the happy-path mirror of the drift test above.
func TestConventionsCheckPassesOnCleanTree(t *testing.T) {
	conventionsPath, err := filepath.Abs(filepath.Join("..", "..", "conventions.yaml"))
	if err != nil {
		t.Fatalf("resolve conventions.yaml: %v", err)
	}
	_, err = runCommand(t, "conventions", "emit", "--check", "--file", conventionsPath)
	if err != nil {
		t.Fatalf("expected --check to pass on a clean tree, got %v", err)
	}
}

// TestConventionsEmitIncludesSelf is the self-listing constraint from
// the task contract: the emit subcommand must appear in the rendered
// cli_commands list (under its parent `conventions`).
func TestConventionsEmitIncludesSelf(t *testing.T) {
	rendered, err := emitForTest(t)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if !strings.Contains(string(rendered), "\n  - conventions\n") {
		t.Fatalf("rendered yaml missing 'conventions' under cli_commands:\n%s", rendered)
	}
}

// emitForTest invokes `conventions emit` (without --check or --out) via
// the cobra root and returns the captured stdout bytes.
func emitForTest(t *testing.T) ([]byte, error) {
	t.Helper()
	out, err := runCommand(t, "conventions", "emit")
	return []byte(out), err
}
