package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// jsonUnmarshal is a thin wrapper used by RegionRef tests to keep the
// test bodies focused on assertions, not boilerplate.
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func TestCoordMapRoundTripAndMap(t *testing.T) {
	capture := Bounds{X: 10, Y: 20, Width: 1000, Height: 500}
	image := Size{Width: 500, Height: 250}
	m := NewCoordMap(capture, image)
	if got, want := m.String(), "10,20,1000,500,500,250"; got != want {
		t.Fatalf("coord map string = %q, want %q", got, want)
	}
	parsed, err := ParseCoordMap(m.String())
	if err != nil {
		t.Fatalf("ParseCoordMap returned error: %v", err)
	}
	x, y, err := parsed.MapPoint(250, 125)
	if err != nil {
		t.Fatalf("MapPoint returned error: %v", err)
	}
	if x != 510 || y != 270 {
		t.Fatalf("mapped point = %d,%d, want 510,270", x, y)
	}
}

func TestCoordMapRejectsBadInput(t *testing.T) {
	if _, err := ParseCoordMap("1,2,3"); err == nil {
		t.Fatal("expected invalid coord map error")
	}
	m := CoordMap{CaptureWidth: 100, CaptureHeight: 100, ImageWidth: 10, ImageHeight: 10}
	if _, _, err := m.MapPoint(10, 0); err == nil {
		t.Fatal("expected out-of-bounds point error")
	}
}

func TestErrorCode(t *testing.T) {
	if got := ErrorCode(Validation("BAD", "bad input", nil)); got != ExitValidation {
		t.Fatalf("ErrorCode validation = %d, want %d", got, ExitValidation)
	}
}

