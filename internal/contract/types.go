package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ExitSuccess      = 0
	ExitGeneric      = 1
	ExitValidation   = 2
	ExitNotFound     = 3
	ExitDependency   = 4
	ExitPrecondition = 5
	ExitCancelled    = 6
)

// SchemaVersion is the current action-payload schema version that this
// build of MyComputer speaks. Every computer_actions request and response
// must carry this value at the top level. Bumped only on breaking
// changes to the request/response envelopes.
const SchemaVersion = "0.2"

// ValidateSchemaVersion checks that the supplied value is a recognized
// action-payload schema version. Empty values are rejected with
// VALIDATION_SCHEMA_VERSION_REQUIRED (exit 2) and include a
// remediation hint pointing at the current supported set. Unrecognized
// non-empty values are rejected with
// VALIDATION_SCHEMA_VERSION_UNSUPPORTED.
func ValidateSchemaVersion(value string) error {
	if value == "" {
		return Validation(
			"VALIDATION_SCHEMA_VERSION_REQUIRED",
			"computer_actions requests require a top-level schema_version field; v0.1 clients must upgrade their payload",
			map[string]any{
				"remediation":        "add \"schema_version\": \"" + SchemaVersion + "\" to the request envelope",
				"supported_versions": SupportedSchemaVersions(),
			},
		)
	}
	for _, v := range SupportedSchemaVersions() {
		if v == value {
			return nil
		}
	}
	return Validation(
		"VALIDATION_SCHEMA_VERSION_UNSUPPORTED",
		"schema_version is not supported by this server",
		map[string]any{
			"received":           value,
			"supported_versions": SupportedSchemaVersions(),
		},
	)
}

// SupportedSchemaVersions enumerates every schema version this server
// accepts on inbound requests. Requests without a schema_version field
// at all are rejected with VALIDATION_SCHEMA_VERSION_REQUIRED; an
// explicit "0.1" is accepted for clients that have started declaring
// the field but haven't migrated their action shapes yet. The current
// preferred version is SchemaVersion. v0.1 wire compatibility for
// screenshot responses (coord_map string) is preserved separately.
func SupportedSchemaVersions() []string {
	return []string{"0.2", "0.1"}
}

// Coordinate spaces. Every Point carries one of these in its Space
// field. The Resolve() helper is the single conversion site that turns
// a Point in any space into absolute screen coordinates.
const (
	CoordSpaceScreen      = "screen"
	CoordSpaceScreenshot  = "screenshot"
	CoordSpaceWindow      = "window"
	CoordSpaceWindowFrame = "window_frame"
	CoordSpaceMonitor     = "monitor"
)

// WindowGeometryRefusedCode is emitted as a warning (not error) in
// ActionResult.Details when a window verb's post-op geometry diverges
// from the requested geometry beyond a small tolerance — typically a
// tiling WM (i3/bspwm/sway-X11) refusing to honor floating geometry.
// The action result remains ok:true; the agent decides recovery.
const WindowGeometryRefusedCode = "WINDOW_GEOMETRY_REFUSED"

// WindowGeometryDivergedCode is emitted as a warning (not error) in
// ActionResult.Details when a window verb's post-op WM-reported
// client_bounds disagrees with the actually-rendered surface — typical
// of immediate-mode toolkits (Gio, Dear ImGui, Flutter-Linux, egui)
// that do not react to ConfigureNotify, leaving the WM-reported bounds
// containing exposed desktop wallpaper instead of app content.
//
// Detection samples a 50x50 patch at the bottom-right interior corner
// of client_bounds and compares it to the root window's dominant
// background color; >=70% root-color hits flips the warning. The
// action remains ok:true. Joins the WINDOW_GEOMETRY_REFUSED family of
// advisory warnings; the agent should fall back to find_color /
// find_text targeting instead of trusting WM coordinates.
const WindowGeometryDivergedCode = "WINDOW_GEOMETRY_DIVERGED"

// CoordSpace is a typed string alias for the coordinate-space enum.
// Stored as plain string in Point.Space for JSON round-tripping; this
// type is for internal type-safety where it matters.
type CoordSpace = string

