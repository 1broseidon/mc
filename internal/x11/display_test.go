package x11

import (
	"errors"
	"os"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

func TestMaybeAutoDetectDisplaySetsSingularResult(t *testing.T) {
	t.Setenv("DISPLAY", "")
	res := maybeAutoDetectDisplayWith(func() AutoDetectResult {
		return AutoDetectResult{Display: ":7", Source: "/tmp/.X11-unix/X7"}
	})
	if res.Display != ":7" {
		t.Fatalf("Display = %q, want :7", res.Display)
	}
	if got := os.Getenv("DISPLAY"); got != ":7" {
		t.Fatalf("DISPLAY = %q, want :7", got)
	}
}

func TestMaybeAutoDetectDisplayRespectsExplicitEnv(t *testing.T) {
	t.Setenv("DISPLAY", ":99")
	called := false
	res := maybeAutoDetectDisplayWith(func() AutoDetectResult {
		called = true
		return AutoDetectResult{Display: ":7"}
	})
	if called {
		t.Fatal("probe should not run when DISPLAY is already set")
	}
	if res.Display != "" || res.Source != "" || len(res.Ambiguous) != 0 || res.Empty {
		t.Fatalf("result = %+v, want zero value for explicit DISPLAY", res)
	}
	if got := os.Getenv("DISPLAY"); got != ":99" {
		t.Fatalf("DISPLAY = %q, want :99", got)
	}
}

func TestDisplayResolutionErrorReportsAmbiguousCandidates(t *testing.T) {
	err := displayResolutionError(AutoDetectResult{Ambiguous: []string{":1", ":2"}})
	var app *contract.AppError
	if !errors.As(err, &app) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if app.Code != "DISPLAY_AMBIGUOUS" {
		t.Fatalf("code = %q, want DISPLAY_AMBIGUOUS", app.Code)
	}
	cands, _ := app.Details["candidates"].([]string)
	if len(cands) != 2 || cands[0] != ":1" || cands[1] != ":2" {
		t.Fatalf("candidates = %#v, want [:1 :2]", app.Details["candidates"])
	}
}
