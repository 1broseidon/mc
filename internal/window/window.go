package window

// Anvil · target: internal/window · kind: package · scope: package
// caller profile: agent,script · surface pattern: package-API (delegating) · risk class: R0
// contracts: List/Focus/Focused/Move/Resize/Raise/Minimize/Maximize/Workspace/Close
//   + VerbResult/VerbWarning + request types + error codes preserved
// obligations: EWMH/ICCCM list+control delegated to platform.Provider.Windows()

import (
	"context"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// Target is the WM-agnostic window selector used by focus_window and
// every window_* verb. It mirrors contract.WindowTarget exactly so
// internal callers can use either name; the alias keeps the wire shape on
// Action.Target and Point.Target unified with the focus_window input.
type Target = contract.WindowTarget

// Geometry tolerance (px) for post-op tiling-WM no-op detection. A
// divergence larger than this between the requested geometry and the
// post-op bounds raises a WINDOW_GEOMETRY_REFUSED warning but does not
// fail the action.
const geometryTolerance = 5

// Post-op settle windows. The service owns the post-op re-read, so the
// "give the WM a chance to apply the request" sleeps live here rather than
// in the platform primitives (which return as soon as the request is in
// flight).
const (
	moveResizeSettle = 30 * time.Millisecond
	workspaceSettle  = 20 * time.Millisecond
)

func List(ctx context.Context) ([]contract.WindowInfo, error) {
	return platform.Current().Windows().List(ctx)
}

func Focus(ctx context.Context, target Target) (contract.WindowInfo, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return contract.WindowInfo{}, err
	}
	if err := platform.Current().Windows().Focus(ctx, platform.NativeIDOf(win)); err != nil {
		return contract.WindowInfo{}, err
	}
	win.Focused = true
	return win, nil
}

func Focused(ctx context.Context) (*contract.WindowInfo, error) {
	windows, err := List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range windows {
		if windows[i].Focused {
			return &windows[i], nil
		}
	}
	return nil, nil
}

