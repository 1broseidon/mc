package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/mcpserver"
)

// newConventionsCommand returns the `mycomputer conventions` command
// tree. The single subcommand `emit` regenerates conventions.yaml from
// the live binary surface — cobra command tree, root persistent flags,
// MCP tool catalog, contract exit codes, and contract schema versions.
//
// Static editorial sections (notes, surface_pattern, caller list,
// config_files, env_prefix, json_error_shape) live in the renderer
// below — they don't change per-build, but live in code so the regen
// is fully self-contained and reproducible from a checked-out repo.
//
// The subcommand supports two modes:
//   - default: write the rendered YAML to stdout (or --out <path>).
//   - --check: render the YAML, diff against conventions.yaml at the
//     repo root (or --file <path>), and exit non-zero on drift. Exit
//     code 2 on drift; exit 0 on a clean tree. Drift output is a
//     unified diff on stderr.
func newConventionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conventions",
		Short: "Inspect and regenerate the conventions.yaml surface contract",
		Long: "Conventions subcommands keep conventions.yaml in sync with the live binary surface. " +
			"The 'emit' subcommand walks the cobra command tree, the MCP tool catalog, and the contract " +
			"package constants and renders a fully regenerated conventions.yaml. With --check it diffs " +
			"against the checked-in file and exits 2 on drift so CI can prevent contract drift.",
	}
	cmd.AddCommand(newConventionsEmitCommand())
	return cmd
}

func newConventionsEmitCommand() *cobra.Command {
	var (
		outPath   string
		checkPath string
		check     bool
	)
	c := &cobra.Command{
		Use:   "emit",
		Short: "Render conventions.yaml from the live binary surface",
		Long: "Emit walks the cobra command tree, root persistent flags, MCP tool catalog (from mcpserver.Catalog), " +
			"the doctor backend probe list, and the contract package's exit-code and schema-version constants, " +
			"and renders the result as YAML. With --check, the output is diffed against the on-disk conventions.yaml; " +
			"exit 0 on no drift, exit 2 on drift, with a unified diff on stderr.",
		Example: `  mycomputer conventions emit > conventions.yaml
  mycomputer conventions emit --out conventions.yaml
  mycomputer conventions emit --check`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rendered, err := renderConventionsYAML(cmd)
			if err != nil {
				return err
			}
			if check {
				path := checkPath
				if path == "" {
					path = "conventions.yaml"
				}
				existing, readErr := os.ReadFile(path)
				if readErr != nil {
					return contract.NewError(contract.ExitNotFound, "CONVENTIONS_FILE_MISSING", "conventions.yaml not found: "+readErr.Error(), map[string]any{"path": path})
				}
				if bytes.Equal(existing, rendered) {
					return nil
				}
				diff := unifiedDiff(string(existing), string(rendered), path, "<emit>")
				_, _ = fmt.Fprint(cmd.ErrOrStderr(), diff)
				return contract.NewError(contract.ExitValidation, "CONVENTIONS_DRIFT", "conventions.yaml differs from the live binary surface; run 'mycomputer conventions emit --out conventions.yaml' to regenerate", map[string]any{"path": path})
			}
			if outPath != "" {
				return os.WriteFile(outPath, rendered, 0o644)
			}
			_, err = cmd.OutOrStdout().Write(rendered)
			return err
		},
	}
	c.Flags().StringVar(&outPath, "out", "", "write the rendered YAML to this path instead of stdout")
	c.Flags().BoolVar(&check, "check", false, "compare the rendered YAML against the on-disk conventions.yaml; exit 2 on drift")
	c.Flags().StringVar(&checkPath, "file", "", "path to the conventions.yaml to compare against (default: ./conventions.yaml)")
	return c
}