type AppError struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Details  map[string]any `json:"details,omitempty"`
	ExitCode int            `json:"-"`
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func ErrorCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var app *AppError
	if errors.As(err, &app) && app.ExitCode != 0 {
		return app.ExitCode
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ExitCancelled
	}
	return ExitGeneric
}

func NewError(exit int, code, message string, details map[string]any) *AppError {
	return &AppError{ExitCode: exit, Code: code, Message: message, Details: details}
}

func Validation(code, message string, details map[string]any) *AppError {
	return NewError(ExitValidation, code, message, details)
}

func NotFound(code, message string, details map[string]any) *AppError {
	return NewError(ExitNotFound, code, message, details)
}

func Dependency(code, message string, details map[string]any) *AppError {
	return NewError(ExitDependency, code, message, details)
}

func Precondition(code, message string, details map[string]any) *AppError {
	return NewError(ExitPrecondition, code, message, details)
}

func Cancelled(message string) *AppError {
	return NewError(ExitCancelled, "CANCELLED", message, nil)
}

type ErrorEnvelope struct {
	Error *AppError `json:"error"`
}

func MarshalError(err error) []byte {
	var app *AppError
	if !errors.As(err, &app) {
		app = NewError(ExitGeneric, "ERROR", err.Error(), nil)
	}
	b, marshalErr := json.Marshal(ErrorEnvelope{Error: app})
	if marshalErr != nil {
		return []byte(`{"error":{"code":"ERROR","message":"failed to marshal error"}}`)
	}
	return b
}

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (b Bounds) Empty() bool {
	return b.Width <= 0 || b.Height <= 0
}

func (b Bounds) Contains(x, y int) bool {
	return x >= b.X && y >= b.Y && x < b.X+b.Width && y < b.Y+b.Height
}

