//go:build linux

package x11adapter

import (
	"context"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// hasDisplay reports whether a live X server is reachable. Capture tests
// skip (rather than fail) on headless CI so the package stays portable.
func hasDisplay() bool {
	d, err := x11.Open()
	if err != nil {
		return false
	}
	d.Close()
	return true
}

func TestProviderRegisteredAsX11(t *testing.T) {
	// The adapter's init self-registers; Current() must be the X11 backend.
	if got := platform.Current().Name(); got != "x11" {
		t.Fatalf("platform.Current().Name() = %q, want %q", got, "x11")
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	// Compile-time guarantee plus a nil-accessor sanity check.
	var _ platform.Provider = New()
	p := New()
	if p.Pointer() == nil || p.Keyboard() == nil || p.Screen() == nil ||
		p.Windows() == nil || p.Clipboard() == nil {
		t.Fatal("provider returned a nil capability")
	}
	if _, ok := p.Accessibility(); !ok {
		t.Fatal("accessibility should be available on the X11 adapter")
	}
	if _, ok := p.Activity(); !ok {
		t.Fatal("activity should be available on the X11 adapter")
	}
}

func TestClipboardSelectionsAdvertised(t *testing.T) {
	sels := New().Clipboard().Selections()
	want := map[platform.Selection]bool{platform.SelectionClipboard: true, platform.SelectionPrimary: true}
	if len(sels) != len(want) {
		t.Fatalf("Selections() = %v, want clipboard+primary", sels)
	}
	for _, s := range sels {
		if !want[s] {
			t.Fatalf("unexpected selection %q", s)
		}
	}
}

// TestWindowsListLiveDisplay smokes the migrated EWMH window enumeration
// against a live X server: List must not error, and every record must
// carry a stable hex ID plus a non-empty ClientBounds whenever Bounds is
// non-empty (the frame-extent fallback contract).
func TestWindowsListLiveDisplay(t *testing.T) {
	if !hasDisplay() {
		t.Skip("no live X server; skipping live window-list test")
	}
	wins, err := New().Windows().List(context.Background())
	if err != nil {
		t.Fatalf("Windows().List: %v", err)
	}
	for _, w := range wins {
		if w.ID == "" {
			t.Fatalf("window has empty ID: %+v", w)
		}
		if !w.Bounds.Empty() && w.ClientBounds.Empty() {
			t.Fatalf("window %s has non-empty Bounds but empty ClientBounds", w.ID)
		}
	}
}

func TestScreenGrabLiveDisplay(t *testing.T) {
	if !hasDisplay() {
		t.Skip("no live X server; skipping live capture test")
	}
	sg := New().Screen()
	ctx := context.Background()

	bounds, err := sg.ScreenBounds(ctx)
	if err != nil {
		t.Fatalf("ScreenBounds: %v", err)
	}
	if bounds.Empty() {
		t.Fatalf("ScreenBounds returned empty: %+v", bounds)
	}

	// Grab a small top-left patch and confirm dimensions round-trip.
	want := contract.Bounds{X: 0, Y: 0, Width: 32, Height: 16}
	img, got, err := sg.Grab(ctx, want)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if got.Width != want.Width || got.Height != want.Height {
		t.Fatalf("Grab bounds = %+v, want %+v", got, want)
	}
	if img.Bounds().Dx() != want.Width || img.Bounds().Dy() != want.Height {
		t.Fatalf("image size = %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), want.Width, want.Height)
	}
	// Every pixel must be fully opaque (adapter forces A=255).
	if _, _, _, a := img.At(0, 0).RGBA(); a>>8 != 255 {
		t.Fatalf("pixel alpha = %d, want 255", a>>8)
	}
}

// TestComputeScaleNegative pins the contract that scale never falls
// below 1.0 when inputs are unavailable or invalid. Migrated from
// internal/screen when the RandR scale derivation moved into the adapter.
func TestComputeScaleNegative(t *testing.T) {
	cases := []struct {
		name    string
		widthPx int
		widthMm uint32
	}{
		{name: "zero pixels", widthPx: 0, widthMm: 100},
		{name: "negative pixels", widthPx: -1, widthMm: 100},
		{name: "zero millimeters", widthPx: 1920, widthMm: 0},
	}
	for _, tc := range cases {
		if got := computeScale(tc.widthPx, tc.widthMm); got != 1.0 {
			t.Fatalf("computeScale(%s) = %v; want fallback 1.0", tc.name, got)
		}
	}
}

// TestComputeScalePositive verifies the DPI math against the canonical
// 96 DPI baseline. Migrated from internal/screen.
func TestComputeScalePositive(t *testing.T) {
	standard := computeScale(1920, 508)
	if standard < 0.9 || standard > 1.1 {
		t.Fatalf("expected scale near 1.0 for 1920px / 508mm, got %v", standard)
	}
	hidpi := computeScale(1920, 254)
	if hidpi < 1.9 || hidpi > 2.1 {
		t.Fatalf("expected scale near 2.0 for 1920px / 254mm, got %v", hidpi)
	}
}

// TestRefreshFromMode pins the refresh-rate derivation, including the
// zero-guard for missing mode timings.
func TestRefreshFromMode(t *testing.T) {
	if got := refreshFromMode(0, 100, 100); got != 0 {
		t.Fatalf("refreshFromMode with zero dotclock = %d, want 0", got)
	}
	// 60Hz: dotClock / (htotal*vtotal) ~= 60.
	if got := refreshFromMode(148_500_000, 2200, 1125); got != 60 {
		t.Fatalf("refreshFromMode 1080p60 = %d, want 60", got)
	}
}

func TestMonitorsLiveDisplay(t *testing.T) {
	if !hasDisplay() {
		t.Skip("no live X server; skipping monitor enumeration test")
	}
	mons, err := New().Screen().Monitors(context.Background())
	if err != nil {
		t.Fatalf("Monitors: %v", err)
	}
	// RandR may legitimately report zero (service synthesizes a root
	// monitor); when it reports any, exactly one must be primary.
	if len(mons) > 0 {
		primaries := 0
		for _, m := range mons {
			if m.Primary {
				primaries++
			}
			if m.Scale <= 0 {
				t.Fatalf("monitor %d has non-positive scale %v", m.Index, m.Scale)
			}
		}
		if primaries != 1 {
			t.Fatalf("got %d primary monitors, want exactly 1", primaries)
		}
	}
}