// renderConventionsYAML produces the deterministic YAML body that
// becomes conventions.yaml. The output ordering is fixed (top-level
// keys in editorial order, child slices sorted) so two clean runs
// yield byte-identical output. cmd.Root() is the cobra root used to
// walk the CLI command tree and global flags.
func renderConventionsYAML(cmd *cobra.Command) ([]byte, error) {
	// Build the MCP tool catalog by instantiating a server. New() is
	// the single source of truth for what tools ship — see the add()
	// calls in internal/mcpserver/server.go. We discard the server
	// itself; we just want Catalog() populated with the live set.
	_ = mcpserver.New(mcpserver.Options{
		Version: versionInfo(),
		Config:  config.Effective{},
	})
	catalog := mcpserver.Catalog()
	var readOnly []string
	var mutating []string
	for _, t := range catalog {
		if t.ReadOnly {
			readOnly = append(readOnly, t.Name)
		} else {
			mutating = append(mutating, t.Name)
		}
	}
	sort.Strings(readOnly)
	sort.Strings(mutating)

	// CLI commands: walk the cobra tree from the root. Skip hidden
	// commands and cobra's auto-generated `help` command (which is
	// never explicitly registered and is implementation noise).
	root := cmd.Root()
	var cliCommands []string
	for _, sub := range root.Commands() {
		if sub.Hidden {
			continue
		}
		if sub.Name() == "help" {
			continue
		}
		cliCommands = append(cliCommands, sub.Name())
	}
	sort.Strings(cliCommands)

	// Global flags: iterate the root persistent flag set. Keys are
	// "--name", values are the flag usage string.
	globalFlags := map[string]string{}
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		globalFlags["--"+f.Name] = f.Usage
	})

	// Backends: enumerated statically from the diagnostic.Doctor
	// probe list (DISPLAY/XAUTHORITY are environment rows but ship in
	// the same backends slice). Importing diagnostic here would
	// require an X11 connection and is overkill; keep the list in
	// sync with internal/diagnostic/diagnostic.go via the snapshot
	// test (cmd/mycomputer/conventions_test.go).
	doctorBackends := []string{
		"DISPLAY",
		"XAUTHORITY",
		"x11",
		"xtest",
		"randr",
		"xfixes",
		"at_spi",
		"browser",
		"ocr_tesseract",
		"template_match",
		"clipboard",
		"ime",
		"xinput2",
		"audit",
	}

	// Exit codes: from contract package constants. Renderer emits
	// these in numeric order with the canonical machine-readable name.
	exitCodes := []struct {
		Code int
		Name string
	}{
		{contract.ExitSuccess, "success"},
		{contract.ExitGeneric, "generic_or_unclassified_error"},
		{contract.ExitValidation, "validation_error"},
		{contract.ExitNotFound, "not_found"},
		{contract.ExitDependency, "dependency_unavailable"},
		{contract.ExitPrecondition, "precondition_or_state_error"},
		{contract.ExitCancelled, "cancelled"},
	}

	schemaVersions := contract.SupportedSchemaVersions()

	return renderYAML(conventionsModel{
		Tool:           "mycomputer",
		Product:        "MyComputer",
		Module:         "github.com/1broseidon/mc",
		Kind:           "cli+mcp",
		Boundary:       "tool-scope",
		Callers:        []string{"agent", "script", "human-operator"},
		SurfacePattern: "verb-surface-cli-with-mcp-first-tools",
		Stdout:         "data/results only",
		Stderr:         "diagnostics/progress/errors",
		ConfigPrecedence: []string{
			"flags",
			"environment",
			"config_file",
			"defaults",
		},
		EnvPrefix: "MYCOMPUTER_",
		ConfigFiles: []string{
			"./mycomputer.yaml",
			"$XDG_CONFIG_HOME/mycomputer/config.yaml",
			"~/.config/mycomputer/config.yaml",
			"/etc/mycomputer/config.yaml",
		},
		GlobalFlags:    globalFlags,
		ExitCodes:      exitCodes,
		SchemaVersions: schemaVersions,
		SchemaVersion:  contract.SchemaVersion,
		CLICommands:    cliCommands,
		MCPServerID:    "my-computer",
		MCPReadOnly:    readOnly,
		MCPMutating:    mutating,
		DoctorBackends: doctorBackends,
		JSONErrorShape: map[string]string{
			"code":    "STABLE_MACHINE_CODE",
			"message": "actionable human-readable message",
			"details": "optional object",
		},
		SchemaGovernance: schemaGovernance{
			Rule:     "Wire-affecting changes during a release cycle require either (a) schema_version point-bump or (b) anvil.md ledger entry. No silent in-place mutation of shipped wire shapes.",
			Ratified: "2026-05-26",
		},
		Notes: []string{
			"No generic exec, shell, filesystem, or terminal command tool.",
			"Native Wayland control is out of MVP scope; doctor reports blockers.",
			"Screenshot responses include coord_map for screenshot-to-screen coordinate conversion.",
			"computer_actions request envelopes must carry schema_version=\"" + contract.SchemaVersion + "\"; v0.1 payloads are accepted for wire compatibility but rejected with VALIDATION_SCHEMA_VERSION_REQUIRED when the field is missing entirely.",
			"--allow-close gates window_close at server start; without it the tool returns PRECONDITION_CLOSE_NOT_ALLOWED.",
			"--logical-coords is experimental HiDPI translation; production agents should stay on physical pixels.",
			"--dry-run resolves and validates mutating actions without touching the desktop; result envelopes carry dry_run:true.",
			"--audit-screenshots captures before/after PNGs for every mutating action and records them in the audit log (expensive).",
			"--verbose is a global flag; per-command semantics opt in by reading rootOpts.Verbose. Currently observed by doctor (probe count + elapsed) and version (build metadata); other commands ignore it. Adding a new --verbose-aware command is a per-command change, not a flag change.",
			"This file is generated by 'mycomputer conventions emit'; CI runs 'mycomputer conventions emit --check' to prevent drift.",
		},
	}), nil
}