// RegionRef is the coord-space-aware superset of Bounds used by every
// request that previously accepted a bare screen-space Bounds (find_*,
// wait_for_*, screenshot, etc.). On the wire the JSON shape is
// backward-compatible: a bare `{x, y, width, height}` payload (the v0.1
// / v0.2 shape) deserializes into a RegionRef whose Space is "" and
// resolves as screen-space — no behavior change for existing clients.
//
// The extended shape adds:
//
//   - Space:        "screen" (default), "window", "window_frame", or
//     "monitor". Empty equals "screen".
//   - Target:       window selector — REQUIRED when Space is "window"
//     or "window_frame". Mirrors Point.Target.
//   - MonitorIndex: zero-based monitor index — REQUIRED when Space is
//     "monitor". Mirrors Point.MonitorIndex.
//
// Coordinate resolution: ResolveRegion turns a RegionRef into an
// absolute screen-space Bounds against the supplied ResolveContext
// (window/monitor lists). It is the single entry point for region
// translation; every consumer of an extended region MUST route through
// ResolveRegion before doing X11 work, so coord_map and downstream
// screen-space outputs remain consistent.
type RegionRef struct {
	X            int          `json:"x"`
	Y            int          `json:"y"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	Space        string       `json:"space,omitempty" jsonschema:"screen (default), window, window_frame, or monitor"`
	Target       WindowTarget `json:"target,omitzero" jsonschema:"window selector; required when space=window or window_frame"`
	MonitorIndex *int         `json:"monitor_index,omitempty" jsonschema:"zero-based monitor index; required when space=monitor"`
}

// Empty reports whether the RegionRef carries no usable size. Width or
// height <= 0 counts as empty so callers can use the same "fall back to
// default" pattern as Bounds.Empty.
func (r RegionRef) Empty() bool {
	return r.Width <= 0 || r.Height <= 0
}

// Bounds returns the bare {x, y, width, height} portion of the
// RegionRef as a Bounds value. NOTE: this is the LOCAL (pre-resolution)
// rectangle — for window/monitor spaces it is relative to the window's
// or monitor's origin, NOT a screen-space rectangle. Use ResolveRegion
// when you need absolute screen coordinates.
func (r RegionRef) Bounds() Bounds {
	return Bounds{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
}

// RegionRefFromBounds wraps a bare Bounds in a screen-space RegionRef.
// Convenience for internal call sites that already have a Bounds and
// want to flow through the RegionRef-shaped API.
func RegionRefFromBounds(b Bounds) RegionRef {
	return RegionRef{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
}

// ResolveRegion is the single internal entry point that converts a
// RegionRef in any declared coordinate space into an absolute
// screen-space Bounds. Mirrors Resolve() for Points.
//
// Behavior:
//
//   - Space "" or "screen": returns the bare Bounds unchanged.
//   - Space "window": requires Target and a non-empty
//     ResolveContext.Windows. The rectangle is interpreted relative to
//     the matched window's client_bounds (decorations excluded). Falls
//     back to outer bounds when client_bounds are unknown.
//   - Space "window_frame": same as "window" but anchored on the
//     matched window's outer (frame-inclusive) bounds.
//   - Space "monitor": requires MonitorIndex and a non-empty
//     ResolveContext.Monitors. Rectangle is interpreted relative to
//     that monitor's bounds.
//
// The returned Bounds is the *requested* rectangle in absolute screen
// coordinates. Callers (screen.Capture, screen.CaptureRGBA) are still
// responsible for clamping to the screen bounds — ResolveRegion does
// not silently truncate. Any unknown space is a hard validation error.
//
// A zero-sized RegionRef (Empty() == true) is returned as a zero-sized
// Bounds with the resolved origin so callers can apply their own
// "empty means default" fallback (e.g. focused-window region).
func ResolveRegion(r RegionRef, rctx ResolveContext) (Bounds, error) {
	switch r.Space {
	case "", CoordSpaceScreen:
		return r.Bounds(), nil
	case CoordSpaceWindow, CoordSpaceWindowFrame:
		if r.Target.Empty() {
			return Bounds{}, Validation("REGION_TARGET_REQUIRED", "window-space region requires a target (id, title, class, or pid)", map[string]any{"space": r.Space})
		}
		win, err := matchWindow(rctx.Windows, r.Target)
		if err != nil {
			return Bounds{}, err
		}
		origin := win.Bounds
		if r.Space == CoordSpaceWindow {
			if !win.ClientBounds.Empty() {
				origin = win.ClientBounds
			}
		}
		if origin.Empty() {
			return Bounds{}, Validation("WINDOW_BOUNDS_UNKNOWN", "matched window has no usable bounds", map[string]any{"target": r.Target, "window_id": win.ID})
		}
		if r.Width <= 0 || r.Height <= 0 {
			// Zero-sized region in window space: return the full window
			// rect so callers can use it as a "default to this window"
			// shorthand.
			return origin, nil
		}
		if r.X < 0 || r.Y < 0 || r.X+r.Width > origin.Width || r.Y+r.Height > origin.Height {
			return Bounds{}, Validation("REGION_OUT_OF_BOUNDS", "window-space region is outside the window bounds", map[string]any{
				"space":     r.Space,
				"region":    r.Bounds(),
				"window_id": win.ID,
				"bounds":    origin,
			})
		}
		return Bounds{X: origin.X + r.X, Y: origin.Y + r.Y, Width: r.Width, Height: r.Height}, nil
	case CoordSpaceMonitor:
		if r.MonitorIndex == nil {
			return Bounds{}, Validation("REGION_MONITOR_INDEX_REQUIRED", "monitor-space region requires a monitor_index", map[string]any{"space": r.Space})
		}
		idx := *r.MonitorIndex
		if idx < 0 || idx >= len(rctx.Monitors) {
			return Bounds{}, Validation("MONITOR_INDEX_OUT_OF_RANGE", "monitor_index is outside the available monitors", map[string]any{
				"monitor_index": idx,
				"monitor_count": len(rctx.Monitors),
			})
		}
		bounds := rctx.Monitors[idx].Bounds
		if bounds.Empty() {
			return Bounds{}, Validation("MONITOR_BOUNDS_UNKNOWN", "selected monitor has no usable bounds", map[string]any{"monitor_index": idx})
		}
		if r.Width <= 0 || r.Height <= 0 {
			return bounds, nil
		}
		if r.X < 0 || r.Y < 0 || r.X+r.Width > bounds.Width || r.Y+r.Height > bounds.Height {
			return Bounds{}, Validation("REGION_OUT_OF_BOUNDS", "monitor-space region is outside the monitor bounds", map[string]any{
				"space":         r.Space,
				"region":        r.Bounds(),
				"monitor_index": idx,
				"bounds":        bounds,
			})
		}
		return Bounds{X: bounds.X + r.X, Y: bounds.Y + r.Y, Width: r.Width, Height: r.Height}, nil
	default:
		return Bounds{}, Validation("INVALID_COORDINATE_SPACE", "region coordinate space must be screen, window, window_frame, or monitor", map[string]any{"space": r.Space})
	}
}

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// WindowTarget identifies a window for matching/resolution. At least
// one of the fields must be non-zero; the matcher walks ID > PID >
// Class > Title in declaration order. Used by focus_window, by every
// window_* verb, and by Point.Target when point.space="window" or
// "window_frame". JSON shape matches the existing focus_window input.
type WindowTarget struct {
	ID    string `json:"id,omitempty" jsonschema:"hex or decimal X11 window id"`
	Title string `json:"title,omitempty" jsonschema:"case-insensitive substring match against window title"`
	Class string `json:"class,omitempty" jsonschema:"case-insensitive exact match against WM_CLASS"`
	PID   uint32 `json:"pid,omitempty" jsonschema:"process id"`
}

// Empty reports whether the target carries no selectable criteria.
func (t WindowTarget) Empty() bool {
	return t.ID == "" && t.Title == "" && t.Class == "" && t.PID == 0
}

type Point struct {
	X            int          `json:"x"`
	Y            int          `json:"y"`
	Space        string       `json:"space,omitempty" jsonschema:"screen, screenshot, window, window_frame, or monitor"`
	CoordMap     string       `json:"coord_map,omitempty" jsonschema:"coordinate map returned by a screenshot response; required when space=screenshot"`
	Target       WindowTarget `json:"target,omitzero" jsonschema:"window selector; required when space=window or window_frame"`
	MonitorIndex *int         `json:"monitor_index,omitempty" jsonschema:"zero-based monitor index; required when space=monitor"`
}

// ResolveContext carries the ambient information a coordinate-space
// resolver needs (active window list, monitor list, etc.) when
// translating a non-screen Point into absolute screen coordinates.
//
// Tasks 2-7 will populate these fields. For task-1 the field set is
// declared so downstream resolvers can plug in without further surface
// changes.
type ResolveContext struct {
	// Windows is the snapshot of currently-visible windows. Required
	// when resolving Points whose Space is "window".
	Windows []WindowInfo
	// Monitors is the list of physical monitors in display order.
	// Required when resolving Points whose Space is "monitor".
	Monitors []MonitorInfo
}

// Resolve is the single internal entry point that converts a Point in
// any declared coordinate space into absolute screen coordinates. Every
// click/move/drag/scroll/screenshot handler MUST route coordinate
// translation through this helper — direct use of CoordMap.MapPoint or
// manual arithmetic is a layering violation.
//
// Behavior:
//
//   - space "" or "screen": returns (X, Y) unchanged.
//   - space "screenshot": requires a CoordMap string captured from a
//     prior screenshot response; maps image-space pixels to screen
//     pixels. Preserves v0.1 wire compatibility.
//   - space "window": requires Target and a non-empty
//     ResolveContext.Windows. Translates relative to the matched
//     window's client_bounds (decorations excluded). Coordinates are
//     clamped to client_bounds; out-of-bounds is a validation error.
//   - space "window_frame": same as "window" but uses the matched
//     window's outer screen bounds (decorations included). Intended for
//     clicking custom titlebars in Gio/ImGui apps.
//   - space "monitor": requires MonitorIndex and a non-empty
//     ResolveContext.Monitors. Translates relative to that monitor's
//     bounds; clamped to those bounds.
//
// Any unknown space is a hard validation error.
func Resolve(p Point, rctx ResolveContext) (int, int, error) {
	switch p.Space {
	case "", CoordSpaceScreen:
		return p.X, p.Y, nil
	case CoordSpaceScreenshot:
		if p.CoordMap == "" {
			return 0, 0, Validation("COORD_MAP_REQUIRED", "screenshot-space coordinates require a coord_map", map[string]any{"space": p.Space})
		}
		m, err := ParseCoordMap(p.CoordMap)
		if err != nil {
			return 0, 0, err
		}
		return m.MapPoint(p.X, p.Y)
	case CoordSpaceWindow, CoordSpaceWindowFrame:
		if p.Target.Empty() {
			return 0, 0, Validation("COORD_TARGET_REQUIRED", "window-space coordinates require a target (id, title, class, or pid)", map[string]any{"space": p.Space})
		}
		win, err := matchWindow(rctx.Windows, p.Target)
		if err != nil {
			return 0, 0, err
		}
		origin := win.Bounds
		if p.Space == CoordSpaceWindow {
			// client_bounds preferred; fall back to outer bounds when
			// frame extents are unknown so callers still get a sensible
			// answer.
			if !win.ClientBounds.Empty() {
				origin = win.ClientBounds
			}
		}
		if origin.Empty() {
			return 0, 0, Validation("WINDOW_BOUNDS_UNKNOWN", "matched window has no usable bounds", map[string]any{"target": p.Target, "window_id": win.ID})
		}
		if p.X < 0 || p.Y < 0 || p.X >= origin.Width || p.Y >= origin.Height {
			return 0, 0, Validation("POINT_OUT_OF_BOUNDS", "window-space coordinate is outside the window bounds", map[string]any{
				"space":     p.Space,
				"x":         p.X,
				"y":         p.Y,
				"window_id": win.ID,
				"bounds":    origin,
			})
		}
		return origin.X + p.X, origin.Y + p.Y, nil
	case CoordSpaceMonitor:
		if p.MonitorIndex == nil {
			return 0, 0, Validation("COORD_MONITOR_INDEX_REQUIRED", "monitor-space coordinates require a monitor_index", map[string]any{"space": p.Space})
		}
		idx := *p.MonitorIndex
		if idx < 0 || idx >= len(rctx.Monitors) {
			return 0, 0, Validation("MONITOR_INDEX_OUT_OF_RANGE", "monitor_index is outside the available monitors", map[string]any{
				"monitor_index": idx,
				"monitor_count": len(rctx.Monitors),
			})
		}
		bounds := rctx.Monitors[idx].Bounds
		if bounds.Empty() {
			return 0, 0, Validation("MONITOR_BOUNDS_UNKNOWN", "selected monitor has no usable bounds", map[string]any{"monitor_index": idx})
		}
		if p.X < 0 || p.Y < 0 || p.X >= bounds.Width || p.Y >= bounds.Height {
			return 0, 0, Validation("POINT_OUT_OF_BOUNDS", "monitor-space coordinate is outside the monitor bounds", map[string]any{
				"monitor_index": idx,
				"x":             p.X,
				"y":             p.Y,
				"bounds":        bounds,
			})
		}
		return bounds.X + p.X, bounds.Y + p.Y, nil
	default:
		return 0, 0, Validation("INVALID_COORDINATE_SPACE", "coordinate space must be screen, screenshot, window, window_frame, or monitor", map[string]any{"space": p.Space})
	}
}

// matchWindow finds the single window in candidates that matches the
// target. Returns precondition errors with the candidate list attached
// when the target matches zero or multiple windows. The matcher honors
// declaration order: ID > PID > Class > Title.
func matchWindow(candidates []WindowInfo, target WindowTarget) (WindowInfo, error) {
	if len(candidates) == 0 {
		return WindowInfo{}, Precondition("WINDOW_NOT_FOUND", "no windows were captured in the resolve context", map[string]any{"target": target})
	}
	var hits []WindowInfo
	for _, win := range candidates {
		if MatchWindowInfo(win, target) {
			hits = append(hits, win)
		}
	}
	if len(hits) == 0 {
		return WindowInfo{}, Precondition("WINDOW_NOT_FOUND", "no window matched the target", map[string]any{
			"target":     target,
			"candidates": windowSummaries(candidates),
		})
	}
	if len(hits) > 1 {
		return WindowInfo{}, Precondition("WINDOW_AMBIGUOUS", "target matched multiple windows; tighten the selector", map[string]any{
			"target":     target,
			"candidates": windowSummaries(hits),
		})
	}
	return hits[0], nil
}

// MatchWindowInfo reports whether a window satisfies a WindowTarget.
// Exported so the window package can share the matcher with focus_window
// and the window_* verbs. ID matching is exact (hex or decimal); PID is
// numeric equality; Class is case-insensitive exact; Title is
// case-insensitive substring. If more than one selector is set, all
// non-empty selectors must match (logical AND).
func MatchWindowInfo(win WindowInfo, target WindowTarget) bool {
	if target.Empty() {
		return false
	}
	if target.ID != "" {
		if !equalsWindowID(win, target.ID) {
			return false
		}
	}
	if target.PID != 0 {
		if win.PID != target.PID {
			return false
		}
	}
	if target.Class != "" {
		if !strings.EqualFold(win.Class, target.Class) {
			return false
		}
	}
	if target.Title != "" {
		if !strings.Contains(strings.ToLower(win.Title), strings.ToLower(target.Title)) {
			return false
		}
	}
	return true
}

func equalsWindowID(win WindowInfo, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, win.ID) {
		return true
	}
	base := 10
	value := raw
	if strings.HasPrefix(strings.ToLower(raw), "0x") {
		base = 16
		value = raw[2:]
	}
	parsed, err := strconv.ParseUint(value, base, 32)
	return err == nil && uint32(parsed) == win.XID
}

func windowSummaries(wins []WindowInfo) []map[string]any {
	out := make([]map[string]any, 0, len(wins))
	for _, w := range wins {
		out = append(out, map[string]any{
			"id":    w.ID,
			"title": w.Title,
			"class": w.Class,
			"pid":   w.PID,
		})
	}
	return out
}

// ScreenPoint resolves a Point with an empty ResolveContext. It is a
// thin convenience wrapper around Resolve for call sites that only
// handle the screen and screenshot spaces today. Call sites that need
// to support window or monitor spaces must call Resolve directly with a
// populated ResolveContext.
func (p Point) ScreenPoint() (int, int, error) {
	return Resolve(p, ResolveContext{})
}

type CoordMap struct {
	CaptureX      int `json:"capture_x"`
	CaptureY      int `json:"capture_y"`
	CaptureWidth  int `json:"capture_width"`
	CaptureHeight int `json:"capture_height"`
	ImageWidth    int `json:"image_width"`
	ImageHeight   int `json:"image_height"`
}

func NewCoordMap(capture Bounds, image Size) CoordMap {
	return CoordMap{
		CaptureX:      capture.X,
		CaptureY:      capture.Y,
		CaptureWidth:  capture.Width,
		CaptureHeight: capture.Height,
		ImageWidth:    image.Width,
		ImageHeight:   image.Height,
	}
}

func ParseCoordMap(value string) (CoordMap, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 6 {
		return CoordMap{}, Validation("INVALID_COORD_MAP", "coord_map must contain six comma-separated integers", map[string]any{"coord_map": value})
	}
	ints := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return CoordMap{}, Validation("INVALID_COORD_MAP", "coord_map contains a non-integer value", map[string]any{"coord_map": value, "part": part})
		}
		ints[i] = n
	}
	m := CoordMap{CaptureX: ints[0], CaptureY: ints[1], CaptureWidth: ints[2], CaptureHeight: ints[3], ImageWidth: ints[4], ImageHeight: ints[5]}
	if m.CaptureWidth <= 0 || m.CaptureHeight <= 0 || m.ImageWidth <= 0 || m.ImageHeight <= 0 {
		return CoordMap{}, Validation("INVALID_COORD_MAP", "coord_map dimensions must be positive", map[string]any{"coord_map": value})
	}
	return m, nil
}

func (m CoordMap) String() string {
	return fmt.Sprintf("%d,%d,%d,%d,%d,%d", m.CaptureX, m.CaptureY, m.CaptureWidth, m.CaptureHeight, m.ImageWidth, m.ImageHeight)
}

func (m CoordMap) MapPoint(x, y int) (int, int, error) {
	if x < 0 || y < 0 || x >= m.ImageWidth || y >= m.ImageHeight {
		return 0, 0, Validation("POINT_OUT_OF_BOUNDS", "screenshot coordinate is outside the screenshot image", map[string]any{"x": x, "y": y, "image_width": m.ImageWidth, "image_height": m.ImageHeight})
	}
	sx := m.CaptureX + (x * m.CaptureWidth / m.ImageWidth)
	sy := m.CaptureY + (y * m.CaptureHeight / m.ImageHeight)
	return sx, sy, nil
}

type BackendStatus struct {
	Name      string         `json:"name"`
	Ready     bool           `json:"ready"`
	Required  bool           `json:"required"`
	Message   string         `json:"message,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
	// Capabilities is an optional, backend-defined list of capability
	// tokens (e.g. "read", "set_text" for AT-SPI). Empty for backends
	// that do not advertise capabilities. Added in v0.2; later tasks
	// populate per-backend values.
	Capabilities []string `json:"capabilities,omitempty"`
}

