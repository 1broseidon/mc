package window

import (
	"context"
	"os"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

// NOTE: TestClientBoundsCalculation moved to internal/platform/x11adapter
// when the decoration-inset math (clientBounds) migrated into the window
// backend alongside the EWMH property readers.

// TestGeometryMismatch verifies that the post-op divergence detector
// honors the 5-pixel tolerance and only flags axes that were actually
// requested.
func TestGeometryMismatch(t *testing.T) {
	x, y, w, h := 100, 200, 800, 600
	observed := contract.Bounds{X: 100, Y: 200, Width: 800, Height: 600}
	if m := geometryMismatch(observed, &x, &y, &w, &h); m != nil {
		t.Fatalf("exact match should not flag, got %+v", m)
	}
	observedClose := contract.Bounds{X: 102, Y: 198, Width: 803, Height: 597}
	if m := geometryMismatch(observedClose, &x, &y, &w, &h); m != nil {
		t.Fatalf("within tolerance should not flag, got %+v", m)
	}
	observedBig := contract.Bounds{X: 500, Y: 200, Width: 800, Height: 600}
	m := geometryMismatch(observedBig, &x, &y, &w, &h)
	if m == nil {
		t.Fatalf("400px divergence should flag")
	}
	if m["requested_x"] != 100 || m["observed_x"] != 500 {
		t.Fatalf("mismatch details missing x divergence: %+v", m)
	}
	if _, present := m["requested_width"]; present {
		t.Fatalf("width was within tolerance and should not be flagged: %+v", m)
	}
	// Nil-flag arms must be ignored entirely (e.g. window_resize that
	// only sends width/height should not check x/y).
	if m := geometryMismatch(contract.Bounds{X: 9999, Y: 9999, Width: 800, Height: 600}, nil, nil, &w, &h); m != nil {
		t.Fatalf("nil x/y arms must be ignored, got %+v", m)
	}
}

// TestEmptyTargetRejected confirms the entry guard rather than relying
// on X11 round-trips for the empty case.
func TestEmptyTargetRejected(t *testing.T) {
	_, err := resolveOne(context.Background(), Target{})
	if err == nil {
		t.Fatal("expected an error for empty target")
	}
	var app *contract.AppError
	if !errorsAs(err, &app) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if app.Code != "WINDOW_TARGET_REQUIRED" {
		t.Fatalf("code = %q, want WINDOW_TARGET_REQUIRED", app.Code)
	}
}

// TestVerbResultShape ensures VerbResult is JSON-serializable in the
// expected envelope when a warning is attached. Avoids X11 entirely.
func TestVerbResultShape(t *testing.T) {
	res := VerbResult{
		Window: contract.WindowInfo{ID: "0x1", Title: "Sample"},
		Warning: &VerbWarning{
			Code:    contract.WindowGeometryRefusedCode,
			Message: "tiling WM refused move",
			Details: map[string]any{"requested_x": 100, "observed_x": 0, "tolerance_px": geometryTolerance},
		},
	}
	if res.Warning.Code != contract.WindowGeometryRefusedCode {
		t.Fatalf("warning code mismatch")
	}
	if res.Warning.Details["tolerance_px"] != 5 {
		t.Fatalf("expected tolerance_px=5 in warning details")
	}
}

// liveX11 reports whether DISPLAY is set so we can gate live integration
// checks behind a skip on CI runners with no X server.
func liveX11(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY is not set; skipping live X11 test")
	}
}

// TestLiveListPopulatesClientBounds covers the integration of frame
// extents into list_windows when an X11 server is available.
func TestLiveListPopulatesClientBounds(t *testing.T) {
	liveX11(t)
	wins, err := List(context.Background())
	if err != nil {
		t.Skipf("List failed on this environment: %v", err)
	}
	if len(wins) == 0 {
		t.Skip("no top-level windows visible; cannot verify client_bounds")
	}
	for _, w := range wins {
		// ClientBounds must be a valid rectangle. When _NET_FRAME_EXTENTS
		// is absent it equals Bounds; either way it must be non-empty
		// whenever Bounds is non-empty.
		if !w.Bounds.Empty() && w.ClientBounds.Empty() {
			t.Fatalf("window %s has non-empty Bounds but empty ClientBounds", w.ID)
		}
	}
}

// errorsAs is a tiny shim so the test file does not have to import the
// stdlib errors package separately just for one call.
func errorsAs(err error, target **contract.AppError) bool {
	for err != nil {
		if app, ok := err.(*contract.AppError); ok {
			*target = app
			return true
		}
		type unwrap interface{ Unwrap() error }
		if u, ok := err.(unwrap); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}