func TestSchemaVersionConstants(t *testing.T) {
	if SchemaVersion == "" {
		t.Fatal("SchemaVersion must be a non-empty constant")
	}
	versions := SupportedSchemaVersions()
	if len(versions) == 0 {
		t.Fatal("SupportedSchemaVersions must return at least one version")
	}
	found := false
	for _, v := range versions {
		if v == SchemaVersion {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SchemaVersion %q must appear in SupportedSchemaVersions %v", SchemaVersion, versions)
	}
}

func TestValidateSchemaVersion(t *testing.T) {
	t.Run("empty rejected with REQUIRED code", func(t *testing.T) {
		err := ValidateSchemaVersion("")
		if err == nil {
			t.Fatal("expected error for empty schema_version")
		}
		var app *AppError
		if !errors.As(err, &app) {
			t.Fatalf("expected AppError, got %T", err)
		}
		if app.Code != "VALIDATION_SCHEMA_VERSION_REQUIRED" {
			t.Fatalf("code = %q, want VALIDATION_SCHEMA_VERSION_REQUIRED", app.Code)
		}
		if app.ExitCode != ExitValidation {
			t.Fatalf("exit code = %d, want %d", app.ExitCode, ExitValidation)
		}
		if !strings.Contains(app.Message, "schema_version") {
			t.Fatalf("message should mention schema_version: %q", app.Message)
		}
		if _, ok := app.Details["remediation"]; !ok {
			t.Fatal("details must include a remediation hint")
		}
	})
	t.Run("supported value accepted", func(t *testing.T) {
		if err := ValidateSchemaVersion(SchemaVersion); err != nil {
			t.Fatalf("current SchemaVersion must be accepted, got %v", err)
		}
	})
	t.Run("unsupported value rejected", func(t *testing.T) {
		err := ValidateSchemaVersion("99.99")
		if err == nil {
			t.Fatal("expected error for unsupported schema_version")
		}
		var app *AppError
		if !errors.As(err, &app) {
			t.Fatalf("expected AppError, got %T", err)
		}
		if app.Code != "VALIDATION_SCHEMA_VERSION_UNSUPPORTED" {
			t.Fatalf("code = %q, want VALIDATION_SCHEMA_VERSION_UNSUPPORTED", app.Code)
		}
	})
}

func TestResolveTableDriven(t *testing.T) {
	monitorIndex := 0
	validCoordMap := NewCoordMap(Bounds{X: 10, Y: 20, Width: 1000, Height: 500}, Size{Width: 500, Height: 250}).String()

	cases := []struct {
		name    string
		point   Point
		rctx    ResolveContext
		wantX   int
		wantY   int
		wantErr string // expected AppError.Code, or "" for success
	}{
		{
			name:  "screen space passes through",
			point: Point{X: 100, Y: 200, Space: CoordSpaceScreen},
			wantX: 100, wantY: 200,
		},
		{
			name:  "empty space defaults to screen",
			point: Point{X: 5, Y: 6},
			wantX: 5, wantY: 6,
		},
		{
			name:  "screenshot maps via coord_map",
			point: Point{X: 250, Y: 125, Space: CoordSpaceScreenshot, CoordMap: validCoordMap},
			wantX: 510, wantY: 270,
		},
		{
			name:    "screenshot without coord_map rejected",
			point:   Point{X: 1, Y: 1, Space: CoordSpaceScreenshot},
			wantErr: "COORD_MAP_REQUIRED",
		},
		{
			name:    "screenshot out of bounds rejected",
			point:   Point{X: 9999, Y: 9999, Space: CoordSpaceScreenshot, CoordMap: validCoordMap},
			wantErr: "POINT_OUT_OF_BOUNDS",
		},
		{
			name:    "window requires target",
			point:   Point{X: 0, Y: 0, Space: CoordSpaceWindow},
			wantErr: "COORD_TARGET_REQUIRED",
		},
		{
			name:  "window resolves via client_bounds",
			point: Point{X: 10, Y: 20, Space: CoordSpaceWindow, Target: WindowTarget{Title: "Firefox"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Title: "Firefox",
				Bounds:           Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds:     Bounds{X: 110, Y: 230, Width: 780, Height: 565},
				DecorationInsets: DecorationInsets{Left: 10, Top: 30, Right: 10, Bottom: 5},
			}}},
			wantX: 120, wantY: 250,
		},
		{
			name:  "window_frame uses outer bounds",
			point: Point{X: 10, Y: 5, Space: CoordSpaceWindowFrame, Target: WindowTarget{Class: "fam-ui"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:           Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds:     Bounds{X: 110, Y: 230, Width: 780, Height: 565},
				DecorationInsets: DecorationInsets{Left: 10, Top: 30, Right: 10, Bottom: 5},
			}}},
			wantX: 110, wantY: 205,
		},
		{
			name:  "window with zero insets falls back to outer bounds",
			point: Point{X: 5, Y: 6, Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds: Bounds{X: 50, Y: 60, Width: 400, Height: 300},
			}}},
			wantX: 55, wantY: 66,
		},
		{
			name:    "window target not found",
			point:   Point{X: 1, Y: 1, Space: CoordSpaceWindow, Target: WindowTarget{Title: "MissingApp"}},
			rctx:    ResolveContext{Windows: []WindowInfo{{ID: "0x1", Title: "Firefox", Bounds: Bounds{X: 0, Y: 0, Width: 100, Height: 100}}}},
			wantErr: "WINDOW_NOT_FOUND",
		},
		{
			name:  "window target ambiguous",
			point: Point{X: 1, Y: 1, Space: CoordSpaceWindow, Target: WindowTarget{Class: "term"}},
			rctx: ResolveContext{Windows: []WindowInfo{
				{ID: "0x1", Class: "term", Bounds: Bounds{X: 0, Y: 0, Width: 100, Height: 100}},
				{ID: "0x2", Class: "term", Bounds: Bounds{X: 100, Y: 0, Width: 100, Height: 100}},
			}},
			wantErr: "WINDOW_AMBIGUOUS",
		},
		{
			name:  "window coordinate out of bounds",
			point: Point{X: 9999, Y: 9999, Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 200, Height: 100},
				ClientBounds: Bounds{X: 0, Y: 20, Width: 200, Height: 80},
			}}},
			wantErr: "POINT_OUT_OF_BOUNDS",
		},
		{
			name:    "monitor requires monitor_index",
			point:   Point{X: 0, Y: 0, Space: CoordSpaceMonitor},
			wantErr: "COORD_MONITOR_INDEX_REQUIRED",
		},
		{
			name:  "monitor resolves with offset",
			point: Point{X: 100, Y: 50, Space: CoordSpaceMonitor, MonitorIndex: &monitorIndex},
			rctx:  ResolveContext{Monitors: []MonitorInfo{{Bounds: Bounds{X: 1920, Y: 0, Width: 1920, Height: 1080}}}},
			wantX: 2020, wantY: 50,
		},
		{
			name:    "monitor index out of range",
			point:   Point{X: 0, Y: 0, Space: CoordSpaceMonitor, MonitorIndex: &monitorIndex},
			rctx:    ResolveContext{Monitors: nil},
			wantErr: "MONITOR_INDEX_OUT_OF_RANGE",
		},
		{
			name:    "monitor coordinate out of bounds",
			point:   Point{X: 5000, Y: 5000, Space: CoordSpaceMonitor, MonitorIndex: &monitorIndex},
			rctx:    ResolveContext{Monitors: []MonitorInfo{{Bounds: Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}}}},
			wantErr: "POINT_OUT_OF_BOUNDS",
		},
		{
			name:    "unknown space rejected",
			point:   Point{X: 0, Y: 0, Space: "telepathy"},
			wantErr: "INVALID_COORDINATE_SPACE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y, err := Resolve(tc.point, tc.rctx)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got success (%d,%d)", tc.wantErr, x, y)
				}
				var app *AppError
				if !errors.As(err, &app) {
					t.Fatalf("expected AppError, got %T: %v", err, err)
				}
				if app.Code != tc.wantErr {
					t.Fatalf("code = %q, want %q", app.Code, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if x != tc.wantX || y != tc.wantY {
				t.Fatalf("got (%d,%d), want (%d,%d)", x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

func TestResolveRegion(t *testing.T) {
	monitorIndex := 1
	badMonitorIndex := 5

	cases := []struct {
		name    string
		ref     RegionRef
		rctx    ResolveContext
		want    Bounds
		wantErr string
	}{
		{
			name: "screen space passthrough",
			ref:  RegionRef{X: 100, Y: 200, Width: 50, Height: 60, Space: CoordSpaceScreen},
			want: Bounds{X: 100, Y: 200, Width: 50, Height: 60},
		},
		{
			name: "empty space defaults to screen",
			ref:  RegionRef{X: 0, Y: 0, Width: 913, Height: 976},
			want: Bounds{X: 0, Y: 0, Width: 913, Height: 976},
		},
		{
			name: "window resolves via client_bounds",
			ref:  RegionRef{X: 0, Y: 0, Width: 200, Height: 200, Space: CoordSpaceWindow, Target: WindowTarget{Class: "fam-ui"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:           Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds:     Bounds{X: 110, Y: 230, Width: 780, Height: 565},
				DecorationInsets: DecorationInsets{Left: 10, Top: 30, Right: 10, Bottom: 5},
			}}},
			want: Bounds{X: 110, Y: 230, Width: 200, Height: 200},
		},
		{
			name: "window_frame uses outer bounds",
			ref:  RegionRef{X: 0, Y: 0, Width: 200, Height: 200, Space: CoordSpaceWindowFrame, Target: WindowTarget{Class: "fam-ui"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:           Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds:     Bounds{X: 110, Y: 230, Width: 780, Height: 565},
				DecorationInsets: DecorationInsets{Left: 10, Top: 30, Right: 10, Bottom: 5},
			}}},
			want: Bounds{X: 100, Y: 200, Width: 200, Height: 200},
		},
		{
			name: "window space with offset inside client_bounds",
			ref:  RegionRef{X: 5, Y: 10, Width: 50, Height: 60, Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 10, Y: 30, Width: 780, Height: 565},
			}}},
			want: Bounds{X: 15, Y: 40, Width: 50, Height: 60},
		},
		{
			name: "monitor resolves with offset",
			ref:  RegionRef{X: 100, Y: 50, Width: 200, Height: 200, Space: CoordSpaceMonitor, MonitorIndex: &monitorIndex},
			rctx: ResolveContext{Monitors: []MonitorInfo{
				{Bounds: Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}},
				{Bounds: Bounds{X: 1920, Y: 0, Width: 1920, Height: 1080}},
			}},
			want: Bounds{X: 2020, Y: 50, Width: 200, Height: 200},
		},
		{
			name:    "window without target rejected",
			ref:     RegionRef{X: 0, Y: 0, Width: 10, Height: 10, Space: CoordSpaceWindow},
			wantErr: "REGION_TARGET_REQUIRED",
		},
		{
			name:    "window target not found",
			ref:     RegionRef{X: 0, Y: 0, Width: 10, Height: 10, Space: CoordSpaceWindow, Target: WindowTarget{Class: "missing"}},
			rctx:    ResolveContext{Windows: []WindowInfo{{ID: "0x1", Class: "fam-ui", Bounds: Bounds{X: 0, Y: 0, Width: 100, Height: 100}}}},
			wantErr: "WINDOW_NOT_FOUND",
		},
		{
			// v0.3.4 default: oversized window-space region is clamped to
			// the client bounds and the screen-space rect is returned. The
			// Strict opt-in path is covered separately in
			// TestResolveRegion_ClampOversize below.
			name: "window region out of bounds clamps by default",
			ref:  RegionRef{X: 0, Y: 0, Width: 9999, Height: 9999, Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 200, Height: 100},
				ClientBounds: Bounds{X: 0, Y: 20, Width: 200, Height: 80},
			}}},
			want: Bounds{X: 0, Y: 20, Width: 200, Height: 80},
		},
		{
			name: "window region out of bounds with strict rejected",
			ref:  RegionRef{X: 0, Y: 0, Width: 9999, Height: 9999, Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}, Strict: true},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 200, Height: 100},
				ClientBounds: Bounds{X: 0, Y: 20, Width: 200, Height: 80},
			}}},
			wantErr: "REGION_OUT_OF_BOUNDS",
		},
		{
			name:    "monitor without index rejected",
			ref:     RegionRef{X: 0, Y: 0, Width: 10, Height: 10, Space: CoordSpaceMonitor},
			wantErr: "REGION_MONITOR_INDEX_REQUIRED",
		},
		{
			name:    "monitor index out of range",
			ref:     RegionRef{X: 0, Y: 0, Width: 10, Height: 10, Space: CoordSpaceMonitor, MonitorIndex: &badMonitorIndex},
			rctx:    ResolveContext{Monitors: []MonitorInfo{{Bounds: Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}}}},
			wantErr: "MONITOR_INDEX_OUT_OF_RANGE",
		},
		{
			name:    "unknown space rejected",
			ref:     RegionRef{X: 0, Y: 0, Width: 10, Height: 10, Space: "telepathy"},
			wantErr: "INVALID_COORDINATE_SPACE",
		},
		{
			name: "zero-size window region returns full client bounds",
			ref:  RegionRef{Space: CoordSpaceWindow, Target: WindowTarget{ID: "0x1"}},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 10, Y: 30, Width: 780, Height: 565},
			}}},
			want: Bounds{X: 10, Y: 30, Width: 780, Height: 565},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRegion(tc.ref, tc.rctx)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got %+v", tc.wantErr, got)
				}
				var app *AppError
				if !errors.As(err, &app) {
					t.Fatalf("expected AppError, got %T: %v", err, err)
				}
				if app.Code != tc.wantErr {
					t.Fatalf("code = %q, want %q", app.Code, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestResolveRegion_ClampOversize pins the v0.3.4 clamp-by-default
// contract for oversized region requests. Window-, monitor-, and
// (separately tested) screen-space callers that exceed the available
// area get a clamped screen-space rectangle plus the OriginalRegion /
// ClampedRegion metadata under ResolveResult so the screenshot,
// find_*, and wait_for_* responses can surface region_clamped:true.
// Strict callers (RegionRef.Strict=true) still receive
// REGION_OUT_OF_BOUNDS — the fail-loudly path is preserved as an
// opt-in.
//
// The cases cover the three coordinate spaces named in the task
// contract — window (the original Codex repro), monitor, and the
// screen-space passthrough surface (passthrough; clamp against the
// physical screen happens in screen.CaptureRGBA, which lives in the
// screen package, not here). The default-clamp branch is exercised
// in addition to the strict-fail branch so future refactors that lose
// the Strict toggle would surface as a test break.
func TestResolveRegion_ClampOversize(t *testing.T) {
	monitorIndex := 0

	cases := []struct {
		name       string
		ref        RegionRef
		rctx       ResolveContext
		wantBounds Bounds
		// when set, asserts ResolveResult.Clamped is true and the
		// original / clamped local rects match.
		wantClamped  bool
		wantOriginal Bounds
		wantClipped  Bounds
		wantErr      string // non-empty when Strict path should error
	}{
		{
			// Codex repro: window-space rectangle blows past the
			// client bounds. Default clamps to the client bounds and
			// returns the absolute screen-space rect. Original carries
			// the requested {0,0,99999,99999}; clamped carries the
			// client-bounds-relative rectangle {0,0,clientW,clientH}.
			name: "window space oversize clamps to client bounds",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space:  CoordSpaceWindow,
				Target: WindowTarget{Class: "fam-ui"},
			},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:       Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 110, Y: 230, Width: 780, Height: 565},
			}}},
			wantBounds:   Bounds{X: 110, Y: 230, Width: 780, Height: 565},
			wantClamped:  true,
			wantOriginal: Bounds{X: 0, Y: 0, Width: 99999, Height: 99999},
			wantClipped:  Bounds{X: 0, Y: 0, Width: 780, Height: 565},
		},
		{
			name: "window space oversize with strict returns REGION_OUT_OF_BOUNDS",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space:  CoordSpaceWindow,
				Target: WindowTarget{Class: "fam-ui"},
				Strict: true,
			},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:       Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 110, Y: 230, Width: 780, Height: 565},
			}}},
			wantErr: "REGION_OUT_OF_BOUNDS",
		},
		{
			// Partial overshoot: caller asks for {50,100,1000,500} but
			// the window is only 200x100 wide. Clamp shrinks to
			// {50,100,150,0}? No — clamp output keeps the offset and
			// shrinks the extent. wait — y=100 is past the 100-tall
			// client_bounds. We pick a case where the clamp leaves a
			// non-empty rectangle: offset 50,10 + size 1000,500 against
			// a 200x100 client.
			name: "window space partial overshoot clamps offset+extent",
			ref: RegionRef{
				X: 50, Y: 10, Width: 1000, Height: 500,
				Space:  CoordSpaceWindow,
				Target: WindowTarget{ID: "0x1"},
			},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 200, Height: 100},
				ClientBounds: Bounds{X: 0, Y: 20, Width: 200, Height: 80},
			}}},
			wantBounds:   Bounds{X: 50, Y: 30, Width: 150, Height: 70},
			wantClamped:  true,
			wantOriginal: Bounds{X: 50, Y: 10, Width: 1000, Height: 500},
			wantClipped:  Bounds{X: 50, Y: 10, Width: 150, Height: 70},
		},
		{
			// window_frame-space oversize: clamp anchors on the outer
			// bounds, not client_bounds. Original caller asked for
			// {0,0,99999,99999} which we shrink to the outer rectangle.
			name: "window_frame space oversize clamps to outer bounds",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space:  CoordSpaceWindowFrame,
				Target: WindowTarget{Class: "fam-ui"},
			},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", Class: "fam-ui",
				Bounds:       Bounds{X: 100, Y: 200, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 110, Y: 230, Width: 780, Height: 565},
			}}},
			wantBounds:   Bounds{X: 100, Y: 200, Width: 800, Height: 600},
			wantClamped:  true,
			wantOriginal: Bounds{X: 0, Y: 0, Width: 99999, Height: 99999},
			wantClipped:  Bounds{X: 0, Y: 0, Width: 800, Height: 600},
		},
		{
			name: "monitor space oversize clamps to monitor bounds",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space:        CoordSpaceMonitor,
				MonitorIndex: &monitorIndex,
			},
			rctx: ResolveContext{Monitors: []MonitorInfo{
				{Bounds: Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}},
			}},
			wantBounds:   Bounds{X: 0, Y: 0, Width: 1920, Height: 1080},
			wantClamped:  true,
			wantOriginal: Bounds{X: 0, Y: 0, Width: 99999, Height: 99999},
			wantClipped:  Bounds{X: 0, Y: 0, Width: 1920, Height: 1080},
		},
		{
			name: "monitor space oversize with strict returns REGION_OUT_OF_BOUNDS",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space:        CoordSpaceMonitor,
				MonitorIndex: &monitorIndex,
				Strict:       true,
			},
			rctx: ResolveContext{Monitors: []MonitorInfo{
				{Bounds: Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}},
			}},
			wantErr: "REGION_OUT_OF_BOUNDS",
		},
		{
			// Screen-space passthrough: ResolveRegion does NOT clamp
			// against the physical screen bounds — that responsibility
			// stays with screen.CaptureRGBA / screen.Capture which
			// already do their own clamp(). So an oversized screen-space
			// region passes through unchanged. This case pins that
			// behavior so future refactors that move the clamp up into
			// contract would have to explicitly opt screen-space in.
			name: "screen space oversize passes through unchanged",
			ref: RegionRef{
				X: 0, Y: 0, Width: 99999, Height: 99999,
				Space: CoordSpaceScreen,
			},
			wantBounds: Bounds{X: 0, Y: 0, Width: 99999, Height: 99999},
		},
		{
			// In-bounds window-space request: clamp must NOT fire so
			// the response wire shape matches v0.3.3 exactly. This is
			// the "no new fields when no clamp happens" backward-compat
			// pin from the task contract.
			name: "window space in-bounds does not clamp",
			ref: RegionRef{
				X: 5, Y: 10, Width: 50, Height: 60,
				Space:  CoordSpaceWindow,
				Target: WindowTarget{ID: "0x1"},
			},
			rctx: ResolveContext{Windows: []WindowInfo{{
				ID: "0x1", XID: 1,
				Bounds:       Bounds{X: 0, Y: 0, Width: 800, Height: 600},
				ClientBounds: Bounds{X: 10, Y: 30, Width: 780, Height: 565},
			}}},
			wantBounds: Bounds{X: 15, Y: 40, Width: 50, Height: 60},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRegionDetailed(tc.ref, tc.rctx)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got %+v", tc.wantErr, got)
				}
				var app *AppError
				if !errors.As(err, &app) {
					t.Fatalf("expected AppError, got %T: %v", err, err)
				}
				if app.Code != tc.wantErr {
					t.Fatalf("code = %q, want %q", app.Code, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Bounds != tc.wantBounds {
				t.Fatalf("Bounds = %+v, want %+v", got.Bounds, tc.wantBounds)
			}
			if got.Clamped != tc.wantClamped {
				t.Fatalf("Clamped = %v, want %v", got.Clamped, tc.wantClamped)
			}
			if tc.wantClamped {
				if got.OriginalRegion != tc.wantOriginal {
					t.Fatalf("OriginalRegion = %+v, want %+v", got.OriginalRegion, tc.wantOriginal)
				}
				if got.ClampedRegion != tc.wantClipped {
					t.Fatalf("ClampedRegion = %+v, want %+v", got.ClampedRegion, tc.wantClipped)
				}
			} else {
				// When no clamp occurred the metadata must stay
				// zero-valued so JSON marshaling omits the optional
				// region_clamped / original_region / clamped_region
				// fields downstream.
				if got.OriginalRegion != (Bounds{}) {
					t.Fatalf("OriginalRegion should be zero when not clamped, got %+v", got.OriginalRegion)
				}
				if got.ClampedRegion != (Bounds{}) {
					t.Fatalf("ClampedRegion should be zero when not clamped, got %+v", got.ClampedRegion)
				}
			}
		})
	}
}