// MoveRequest describes a window_move action.
type MoveRequest struct {
	Target Target `json:"target"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

// ResizeRequest describes a window_resize action.
type ResizeRequest struct {
	Target Target `json:"target"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// MaximizeRequest describes a window_maximize action.
type MaximizeRequest struct {
	Target Target `json:"target"`
	Axis   string `json:"axis,omitempty" jsonschema:"both, horz, or vert; defaults to both"`
}

// WorkspaceRequest describes a window_workspace action.
type WorkspaceRequest struct {
	Target Target `json:"target"`
	Index  int    `json:"index"`
}

// VerbResult captures the post-op window plus any warning notes the
// verb produced (e.g. WINDOW_GEOMETRY_REFUSED).
type VerbResult struct {
	Window  contract.WindowInfo `json:"window"`
	Notes   []string            `json:"notes,omitempty"`
	Warning *VerbWarning        `json:"warning,omitempty"`
}

// VerbWarning is a structured non-fatal warning attached to a verb
// result. ok:true on the action; the agent decides recovery.
type VerbWarning struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Move performs window_move. Post-op compares the new outer bounds against
// the request; a divergence beyond geometryTolerance raises
// WINDOW_GEOMETRY_REFUSED.
func Move(ctx context.Context, req MoveRequest) (VerbResult, error) {
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Move(ctx, platform.NativeIDOf(win), req.X, req.Y); err != nil {
		return VerbResult{}, err
	}
	return finishMoveResize(ctx, win.ID, &req.X, &req.Y, nil, nil)
}

// Resize performs window_resize.
func Resize(ctx context.Context, req ResizeRequest) (VerbResult, error) {
	if req.Width <= 0 || req.Height <= 0 {
		return VerbResult{}, contract.Validation("WINDOW_RESIZE_INVALID", "window_resize requires positive width and height", map[string]any{"width": req.Width, "height": req.Height})
	}
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Resize(ctx, platform.NativeIDOf(win), req.Width, req.Height); err != nil {
		return VerbResult{}, err
	}
	return finishMoveResize(ctx, win.ID, nil, nil, &req.Width, &req.Height)
}

// Raise activates and stacks the window above its siblings.
func Raise(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Raise(ctx, platform.NativeIDOf(win)); err != nil {
		return VerbResult{}, err
	}
	updated, _ := refreshByID(ctx, win.ID)
	return VerbResult{Window: updated}, nil
}

// Minimize iconifies the window.
func Minimize(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Minimize(ctx, platform.NativeIDOf(win)); err != nil {
		return VerbResult{}, err
	}
	updated, _ := refreshByID(ctx, win.ID)
	return VerbResult{Window: updated}, nil
}

// Maximize toggles the maximized state on the requested axis (both, horz,
// vert). Defaults to both.
func Maximize(ctx context.Context, req MaximizeRequest) (VerbResult, error) {
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	axis, err := maximizeAxis(req.Axis)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Maximize(ctx, platform.NativeIDOf(win), axis); err != nil {
		return VerbResult{}, err
	}
	// Short settle so the WM can re-publish bounds before the
	// surface-divergence sampler reads them back.
	time.Sleep(moveResizeSettle)
	updated, ok := refreshByID(ctx, win.ID)
	if !ok {
		updated = win
	}
	warning := detectGeometryDivergence(ctx, updated)
	return VerbResult{Window: updated, Warning: warning}, nil
}

// Workspace moves the window to the desktop at the given zero-based index.
func Workspace(ctx context.Context, req WorkspaceRequest) (VerbResult, error) {
	if req.Index < 0 {
		return VerbResult{}, contract.Validation("WORKSPACE_INDEX_NEGATIVE", "window_workspace index must be >= 0", map[string]any{"index": req.Index})
	}
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	id := platform.NativeIDOf(win)
	if err := platform.Current().Windows().Workspace(ctx, id, req.Index); err != nil {
		return VerbResult{}, err
	}
	time.Sleep(workspaceSettle)
	updated, ok := refreshByID(ctx, win.ID)
	if !ok {
		updated = win
	}
	var warning *VerbWarning
	// Honored-check is best-effort: only platforms that expose a
	// per-window workspace index (X11 via WindowWorkspaceReader) can
	// verify the move. Platforms without it skip the check.
	if reader, ok := platform.Current().Windows().(platform.WindowWorkspaceReader); ok {
		if got, gerr := reader.WorkspaceOf(ctx, id); gerr == nil && got >= 0 && got != req.Index {
			warning = &VerbWarning{
				Code:    contract.WindowGeometryRefusedCode,
				Message: "workspace move was not honored by the window manager",
				Details: map[string]any{"requested_index": req.Index, "observed_index": got},
			}
		}
	}
	return VerbResult{Window: updated, Warning: warning}, nil
}

// Close requests window closure. CLI/MCP-layer callers must gate this
// behind --allow-close (or the batch-level allow_close flag); this
// function itself is unconditional so unit tests and audit paths can
// exercise it.
func Close(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	if err := platform.Current().Windows().Close(ctx, platform.NativeIDOf(win)); err != nil {
		return VerbResult{}, err
	}
	return VerbResult{Window: win}, nil
}

// finishMoveResize performs the shared post-op for window_move /
// window_resize: settle, re-read geometry, and decide between a
// WINDOW_GEOMETRY_REFUSED warning (the WM ignored/partially applied the
// request) and a WINDOW_GEOMETRY_DIVERGED warning (the WM applied it but
// the rendered surface didn't follow). The two are mutually exclusive —
// the refusal is the stronger signal and implies the same recovery.
func finishMoveResize(ctx context.Context, id string, x, y, w, h *int) (VerbResult, error) {
	time.Sleep(moveResizeSettle)
	updated, ok := refreshByID(ctx, id)
	if !ok {
		// Post-op read failed; report success with no warning rather than
		// fabricating geometry.
		return VerbResult{}, nil
	}
	var warning *VerbWarning
	if mismatch := geometryMismatch(updated.Bounds, x, y, w, h); mismatch != nil {
		warning = &VerbWarning{
			Code:    contract.WindowGeometryRefusedCode,
			Message: "window manager refused or partially applied the requested geometry",
			Details: mismatch,
		}
	} else if div := detectGeometryDivergence(ctx, updated); div != nil {
		warning = div
	}
	return VerbResult{Window: updated, Warning: warning}, nil
}

// refreshByID re-reads a single window's post-op snapshot by listing and
// matching on the stable ID. Returns ok=false when the window is no longer
// present (e.g. closed) or the list failed.
func refreshByID(ctx context.Context, id string) (contract.WindowInfo, bool) {
	wins, err := List(ctx)
	if err != nil {
		return contract.WindowInfo{}, false
	}
	for _, w := range wins {
		if w.ID == id {
			return w, true
		}
	}
	return contract.WindowInfo{}, false
}

func maximizeAxis(axis string) (platform.MaximizeAxis, error) {
	switch strings.ToLower(strings.TrimSpace(axis)) {
	case "", "both":
		return platform.MaximizeBoth, nil
	case "horz", "horizontal":
		return platform.MaximizeHorizontal, nil
	case "vert", "vertical":
		return platform.MaximizeVertical, nil
	default:
		return 0, contract.Validation("WINDOW_MAXIMIZE_AXIS_INVALID", "window_maximize axis must be both, horz, or vert", map[string]any{"axis": axis})
	}
}

func geometryMismatch(observed contract.Bounds, x, y, w, h *int) map[string]any {
	out := map[string]any{}
	miss := false
	check := func(label string, got int, want *int) {
		if want == nil {
			return
		}
		if abs(got-*want) > geometryTolerance {
			out["requested_"+label] = *want
			out["observed_"+label] = got
			miss = true
		}
	}
	check("x", observed.X, x)
	check("y", observed.Y, y)
	check("width", observed.Width, w)
	check("height", observed.Height, h)
	if !miss {
		return nil
	}
	out["tolerance_px"] = geometryTolerance
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// resolveOne picks a single window for the supplied target via the
// shared contract.MatchWindowInfo matcher. Target-not-found and
// ambiguous-target errors carry the candidate list per the contract.
func resolveOne(ctx context.Context, target Target) (contract.WindowInfo, error) {
	if target.Empty() {
		return contract.WindowInfo{}, contract.Validation("WINDOW_TARGET_REQUIRED", "window operation requires a target (id, title, class, or pid)", nil)
	}
	wins, err := List(ctx)
	if err != nil {
		return contract.WindowInfo{}, err
	}
	if len(wins) == 0 {
		return contract.WindowInfo{}, contract.Precondition("WINDOW_NOT_FOUND", "no top-level windows visible", map[string]any{"target": target})
	}
	var hits []contract.WindowInfo
	for _, w := range wins {
		if contract.MatchWindowInfo(w, target) {
			hits = append(hits, w)
		}
	}
	if len(hits) == 0 {
		return contract.WindowInfo{}, contract.Precondition("WINDOW_NOT_FOUND", "no window matched the target", map[string]any{"target": target, "candidates": summarize(wins)})
	}
	if len(hits) > 1 {
		return contract.WindowInfo{}, contract.Precondition("WINDOW_AMBIGUOUS", "target matched multiple windows; tighten the selector", map[string]any{"target": target, "candidates": summarize(hits)})
	}
	return hits[0], nil
}

func summarize(wins []contract.WindowInfo) []map[string]any {
	out := make([]map[string]any, 0, len(wins))
	for _, w := range wins {
		out = append(out, map[string]any{"id": w.ID, "title": w.Title, "class": w.Class, "pid": w.PID})
	}
	return out
}

// SupportedAtoms reports the EWMH atoms the running WM advertises, keyed
// by name. Used by diagnostic to populate the x11 backend capability
// list. Delegates to the platform window backend's Capabilities (the raw
// _NET_SUPPORTED atom names); platforms without EWMH return an empty map.
func SupportedAtoms(ctx context.Context) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("ewmh probe cancelled")
	}
	names, err := platform.Current().Windows().Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}
