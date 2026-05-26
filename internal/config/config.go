package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/1broseidon/mc/internal/contract"
)

const EnvPrefix = "MYCOMPUTER_"

type Options struct {
	JSON     bool
	Minimal  bool
	MaxChars int
	NoColor  bool
	Config   string
	Quiet    bool
	Verbose  bool
	// RespectUser holds the resolved --respect-user value when the
	// flag is set explicitly. RespectUserSet distinguishes "explicit
	// false" from "unset" so default-true behavior works correctly.
	// Behavioral wiring lands in task-6.
	RespectUser    bool
	RespectUserSet bool
	// AllowClose holds the resolved --allow-close value. Defaults
	// false. When true, window_close actions are permitted for this
	// process. Surfaces as doctor.session.allow_close.
	AllowClose    bool
	AllowCloseSet bool
	// LogicalCoords toggles the experimental opt-in HiDPI translation
	// layer. When set, screenshot dimensions are divided by the
	// primary monitor's scale before encoding and input coordinates
	// from clients are multiplied by that same scale before XTest.
	// Off by default — production agents should keep MyComputer in
	// physical-pixel mode.
	LogicalCoords    bool
	LogicalCoordsSet bool
	// DryRun, when set, propagates into pipeline.ActionBatch.DryRun
	// so every action is validated/resolved but mutating ops are
	// skipped. Off by default.
	DryRun    bool
	DryRunSet bool
	// AuditScreenshots, when set, causes the audit writer to record
	// screenshot paths before/after each mutating action. Expensive;
	// off by default.
	AuditScreenshots    bool
	AuditScreenshotsSet bool
	// AuditFullPayloads, when set, enables the opt-in per-batch
	// payload manifest under <audit_dir>/payloads/<batch_id>.json.
	// Clipboard content is STILL redacted in payload files — the
	// opt-in only un-redacts non-clipboard inputs so `mycomputer
	// audit replay <id>` can reconstruct the full action batch.
	AuditFullPayloads    bool
	AuditFullPayloadsSet bool
}

type File struct {
	MaxChars          *int   `yaml:"max_chars"`
	ScreenshotDir     string `yaml:"screenshot_dir"`
	BrowserBin        string `yaml:"browser_bin"`
	BrowserEndpoint   string `yaml:"browser_endpoint"`
	RespectUser       *bool  `yaml:"respect_user"`
	AllowClose        *bool  `yaml:"allow_close"`
	LogicalCoords     *bool  `yaml:"logical_coords"`
	DryRun            *bool  `yaml:"dry_run"`
	AuditScreenshots  *bool  `yaml:"audit_screenshots"`
	AuditFullPayloads *bool  `yaml:"audit_full_payloads"`
}

type Effective struct {
	Options
	ScreenshotDir   string
	BrowserBin      string
	BrowserEndpoint string
	LoadedConfig    string
	ConfigFiles     []string
	Sources         map[string]string
	// RespectUser is the resolved value after applying precedence:
	// flag > env MYCOMPUTER_RESPECT_USER > config > default (true).
	// Surfaced in doctor.session.respect_user. Behavioral wiring lands
	// in task-6 — this is declaration.
	RespectUser bool
	// AllowClose is the resolved value after applying precedence:
	// flag > env MYCOMPUTER_ALLOW_CLOSE > config > default (false).
	// Surfaced in doctor.session.allow_close.
	AllowClose bool
	// LogicalCoords is the resolved value after applying precedence:
	// flag > env MYCOMPUTER_LOGICAL_COORDS > config > default (false).
	// Experimental opt-in HiDPI translation layer. See
	// Options.LogicalCoords for behavior.
	LogicalCoords bool
	// DryRun is the resolved value after applying precedence:
	// flag > env MYCOMPUTER_DRY_RUN > config > default (false).
	DryRun bool
	// AuditScreenshots is the resolved value after applying
	// precedence: flag > env MYCOMPUTER_AUDIT_SCREENSHOTS > config >
	// default (false).
	AuditScreenshots bool
	// AuditFullPayloads is the resolved value after applying
	// precedence: flag > env MYCOMPUTER_AUDIT_FULL_PAYLOADS > config
	// > default (false). When true the audit writer persists a
	// per-batch payload manifest with clipboard content scrubbed.
	AuditFullPayloads bool
}

func DefaultSearchPaths() []string {
	paths := []string{"./mycomputer.yaml"}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "mycomputer", "config.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "mycomputer", "config.yaml"))
	}
	paths = append(paths, "/etc/mycomputer/config.yaml")
	return paths
}