type Readiness struct {
	Status     string   `json:"status"`
	Blockers   []string `json:"blockers,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}

type DoctorReport struct {
	Product   string          `json:"product"`
	Version   VersionInfo     `json:"version"`
	Session   SessionInfo     `json:"session"`
	Readiness Readiness       `json:"readiness"`
	Backends  []BackendStatus `json:"backends"`
	// SchemaVersions enumerates every action-payload schema version this
	// server can accept on inbound computer_actions requests. The list
	// is MANDATORY in doctor output — never empty, never omitted.
	SchemaVersions []string `json:"schema_versions"`
	AvailableTools []string `json:"available_tools"`
}

type SessionInfo struct {
	Display        string `json:"display,omitempty"`
	XAuthority     string `json:"xauthority,omitempty"`
	XDGSessionType string `json:"xdg_session_type,omitempty"`
	WaylandDisplay string `json:"wayland_display,omitempty"`
	Desktop        string `json:"desktop,omitempty"`
	// RespectUser is the resolved value of the --respect-user flag (or
	// MYCOMPUTER_RESPECT_USER env var, or config value). When true, the
	// agent should yield input to the human user when the user is
	// actively interacting with the desktop. Actual yield behavior is
	// implemented in task-6 (see contract.outOfScope for task-1); this
	// field is the declaration that surfaces the resolved setting. The
	// field is MANDATORY in doctor output — always present.
	RespectUser bool `json:"respect_user"`
	// AllowClose is the resolved value of the --allow-close flag. When
	// true, window_close actions issued in this process are permitted;
	// when false (the default), window_close returns
	// PRECONDITION_CLOSE_NOT_ALLOWED. v0.2 keeps this gated; the field
	// is MANDATORY in doctor output — always present.
	AllowClose bool `json:"allow_close"`
	// LogicalCoords is the resolved value of the --logical-coords flag
	// (also env MYCOMPUTER_LOGICAL_COORDS / config logical_coords). When
	// true MyComputer translates input/output coordinates by the primary
	// monitor's RandR scale; off by default. The field is MANDATORY in
	// doctor output — always present.
	LogicalCoords bool `json:"logical_coords"`
}

type VersionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Go      string `json:"go,omitempty"`
}

type ConfigReport struct {
	Product           string            `json:"product"`
	ConfigFiles       []string          `json:"config_files"`
	LoadedConfig      string            `json:"loaded_config,omitempty"`
	Values            map[string]Value  `json:"values"`
	AvailableBackends map[string]string `json:"available_backends"`
}

type Value struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type MonitorInfo struct {
	// Index is the zero-based position of this monitor in the
	// get_screen_info / observe monitor list. Stable for the lifetime
	// of one process call; agents reference it via
	// point.target.monitor_index when point.space="monitor".
	Index int `json:"index"`
	// Name is the RandR monitor name (e.g. "DP-1", "eDP-1"). Empty
	// when RandR is unavailable or the monitor atom is unset.
	Name string `json:"name,omitempty"`
	// Bounds is the monitor rectangle in physical X11 pixels.
	Bounds Bounds `json:"bounds"`
	// Scale is the informational DPI scale factor (96 DPI = 1.0).
	// Always > 0; defaults to 1.0 when RandR does not expose
	// millimeter dimensions. MyComputer always operates in physical
	// pixels — this field exists so agents can translate between
	// app-reported logical coordinates and physical XTest
	// coordinates when needed.
	Scale float64 `json:"scale"`
	// Primary reports whether this monitor is the X primary. Exactly
	// one monitor is primary on a healthy multi-monitor setup;
	// single-monitor systems always report primary:true for the one
	// monitor.
	Primary bool `json:"primary"`
	// RefreshHz is the refresh rate in hertz, rounded to the nearest
	// integer. Zero when the refresh rate cannot be determined.
	RefreshHz int `json:"refresh_hz,omitempty"`
}

type ScreenInfo struct {
	Bounds   Bounds        `json:"bounds"`
	Monitors []MonitorInfo `json:"monitors,omitempty"`
	Backend  string        `json:"backend"`
}

// DecorationInsets describes the WM frame extents around a window.
// Values are zero when _NET_FRAME_EXTENTS is unavailable.
type DecorationInsets struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

// IsZero reports whether all four insets are zero (or negative).
func (d DecorationInsets) IsZero() bool {
	return d.Left <= 0 && d.Top <= 0 && d.Right <= 0 && d.Bottom <= 0
}

type WindowInfo struct {
	ID      string `json:"id"`
	XID     uint32 `json:"xid"`
	Title   string `json:"title,omitempty"`
	Class   string `json:"class,omitempty"`
	PID     uint32 `json:"pid,omitempty"`
	Focused bool   `json:"focused"`
	// Bounds is the outer (frame-inclusive, screen-space) window
	// rectangle. Preserved unchanged from v0.1 for wire compatibility.
	Bounds Bounds `json:"bounds"`
	// ClientBounds is the inner (decoration-excluded, screen-space)
	// rectangle. This is the default origin used when resolving a Point
	// whose Space is "window". When _NET_FRAME_EXTENTS is unavailable
	// the value equals Bounds.
	ClientBounds Bounds `json:"client_bounds"`
	// DecorationInsets is the WM frame inset (left, top, right, bottom)
	// derived from _NET_FRAME_EXTENTS. All zeros when the property is
	// not advertised by the WM (e.g., tiling WMs without decorations).
	DecorationInsets DecorationInsets `json:"decoration_insets"`
}

type ScreenshotResult struct {
	ImagePath     string `json:"image_path"`
	MimeType      string `json:"mime_type"`
	CaptureBounds Bounds `json:"capture_bounds"`
	ImageSize     Size   `json:"image_size"`
	CoordMap      string `json:"coord_map"`
	Backend       string `json:"backend"`
}

type ObserveResult struct {
	Screen        ScreenInfo        `json:"screen"`
	FocusedWindow *WindowInfo       `json:"focused_window,omitempty"`
	Windows       []WindowInfo      `json:"windows,omitempty"`
	Cursor        *Point            `json:"cursor,omitempty"`
	Screenshot    *ScreenshotResult `json:"screenshot,omitempty"`
	Accessibility map[string]any    `json:"accessibility,omitempty"`
	Backends      map[string]string `json:"backends,omitempty"`
}

type ActionResult struct {
	Action  string         `json:"action"`
	OK      bool           `json:"ok"`
	Backend string         `json:"backend,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type BrowserPipelineResult struct {
	BrowserURL string            `json:"browser_url,omitempty"`
	URL        string            `json:"url,omitempty"`
	Title      string            `json:"title,omitempty"`
	Text       string            `json:"text,omitempty"`
	Screenshot string            `json:"screenshot,omitempty"`
	Steps      []ActionResult    `json:"steps"`
	Values     map[string]string `json:"values,omitempty"`
}