// TestResolveRegion_ClampWindowFullyOutsideBounds pins the edge case
// where a clamp would leave a zero-area rectangle: the clamped result
// is unrecoverable so we surface REGION_OUT_OF_BOUNDS regardless of
// Strict. Documents the intent — clamp-by-default only helps when
// there is some non-empty sub-rectangle inside the available area.
func TestResolveRegion_ClampWindowFullyOutsideBounds(t *testing.T) {
	ref := RegionRef{
		X: 500, Y: 500, Width: 10, Height: 10,
		Space:  CoordSpaceWindow,
		Target: WindowTarget{ID: "0x1"},
	}
	rctx := ResolveContext{Windows: []WindowInfo{{
		ID: "0x1", XID: 1,
		Bounds:       Bounds{X: 0, Y: 0, Width: 200, Height: 100},
		ClientBounds: Bounds{X: 0, Y: 0, Width: 200, Height: 100},
	}}}
	_, err := ResolveRegionDetailed(ref, rctx)
	if err == nil {
		t.Fatal("expected REGION_OUT_OF_BOUNDS for region entirely outside the window")
	}
	var app *AppError
	if !errors.As(err, &app) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if app.Code != "REGION_OUT_OF_BOUNDS" {
		t.Fatalf("code = %q, want REGION_OUT_OF_BOUNDS", app.Code)
	}
}