func Load(opts Options) (Effective, error) {
	eff := Effective{
		Options:       opts,
		ScreenshotDir: os.TempDir(),
		ConfigFiles:   DefaultSearchPaths(),
		Sources:       map[string]string{},
	}
	if eff.MaxChars == 0 {
		eff.MaxChars = 12000
		eff.Sources["max_chars"] = "default"
	}
	eff.Sources["screenshot_dir"] = "default"
	eff.Sources["browser_bin"] = "default"
	eff.Sources["browser_endpoint"] = "default"
	// respect_user defaults to true; precedence overrides apply below.
	eff.RespectUser = true
	eff.Sources["respect_user"] = "default"
	// allow_close defaults to false; explicit opt-in required.
	eff.AllowClose = false
	eff.Sources["allow_close"] = "default"
	// logical_coords defaults to false (experimental opt-in).
	eff.LogicalCoords = false
	eff.Sources["logical_coords"] = "default"
	// dry_run defaults to false.
	eff.DryRun = false
	eff.Sources["dry_run"] = "default"
	// audit_screenshots defaults to false (expensive).
	eff.AuditScreenshots = false
	eff.Sources["audit_screenshots"] = "default"
	// audit_full_payloads defaults to false (privacy-safe).
	eff.AuditFullPayloads = false
	eff.Sources["audit_full_payloads"] = "default"

	cfg, loaded, err := loadFile(opts.Config, eff.ConfigFiles)
	if err != nil {
		return eff, err
	}
	eff.LoadedConfig = loaded
	if cfg.MaxChars != nil && opts.MaxChars == 0 {
		eff.MaxChars = *cfg.MaxChars
		eff.Sources["max_chars"] = "config"
	}
	if cfg.ScreenshotDir != "" {
		eff.ScreenshotDir = cfg.ScreenshotDir
		eff.Sources["screenshot_dir"] = "config"
	}
	if cfg.BrowserBin != "" {
		eff.BrowserBin = cfg.BrowserBin
		eff.Sources["browser_bin"] = "config"
	}
	if cfg.BrowserEndpoint != "" {
		eff.BrowserEndpoint = cfg.BrowserEndpoint
		eff.Sources["browser_endpoint"] = "config"
	}
	if cfg.RespectUser != nil {
		eff.RespectUser = *cfg.RespectUser
		eff.Sources["respect_user"] = "config"
	}
	if cfg.AllowClose != nil {
		eff.AllowClose = *cfg.AllowClose
		eff.Sources["allow_close"] = "config"
	}
	if cfg.LogicalCoords != nil {
		eff.LogicalCoords = *cfg.LogicalCoords
		eff.Sources["logical_coords"] = "config"
	}
	if cfg.DryRun != nil {
		eff.DryRun = *cfg.DryRun
		eff.Sources["dry_run"] = "config"
	}
	if cfg.AuditScreenshots != nil {
		eff.AuditScreenshots = *cfg.AuditScreenshots
		eff.Sources["audit_screenshots"] = "config"
	}
	if cfg.AuditFullPayloads != nil {
		eff.AuditFullPayloads = *cfg.AuditFullPayloads
		eff.Sources["audit_full_payloads"] = "config"
	}

	applyEnv(&eff)
	applyFlags(&eff, opts)
	return eff, nil
}

func loadFile(explicit string, search []string) (File, string, error) {
	var paths []string
	if explicit != "" {
		paths = []string{explicit}
	} else {
		paths = search
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && explicit == "" {
				continue
			}
			if errors.Is(err, os.ErrNotExist) {
				return File{}, "", contract.NotFound("CONFIG_NOT_FOUND", "config file does not exist", map[string]any{"path": path})
			}
			return File{}, "", contract.Dependency("CONFIG_READ_FAILED", "failed to read config file", map[string]any{"path": path, "error": err.Error()})
		}
		var cfg File
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return File{}, "", contract.Validation("CONFIG_INVALID", "config file is not valid YAML", map[string]any{"path": path, "error": err.Error()})
		}
		return cfg, path, nil
	}
	return File{}, "", nil
}

func applyEnv(eff *Effective) {
	if value := os.Getenv(EnvPrefix + "MAX_CHARS"); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			eff.MaxChars = n
			eff.Sources["max_chars"] = "env:" + EnvPrefix + "MAX_CHARS"
		}
	}
	if value := os.Getenv(EnvPrefix + "SCREENSHOT_DIR"); value != "" {
		eff.ScreenshotDir = value
		eff.Sources["screenshot_dir"] = "env:" + EnvPrefix + "SCREENSHOT_DIR"
	}
	if value := os.Getenv(EnvPrefix + "BROWSER_BIN"); value != "" {
		eff.BrowserBin = value
		eff.Sources["browser_bin"] = "env:" + EnvPrefix + "BROWSER_BIN"
	}
	if value := os.Getenv(EnvPrefix + "BROWSER_ENDPOINT"); value != "" {
		eff.BrowserEndpoint = value
		eff.Sources["browser_endpoint"] = "env:" + EnvPrefix + "BROWSER_ENDPOINT"
	}
	if value := os.Getenv(EnvPrefix + "RESPECT_USER"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.RespectUser = b
			eff.Sources["respect_user"] = "env:" + EnvPrefix + "RESPECT_USER"
		}
	}
	if value := os.Getenv(EnvPrefix + "ALLOW_CLOSE"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.AllowClose = b
			eff.Sources["allow_close"] = "env:" + EnvPrefix + "ALLOW_CLOSE"
		}
	}
	if value := os.Getenv(EnvPrefix + "LOGICAL_COORDS"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.LogicalCoords = b
			eff.Sources["logical_coords"] = "env:" + EnvPrefix + "LOGICAL_COORDS"
		}
	}
	if value := os.Getenv(EnvPrefix + "DRY_RUN"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.DryRun = b
			eff.Sources["dry_run"] = "env:" + EnvPrefix + "DRY_RUN"
		}
	}
	if value := os.Getenv(EnvPrefix + "AUDIT_SCREENSHOTS"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.AuditScreenshots = b
			eff.Sources["audit_screenshots"] = "env:" + EnvPrefix + "AUDIT_SCREENSHOTS"
		}
	}
	if value := os.Getenv(EnvPrefix + "AUDIT_FULL_PAYLOADS"); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			eff.AuditFullPayloads = b
			eff.Sources["audit_full_payloads"] = "env:" + EnvPrefix + "AUDIT_FULL_PAYLOADS"
		}
	}
	if os.Getenv("NO_COLOR") != "" {
		eff.NoColor = true
		eff.Sources["no_color"] = "env:NO_COLOR"
	}
}

