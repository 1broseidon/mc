package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/1broseidon/mc/internal/config"
)

func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	rootOpts = config.Options{}
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestVersionJSON(t *testing.T) {
	out, err := runCommand(t, "--json", "version")
	if err != nil {
		t.Fatalf("version failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("version output is not JSON: %v\n%s", err, out)
	}
	if got["version"] == "" {
		t.Fatalf("version field missing: %#v", got)
	}
}

func TestHelpHasContractCommands(t *testing.T) {
	out, err := runCommand(t, "--help")
	if err != nil {
		t.Fatalf("help failed: %v", err)
	}
	for _, want := range []string{"serve", "doctor", "config", "observe", "capture", "windows", "focus", "actions", "browse", "completion", "version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "\n  run ") {
		t.Fatalf("help output must not expose root run command:\n%s", out)
	}
}

func TestActionsRequiresInputFile(t *testing.T) {
	_, err := runCommand(t, "actions")
	if err == nil {
		t.Fatal("expected actions without --input-file to fail")
	}
}
