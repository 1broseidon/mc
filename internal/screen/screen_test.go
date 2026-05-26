package screen

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

func TestCaptureRequestJSONContract(t *testing.T) {
	req := CaptureRequest{
		Out:         "/tmp/shot.jpg",
		Region:      contract.RegionRefFromBounds(contract.Bounds{X: 1, Y: 2, Width: 3, Height: 4}),
		MaxEdge:     1568,
		Zoom:        true,
		ZoomX:       10,
		ZoomY:       20,
		ZoomSize:    30,
		Format:      "jpeg",
		Cursor:      true,
		JPEGQuality: 80,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, key := range []string{"out", "region", "max_edge", "zoom", "zoom_x", "zoom_y", "zoom_size", "format", "cursor", "jpeg_quality"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing JSON key %q in %s", key, b)
		}
	}
	if _, ok := got["MaxEdge"]; ok {
		t.Fatalf("exported Go field name leaked into JSON: %s", b)
	}
}

func TestCaptureFormat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		mime string
		ext  string
	}{
		{in: "", mime: "image/png", ext: "png"},
		{in: "png", mime: "image/png", ext: "png"},
		{in: "jpg", mime: "image/jpeg", ext: "jpg"},
		{in: "jpeg", mime: "image/jpeg", ext: "jpg"},
	} {
		_, mime, ext, err := captureFormat(tc.in)
		if err != nil {
			t.Fatalf("captureFormat(%q) failed: %v", tc.in, err)
		}
		if mime != tc.mime || ext != tc.ext {
			t.Fatalf("captureFormat(%q) = mime %q ext %q", tc.in, mime, ext)
		}
	}
	if _, _, _, err := captureFormat("gif"); err == nil {
		t.Fatal("expected invalid format to fail")
	}
}

// TestInfoMonitors exercises the get_screen_info / monitors contract:
// at least one monitor must be reported, at least one of them must
// carry primary:true, and every monitor must advertise a positive
// scale. Skipped when DISPLAY is unset (CI without X11) so the test
// is portable.
func TestInfoMonitors(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set; skipping X11-dependent monitor test")
	}
	info, err := Info(context.Background())
	if err != nil {
		t.Fatalf("Info() failed: %v", err)
	}
	if len(info.Monitors) == 0 {
		t.Fatalf("expected at least one monitor, got 0")
	}
	hasPrimary := false
	for i, mon := range info.Monitors {
		if mon.Scale <= 0 {
			t.Fatalf("monitor[%d] %q has non-positive scale %v", i, mon.Name, mon.Scale)
		}
		if mon.Index != i {
			t.Fatalf("monitor[%d] index mismatch: got %d want %d", i, mon.Index, i)
		}
		if mon.Primary {
			hasPrimary = true
		}
	}
	if !hasPrimary {
		t.Fatalf("expected at least one monitor marked primary, got none: %+v", info.Monitors)
	}
}

// TestComputeScaleNegative pins the contract that scale never falls
// below 1.0 when inputs are unavailable or invalid (zero-width
// monitors, missing millimeter dimensions). A non-positive scale
// would break point-translation math elsewhere.
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
		got := computeScale(tc.widthPx, tc.widthMm)
		if got != 1.0 {
			t.Fatalf("computeScale(%s) = %v; want fallback 1.0", tc.name, got)
		}
	}
}

// TestComputeScalePositive verifies the DPI math against the
// canonical 96 DPI baseline: a 1920px monitor measuring 508mm wide
// (~20.0 inches) yields ~96 DPI → scale ≈ 1.0; halving the inch
// measurement doubles the DPI → scale ≈ 2.0.
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

// TestLogicalCoordsToggle verifies the package-level toggle round-
// trips. Default is false; set true; reset to false. The screen and
// input packages both read this through LogicalCoordsEnabled.
func TestLogicalCoordsToggle(t *testing.T) {
	if LogicalCoordsEnabled() {
		t.Fatalf("expected LogicalCoordsEnabled() default to be false")
	}
	SetLogicalCoords(true)
	if !LogicalCoordsEnabled() {
		t.Fatalf("expected toggle to flip true after SetLogicalCoords(true)")
	}
	SetLogicalCoords(false)
	if LogicalCoordsEnabled() {
		t.Fatalf("expected toggle to reset to false after SetLogicalCoords(false)")
	}
}

// ensureBoundsType is a compile-time guard ensuring MonitorInfo's
// Bounds field has not silently lost its contract.Bounds shape — a
// change here would break v0.1 wire compatibility for
// screen.bounds.
var _ contract.Bounds = contract.MonitorInfo{}.Bounds