func applyFlags(eff *Effective, opts Options) {
	if opts.MaxChars != 0 {
		eff.MaxChars = opts.MaxChars
		eff.Sources["max_chars"] = "flag:--max-chars"
	}
	if opts.NoColor {
		eff.NoColor = true
		eff.Sources["no_color"] = "flag:--no-color"
	}
	if opts.JSON {
		eff.Sources["json"] = "flag:--json"
	}
	if opts.Minimal {
		eff.Sources["minimal"] = "flag:--minimal"
	}
	if opts.Config != "" {
		eff.Sources["config"] = "flag:--config"
	}
	if opts.RespectUserSet {
		eff.RespectUser = opts.RespectUser
		eff.Sources["respect_user"] = "flag:--respect-user"
	}
	if opts.AllowCloseSet {
		eff.AllowClose = opts.AllowClose
		eff.Sources["allow_close"] = "flag:--allow-close"
	}
	if opts.LogicalCoordsSet {
		eff.LogicalCoords = opts.LogicalCoords
		eff.Sources["logical_coords"] = "flag:--logical-coords"
	}
	if opts.DryRunSet {
		eff.DryRun = opts.DryRun
		eff.Sources["dry_run"] = "flag:--dry-run"
	}
	if opts.AuditScreenshotsSet {
		eff.AuditScreenshots = opts.AuditScreenshots
		eff.Sources["audit_screenshots"] = "flag:--audit-screenshots"
	}
	if opts.AuditFullPayloadsSet {
		eff.AuditFullPayloads = opts.AuditFullPayloads
		eff.Sources["audit_full_payloads"] = "flag:--audit-full-payloads"
	}
}

func (eff Effective) Report() contract.ConfigReport {
	values := map[string]contract.Value{
		"json":                {Value: eff.JSON, Source: source(eff.Sources, "json", "flag/default")},
		"minimal":             {Value: eff.Minimal, Source: source(eff.Sources, "minimal", "flag/default")},
		"max_chars":           {Value: eff.MaxChars, Source: source(eff.Sources, "max_chars", "default")},
		"no_color":            {Value: eff.NoColor, Source: source(eff.Sources, "no_color", "default")},
		"quiet":               {Value: eff.Quiet, Source: "flag/default"},
		"verbose":             {Value: eff.Verbose, Source: "flag/default"},
		"screenshot_dir":      {Value: eff.ScreenshotDir, Source: source(eff.Sources, "screenshot_dir", "default")},
		"browser_bin":         {Value: eff.BrowserBin, Source: source(eff.Sources, "browser_bin", "default")},
		"browser_endpoint":    {Value: eff.BrowserEndpoint, Source: source(eff.Sources, "browser_endpoint", "default")},
		"respect_user":        {Value: eff.RespectUser, Source: source(eff.Sources, "respect_user", "default")},
		"allow_close":         {Value: eff.AllowClose, Source: source(eff.Sources, "allow_close", "default")},
		"logical_coords":      {Value: eff.LogicalCoords, Source: source(eff.Sources, "logical_coords", "default")},
		"dry_run":             {Value: eff.DryRun, Source: source(eff.Sources, "dry_run", "default")},
		"audit_screenshots":   {Value: eff.AuditScreenshots, Source: source(eff.Sources, "audit_screenshots", "default")},
		"audit_full_payloads": {Value: eff.AuditFullPayloads, Source: source(eff.Sources, "audit_full_payloads", "default")},
	}
	return contract.ConfigReport{
		Product:      "MyComputer",
		ConfigFiles:  eff.ConfigFiles,
		LoadedConfig: eff.LoadedConfig,
		Values:       values,
		AvailableBackends: map[string]string{
			"x11":     "native X11 protocol via xgb",
			"xtest":   "XTest synthetic input",
			"randr":   "XRandR monitor geometry",
			"at_spi":  "AT-SPI over D-Bus when available",
			"browser": "Chrome DevTools Protocol",
		},
	}
}

func source(sources map[string]string, key, fallback string) string {
	if value := sources[key]; value != "" {
		return value
	}
	return fallback
}