// Find-result sources. The set is declared here so task-3 and task-4
// can land find_text, find_template, find_color, and find_atspi
// primitives that return FindResult without further surface changes.
const (
	FindSourceOCR      = "ocr"
	FindSourceTemplate = "template"
	FindSourceColor    = "color"
	FindSourceATSPI    = "atspi"
)

// FindCandidate is one hit returned by a find_* primitive. The shape
// is intentionally minimal: bounds in screen space, a confidence score
// in [0, 1], a source tag, and an open extras bag for source-specific
// detail (e.g., OCR text, template name, AT-SPI element id). Consumed
// by task-3 and task-4.
type FindCandidate struct {
	Bounds     Bounds         `json:"bounds"`
	Confidence float64        `json:"confidence"`
	Source     string         `json:"source" jsonschema:"ocr, template, color, or atspi"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// FindResult is the envelope every find_* primitive returns.
// Candidates are sorted by confidence descending, then top-to-bottom,
// then left-to-right. CoordSpace is always "screen" — find primitives
// translate into screen coordinates before returning so callers can
// hand bounds straight to click/move without further conversion.
type FindResult struct {
	Candidates   []FindCandidate `json:"candidates"`
	SearchRegion Bounds          `json:"search_region"`
	CoordSpace   string          `json:"coord_space" jsonschema:"always screen for find results"`
}