// TestRegionRefBareBoundsUnmarshal pins the v0.1/v0.2 wire shape: a
// bare {x,y,width,height} JSON region must Unmarshal into a RegionRef
// whose Space is "" (treated as screen) and whose Bounds() are intact.
func TestRegionRefBareBoundsUnmarshal(t *testing.T) {
	const payload = `{"x":1050,"y":123,"width":1276,"height":931}`
	var ref RegionRef
	if err := jsonUnmarshal(payload, &ref); err != nil {
		t.Fatalf("unmarshal bare bounds failed: %v", err)
	}
	if ref.Space != "" {
		t.Fatalf("expected empty Space for bare bounds, got %q", ref.Space)
	}
	if ref.Bounds() != (Bounds{X: 1050, Y: 123, Width: 1276, Height: 931}) {
		t.Fatalf("bounds = %+v", ref.Bounds())
	}
	resolved, err := ResolveRegion(ref, ResolveContext{})
	if err != nil {
		t.Fatalf("ResolveRegion error: %v", err)
	}
	if resolved != ref.Bounds() {
		t.Fatalf("expected passthrough resolve, got %+v", resolved)
	}
}

// TestRegionRefExtendedUnmarshal verifies the new wire shape carries
// space + target through Unmarshal so window-space callers can submit
// the killer payload: {x,y,w,h,space:"window",target:{class:...}}.
func TestRegionRefExtendedUnmarshal(t *testing.T) {
	const payload = `{"x":0,"y":0,"width":200,"height":200,"space":"window","target":{"class":"fam-ui"}}`
	var ref RegionRef
	if err := jsonUnmarshal(payload, &ref); err != nil {
		t.Fatalf("unmarshal extended region failed: %v", err)
	}
	if ref.Space != CoordSpaceWindow {
		t.Fatalf("Space = %q, want %q", ref.Space, CoordSpaceWindow)
	}
	if ref.Target.Class != "fam-ui" {
		t.Fatalf("Target.Class = %q, want fam-ui", ref.Target.Class)
	}
}

func TestPointScreenPointStillRoutesThroughResolve(t *testing.T) {
	// Backwards-compat shim: existing call sites that still call
	// ScreenPoint() must continue to work for screen/screenshot.
	p := Point{X: 7, Y: 8}
	x, y, err := p.ScreenPoint()
	if err != nil || x != 7 || y != 8 {
		t.Fatalf("ScreenPoint screen passthrough = (%d,%d,%v)", x, y, err)
	}
}