// conventionsModel is the intermediate representation that the renderer
// walks. Field order on the struct mirrors the emit order; renderYAML
// emits one key per line in declaration order so the on-disk file has
// stable editorial layout.
type conventionsModel struct {
	Tool             string
	Product          string
	Module           string
	Kind             string
	Boundary         string
	Callers          []string
	SurfacePattern   string
	Stdout           string
	Stderr           string
	ConfigPrecedence []string
	EnvPrefix        string
	ConfigFiles      []string
	GlobalFlags      map[string]string
	ExitCodes        []struct {
		Code int
		Name string
	}
	SchemaVersions   []string
	SchemaVersion    string
	CLICommands      []string
	MCPServerID      string
	MCPReadOnly      []string
	MCPMutating      []string
	DoctorBackends   []string
	JSONErrorShape   map[string]string
	SchemaGovernance schemaGovernance
	Notes            []string
}

// schemaGovernance is the editorial section ratified by task-20.
// It is intentionally static (not derived from the live binary
// surface) because the rule applies to the contract itself, not to
// any in-code symbol the renderer could walk. Treat it as a frozen
// editorial block: any change is a contract change and must go
// through the same ledger discipline it codifies.
type schemaGovernance struct {
	Rule     string
	Ratified string
}

// renderYAML emits the model as YAML in editorial order. Output is
// deterministic: maps are emitted in a fixed key order (declared
// below), slices in the order present on the model. Two runs against
// the same binary produce byte-identical output.
func renderYAML(m conventionsModel) []byte {
	var b bytes.Buffer
	writeScalar := func(key, value string) {
		fmt.Fprintf(&b, "%s: %s\n", key, value)
	}
	writeList := func(key string, items []string) {
		fmt.Fprintf(&b, "%s:\n", key)
		for _, item := range items {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	writeQuotedList := func(key string, items []string) {
		fmt.Fprintf(&b, "%s:\n", key)
		for _, item := range items {
			fmt.Fprintf(&b, "  - %q\n", item)
		}
	}

	writeScalar("tool", m.Tool)
	writeScalar("product", m.Product)
	writeScalar("module", m.Module)
	writeScalar("kind", m.Kind)
	writeScalar("boundary", m.Boundary)
	writeList("callers", m.Callers)
	writeScalar("surface_pattern", m.SurfacePattern)
	writeScalar("stdout", m.Stdout)
	writeScalar("stderr", m.Stderr)
	writeList("config_precedence", m.ConfigPrecedence)
	writeScalar("env_prefix", m.EnvPrefix)
	writeList("config_files", m.ConfigFiles)

	// global_flags: emit keys sorted alphabetically for deterministic
	// output. Quote values to keep colons/parens inside descriptions
	// from re-parsing as nested YAML structure.
	fmt.Fprintln(&b, "global_flags:")
	flagKeys := make([]string, 0, len(m.GlobalFlags))
	for k := range m.GlobalFlags {
		flagKeys = append(flagKeys, k)
	}
	sort.Strings(flagKeys)
	for _, k := range flagKeys {
		fmt.Fprintf(&b, "  %s: %s\n", k, yamlString(m.GlobalFlags[k]))
	}

	fmt.Fprintln(&b, "exit_codes:")
	for _, ec := range m.ExitCodes {
		fmt.Fprintf(&b, "  %d: %s\n", ec.Code, ec.Name)
	}

	writeQuotedList("schema_versions", m.SchemaVersions)
	writeScalar("schema_version", strconvQuote(m.SchemaVersion))
	writeList("cli_commands", m.CLICommands)
	writeScalar("mcp_server_id", m.MCPServerID)

	fmt.Fprintln(&b, "mcp_tools:")
	fmt.Fprintln(&b, "  read_only:")
	for _, name := range m.MCPReadOnly {
		fmt.Fprintf(&b, "    - %s\n", name)
	}
	fmt.Fprintln(&b, "  mutating:")
	for _, name := range m.MCPMutating {
		fmt.Fprintf(&b, "    - %s\n", name)
	}

	writeList("doctor_backends", m.DoctorBackends)

	fmt.Fprintln(&b, "json_error_shape:")
	fmt.Fprintln(&b, "  error:")
	// Fixed key order: code, message, details.
	fmt.Fprintf(&b, "    code: %s\n", m.JSONErrorShape["code"])
	fmt.Fprintf(&b, "    message: %s\n", m.JSONErrorShape["message"])
	fmt.Fprintf(&b, "    details: %s\n", m.JSONErrorShape["details"])

	// schema_governance: static editorial block ratified by task-20.
	// Emitted as a top-level mapping with a quoted multi-line-safe
	// `rule` value and an ISO date. Anchored before `notes:` so its
	// surface presence is harder to miss when scanning the file.
	fmt.Fprintln(&b, "schema_governance:")
	fmt.Fprintf(&b, "  rule: %s\n", yamlString(m.SchemaGovernance.Rule))
	fmt.Fprintf(&b, "  ratified: %s\n", yamlString(m.SchemaGovernance.Ratified))

	fmt.Fprintln(&b, "notes:")
	for _, n := range m.Notes {
		fmt.Fprintf(&b, "  - %s\n", yamlString(n))
	}

	return b.Bytes()
}

// yamlString returns a YAML-safe scalar rendering for the supplied
// string. Values that contain a colon followed by space, or that start
// with a YAML indicator character, are emitted as double-quoted scalars
// so downstream parsers don't see them as nested mappings.
func yamlString(s string) string {
	if needsQuoting(s) {
		// %q gives Go-style escaping (\" \n etc); YAML accepts those.
		return fmt.Sprintf("%q", s)
	}
	return s
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, "\n\t") {
		return true
	}
	if strings.Contains(s, ": ") {
		return true
	}
	// YAML reserved indicator characters at start.
	switch s[0] {
	case '!', '&', '*', '|', '>', '%', '@', '`', '"', '\'', '#', '{', '}', '[', ']', ',':
		return true
	}
	return false
}

// strconvQuote double-quotes a value so it serializes as a YAML string
// scalar rather than a number-looking value (e.g. schema_version "0.2"
// should not parse as float 0.2 when other tools consume the file).
func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

// unifiedDiff renders a minimal unified diff between old and new line
// content. It is not a full git-style diff; the goal is a human-
// readable hint when --check trips so the operator sees the drifted
// lines without having to run a separate diff tool. Both inputs are
// expected to be full file bodies (newline-terminated lines).
func unifiedDiff(oldText, newText, oldName, newName string) string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", oldName)
	fmt.Fprintf(&b, "+++ %s\n", newName)
	// Walk both line lists in parallel, emitting context, additions,
	// and removals. This is a line-by-line diff (not LCS-based) — good
	// enough for the small drift footprint conventions.yaml is
	// expected to have between releases.
	max := len(oldLines)
	if len(newLines) > max {
		max = len(newLines)
	}
	for i := 0; i < max; i++ {
		var ol, nl string
		if i < len(oldLines) {
			ol = oldLines[i]
		}
		if i < len(newLines) {
			nl = newLines[i]
		}
		switch {
		case i >= len(oldLines):
			fmt.Fprintf(&b, "+%s\n", nl)
		case i >= len(newLines):
			fmt.Fprintf(&b, "-%s\n", ol)
		case ol != nl:
			fmt.Fprintf(&b, "-%s\n", ol)
			fmt.Fprintf(&b, "+%s\n", nl)
		}
	}
	return b.String()
}
