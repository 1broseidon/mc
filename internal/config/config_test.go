package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("max_chars: 10\nscreenshot_dir: /from/config\nbrowser_bin: /bin/config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPrefix+"MAX_CHARS", "20")
	t.Setenv(EnvPrefix+"SCREENSHOT_DIR", "/from/env")

	eff, err := Load(Options{Config: configPath, MaxChars: 30})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if eff.MaxChars != 30 {
		t.Fatalf("MaxChars = %d, want flag value 30", eff.MaxChars)
	}
	if eff.Sources["max_chars"] != "flag:--max-chars" {
		t.Fatalf("max_chars source = %q", eff.Sources["max_chars"])
	}
	if eff.ScreenshotDir != "/from/env" {
		t.Fatalf("ScreenshotDir = %q, want env value", eff.ScreenshotDir)
	}
	if eff.BrowserBin != "/bin/config" {
		t.Fatalf("BrowserBin = %q, want config value", eff.BrowserBin)
	}
}

func TestLoadMissingExplicitConfig(t *testing.T) {
	if _, err := Load(Options{Config: filepath.Join(t.TempDir(), "missing.yaml")}); err == nil {
		t.Fatal("expected missing explicit config to return an error")
	}
}
