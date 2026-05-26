package window

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/x11"
)

// Target is the WM-agnostic window selector used by focus_window and
// every window_* verb. It mirrors contract.WindowTarget exactly so
// internal callers can use either name; the alias keeps task-1's wire
// shape on Action.Target and Point.Target unified with the focus_window
// input.
type Target = contract.WindowTarget

// Geometry tolerance (px) for post-op tiling-WM no-op detection. A
// divergence larger than this between the requested geometry and the
// post-op bounds raises a WINDOW_GEOMETRY_REFUSED warning but does not
// fail the action.
const geometryTolerance = 5

// EWMH state action selectors for _NET_WM_STATE client messages.
const (
	netWMStateRemove = 0
	netWMStateAdd    = 1
	// _NET_MOVERESIZE_WINDOW source: 2 = pager/agent.
	moveResizeSourcePager = 2
	// moveResizeFlagsXYWH sets gravity=static (0) plus the four
	// X/Y/W/H present bits (8..11) plus source bits (12..13 = pager).
	// Layout per EWMH 1.5 §"_NET_MOVERESIZE_WINDOW".
	moveResizeGravityStatic = 0
)

var ewmhAtomNames = []string{
	"_NET_CLIENT_LIST",
	"_NET_WM_NAME",
	"UTF8_STRING",
	"_NET_WM_PID",
	"_NET_ACTIVE_WINDOW",
	"_NET_FRAME_EXTENTS",
	"_NET_MOVERESIZE_WINDOW",
	"_NET_RESTACK_WINDOW",
	"_NET_WM_STATE",
	"_NET_WM_STATE_HIDDEN",
	"_NET_WM_STATE_MAXIMIZED_HORZ",
	"_NET_WM_STATE_MAXIMIZED_VERT",
	"_NET_WM_DESKTOP",
	"_NET_CURRENT_DESKTOP",
	"_NET_CLOSE_WINDOW",
	"_NET_SUPPORTED",
	"WM_CHANGE_STATE",
}

func List(ctx context.Context) ([]contract.WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("window list cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return nil, err
	}
	active, _ := activeWindow(d.Conn, d.Screen.Root, atoms)
	ids, err := clientList(d.Conn, d.Screen.Root, atoms)
	if err != nil {
		return nil, err
	}
	windows := make([]contract.WindowInfo, 0, len(ids))
	for _, id := range ids {
		info, err := infoFor(d.Conn, d.Screen.Root, id, atoms, active)
		if err == nil {
			windows = append(windows, info)
		}
	}
	return windows, nil
}

func Focus(ctx context.Context, target Target) (contract.WindowInfo, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return contract.WindowInfo{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return contract.WindowInfo{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return contract.WindowInfo{}, err
	}
	xid := xproto.Window(win.XID)
	data := xproto.ClientMessageDataUnionData32New([]uint32{1, xproto.TimeCurrentTime, 0, 0, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_ACTIVE_WINDOW"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	xproto.ConfigureWindow(d.Conn, xid, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	xproto.SetInputFocus(d.Conn, xproto.InputFocusPointerRoot, xid, xproto.TimeCurrentTime)
	d.Conn.Sync()
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

// Move performs window_move via _NET_MOVERESIZE_WINDOW with static
// gravity. Post-op compares the new outer bounds against the request;
// a divergence beyond geometryTolerance raises WINDOW_GEOMETRY_REFUSED.
func Move(ctx context.Context, req MoveRequest) (VerbResult, error) {
	return runMoveResize(ctx, req.Target, &req.X, &req.Y, nil, nil, "window_move")
}

// Resize performs window_resize via _NET_MOVERESIZE_WINDOW.
func Resize(ctx context.Context, req ResizeRequest) (VerbResult, error) {
	if req.Width <= 0 || req.Height <= 0 {
		return VerbResult{}, contract.Validation("WINDOW_RESIZE_INVALID", "window_resize requires positive width and height", map[string]any{"width": req.Width, "height": req.Height})
	}
	return runMoveResize(ctx, req.Target, nil, nil, &req.Width, &req.Height, "window_resize")
}

// Raise activates and stacks the window above its siblings. Uses
// _NET_ACTIVE_WINDOW then falls back to ConfigureWindow+stack-above.
func Raise(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	xid := xproto.Window(win.XID)
	data := xproto.ClientMessageDataUnionData32New([]uint32{moveResizeSourcePager, xproto.TimeCurrentTime, 0, 0, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_ACTIVE_WINDOW"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	// Stack-above fallback for WMs that ignore the EWMH client message.
	xproto.ConfigureWindow(d.Conn, xid, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	d.Conn.Sync()
	updated, _ := refreshInfo(d.Conn, d.Screen.Root, xid, atoms)
	return VerbResult{Window: updated}, nil
}

// Minimize iconifies the window via WM_CHANGE_STATE (IconicState=3).
// EWMH's _NET_WM_STATE_HIDDEN is read-only per spec, so the legacy
// WM_CHANGE_STATE client message is the correct hammer.
func Minimize(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	xid := xproto.Window(win.XID)
	// 3 = IconicState per ICCCM 4.1.4.
	data := xproto.ClientMessageDataUnionData32New([]uint32{3, 0, 0, 0, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["WM_CHANGE_STATE"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	updated, _ := refreshInfo(d.Conn, d.Screen.Root, xid, atoms)
	return VerbResult{Window: updated}, nil
}

// Maximize toggles the maximized state on the requested axis (both,
// horz, vert). Defaults to both.
func Maximize(ctx context.Context, req MaximizeRequest) (VerbResult, error) {
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	axis := strings.ToLower(strings.TrimSpace(req.Axis))
	if axis == "" {
		axis = "both"
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	xid := xproto.Window(win.XID)
	var first, second xproto.Atom
	switch axis {
	case "both":
		first = atoms["_NET_WM_STATE_MAXIMIZED_HORZ"]
		second = atoms["_NET_WM_STATE_MAXIMIZED_VERT"]
	case "horz", "horizontal":
		first = atoms["_NET_WM_STATE_MAXIMIZED_HORZ"]
	case "vert", "vertical":
		first = atoms["_NET_WM_STATE_MAXIMIZED_VERT"]
	default:
		return VerbResult{}, contract.Validation("WINDOW_MAXIMIZE_AXIS_INVALID", "window_maximize axis must be both, horz, or vert", map[string]any{"axis": req.Axis})
	}
	data := xproto.ClientMessageDataUnionData32New([]uint32{netWMStateAdd, uint32(first), uint32(second), moveResizeSourcePager, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_WM_STATE"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	updated, _ := refreshInfo(d.Conn, d.Screen.Root, xid, atoms)
	return VerbResult{Window: updated}, nil
}

// Workspace moves the window to the desktop at the given zero-based
// index via _NET_WM_DESKTOP.
func Workspace(ctx context.Context, req WorkspaceRequest) (VerbResult, error) {
	if req.Index < 0 {
		return VerbResult{}, contract.Validation("WORKSPACE_INDEX_NEGATIVE", "window_workspace index must be >= 0", map[string]any{"index": req.Index})
	}
	win, err := resolveOne(ctx, req.Target)
	if err != nil {
		return VerbResult{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	xid := xproto.Window(win.XID)
	data := xproto.ClientMessageDataUnionData32New([]uint32{uint32(req.Index), moveResizeSourcePager, 0, 0, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_WM_DESKTOP"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	// Best-effort: short settle window before re-reading state.
	time.Sleep(20 * time.Millisecond)
	updated, _ := refreshInfo(d.Conn, d.Screen.Root, xid, atoms)
	var warning *VerbWarning
	if got := readDesktop(d.Conn, xid, atoms); got >= 0 && got != req.Index {
		warning = &VerbWarning{
			Code:    contract.WindowGeometryRefusedCode,
			Message: "workspace move was not honored by the window manager",
			Details: map[string]any{"requested_index": req.Index, "observed_index": got},
		}
	}
	return VerbResult{Window: updated, Warning: warning}, nil
}

// Close requests window closure via _NET_CLOSE_WINDOW. CLI/MCP-layer
// callers must gate this behind --allow-close (or the batch-level
// allow_close flag); this function itself is unconditional so unit
// tests and audit paths can exercise it.
func Close(ctx context.Context, target Target) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	xid := xproto.Window(win.XID)
	data := xproto.ClientMessageDataUnionData32New([]uint32{xproto.TimeCurrentTime, moveResizeSourcePager, 0, 0, 0})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_CLOSE_WINDOW"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	return VerbResult{Window: win}, nil
}

// runMoveResize issues a single _NET_MOVERESIZE_WINDOW client message
// honoring the supplied X/Y/W/H pointers (nil means "leave alone") and
// then re-reads geometry to detect tiling-WM refusal.
func runMoveResize(ctx context.Context, target Target, x, y *int, w, h *int, action string) (VerbResult, error) {
	win, err := resolveOne(ctx, target)
	if err != nil {
		return VerbResult{}, err
	}
	d, err := x11.Open()
	if err != nil {
		return VerbResult{}, err
	}
	defer d.Close()
	atoms, err := atoms(d.Conn)
	if err != nil {
		return VerbResult{}, err
	}
	flags := uint32(moveResizeGravityStatic) | (uint32(moveResizeSourcePager) << 12)
	if x != nil {
		flags |= 1 << 8
	}
	if y != nil {
		flags |= 1 << 9
	}
	if w != nil {
		flags |= 1 << 10
	}
	if h != nil {
		flags |= 1 << 11
	}
	xv, yv, wv, hv := uint32(0), uint32(0), uint32(0), uint32(0)
	if x != nil {
		xv = uint32(int32(*x))
	}
	if y != nil {
		yv = uint32(int32(*y))
	}
	if w != nil {
		wv = uint32(*w)
	}
	if h != nil {
		hv = uint32(*h)
	}
	xid := xproto.Window(win.XID)
	data := xproto.ClientMessageDataUnionData32New([]uint32{flags, xv, yv, wv, hv})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atoms["_NET_MOVERESIZE_WINDOW"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	// Short settle to give the WM a chance to apply the request before
	// we re-read geometry. Tiling WMs typically ignore the message
	// entirely; non-tiling WMs apply it synchronously enough that 30ms
	// is sufficient for the post-check.
	time.Sleep(30 * time.Millisecond)
	updated, _ := refreshInfo(d.Conn, d.Screen.Root, xid, atoms)
	var warning *VerbWarning
	if mismatch := geometryMismatch(updated.Bounds, x, y, w, h); mismatch != nil {
		warning = &VerbWarning{
			Code:    contract.WindowGeometryRefusedCode,
			Message: "window manager refused or partially applied the requested geometry",
			Details: mismatch,
		}
	}
	_ = ctx
	_ = action
	return VerbResult{Window: updated, Warning: warning}, nil
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

// SupportedAtoms reads _NET_SUPPORTED from the root window and returns
// the set of atom names the WM advertises. Used by diagnostic.x11 to
// populate the EWMH capability list.
func SupportedAtoms(ctx context.Context) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("ewmh probe cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	supportedAtom, err := x11.InternAtom(d.Conn, "_NET_SUPPORTED")
	if err != nil {
		return nil, contract.Dependency("ATOM_LOOKUP_FAILED", "failed to intern _NET_SUPPORTED", map[string]any{"error": err.Error()})
	}
	reply, err := xproto.GetProperty(d.Conn, false, d.Screen.Root, supportedAtom, xproto.AtomAtom, 0, 1<<16).Reply()
	if err != nil {
		return nil, contract.Dependency("EWMH_PROBE_FAILED", "failed to read _NET_SUPPORTED", map[string]any{"error": err.Error()})
	}
	out := map[string]bool{}
	for i := 0; i+3 < len(reply.Value); i += 4 {
		atom := xproto.Atom(xgb.Get32(reply.Value[i:]))
		name, err := xproto.GetAtomName(d.Conn, atom).Reply()
		if err == nil {
			out[name.Name] = true
		}
	}
	return out, nil
}

func atoms(conn *xgb.Conn) (map[string]xproto.Atom, error) {
	out := map[string]xproto.Atom{}
	for _, name := range ewmhAtomNames {
		atom, err := x11.InternAtom(conn, name)
		if err != nil {
			return nil, contract.Dependency("ATOM_LOOKUP_FAILED", "failed to intern X11 atom", map[string]any{"atom": name, "error": err.Error()})
		}
		out[name] = atom
	}
	return out, nil
}

func clientList(conn *xgb.Conn, root xproto.Window, atoms map[string]xproto.Atom) ([]xproto.Window, error) {
	reply, err := xproto.GetProperty(conn, false, root, atoms["_NET_CLIENT_LIST"], xproto.AtomWindow, 0, 1<<20).Reply()
	if err != nil {
		return nil, contract.Dependency("WINDOW_LIST_FAILED", "failed to query _NET_CLIENT_LIST", map[string]any{"error": err.Error()})
	}
	var ids []xproto.Window
	for i := 0; i+3 < len(reply.Value); i += 4 {
		ids = append(ids, xproto.Window(xgb.Get32(reply.Value[i:])))
	}
	return ids, nil
}

func activeWindow(conn *xgb.Conn, root xproto.Window, atoms map[string]xproto.Atom) (xproto.Window, error) {
	reply, err := xproto.GetProperty(conn, false, root, atoms["_NET_ACTIVE_WINDOW"], xproto.AtomWindow, 0, 1).Reply()
	if err != nil || len(reply.Value) < 4 {
		return 0, err
	}
	return xproto.Window(xgb.Get32(reply.Value)), nil
}

func infoFor(conn *xgb.Conn, root xproto.Window, id xproto.Window, atoms map[string]xproto.Atom, active xproto.Window) (contract.WindowInfo, error) {
	geom, err := xproto.GetGeometry(conn, xproto.Drawable(id)).Reply()
	if err != nil {
		return contract.WindowInfo{}, err
	}
	translated, err := xproto.TranslateCoordinates(conn, id, root, 0, 0).Reply()
	x, y := int(geom.X), int(geom.Y)
	if err == nil {
		x, y = int(translated.DstX), int(translated.DstY)
	}
	title := stringProperty(conn, id, atoms["_NET_WM_NAME"], atoms["UTF8_STRING"])
	if title == "" {
		title = stringProperty(conn, id, xproto.AtomWmName, xproto.AtomString)
	}
	class := wmClass(conn, id)
	pid := cardinalProperty(conn, id, atoms["_NET_WM_PID"])
	bounds := contract.Bounds{X: x, Y: y, Width: int(geom.Width), Height: int(geom.Height)}
	insets := frameExtents(conn, id, atoms)
	client := clientBounds(bounds, insets)
	return contract.WindowInfo{
		ID:               fmt.Sprintf("0x%x", uint32(id)),
		XID:              uint32(id),
		Title:            title,
		Class:            class,
		PID:              pid,
		Focused:          id == active,
		Bounds:           bounds,
		ClientBounds:     client,
		DecorationInsets: insets,
	}, nil
}

// refreshInfo re-reads a single window's info after a mutating verb so
// callers can return the post-op snapshot.
func refreshInfo(conn *xgb.Conn, root xproto.Window, id xproto.Window, atoms map[string]xproto.Atom) (contract.WindowInfo, error) {
	active, _ := activeWindow(conn, root, atoms)
	return infoFor(conn, root, id, atoms, active)
}

func frameExtents(conn *xgb.Conn, win xproto.Window, atoms map[string]xproto.Atom) contract.DecorationInsets {
	reply, err := xproto.GetProperty(conn, false, win, atoms["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, 0, 4).Reply()
	if err != nil || len(reply.Value) < 16 {
		return contract.DecorationInsets{}
	}
	return contract.DecorationInsets{
		Left:   int(int32(xgb.Get32(reply.Value[0:]))),
		Right:  int(int32(xgb.Get32(reply.Value[4:]))),
		Top:    int(int32(xgb.Get32(reply.Value[8:]))),
		Bottom: int(int32(xgb.Get32(reply.Value[12:]))),
	}
}

// clientBounds computes the decoration-excluded rectangle. When
// _NET_FRAME_EXTENTS is unavailable (all zero) the result equals the
// outer bounds so callers always have a usable origin.
func clientBounds(outer contract.Bounds, insets contract.DecorationInsets) contract.Bounds {
	if insets.IsZero() {
		return outer
	}
	w := outer.Width - insets.Left - insets.Right
	h := outer.Height - insets.Top - insets.Bottom
	if w <= 0 || h <= 0 {
		return outer
	}
	return contract.Bounds{
		X:      outer.X + insets.Left,
		Y:      outer.Y + insets.Top,
		Width:  w,
		Height: h,
	}
}

func readDesktop(conn *xgb.Conn, win xproto.Window, atoms map[string]xproto.Atom) int {
	reply, err := xproto.GetProperty(conn, false, win, atoms["_NET_WM_DESKTOP"], xproto.AtomCardinal, 0, 1).Reply()
	if err != nil || len(reply.Value) < 4 {
		return -1
	}
	return int(int32(xgb.Get32(reply.Value)))
}

func stringProperty(conn *xgb.Conn, win xproto.Window, property, typ xproto.Atom) string {
	reply, err := xproto.GetProperty(conn, false, win, property, typ, 0, 1<<16).Reply()
	if err != nil || len(reply.Value) == 0 {
		return ""
	}
	return strings.TrimRight(string(reply.Value), "\x00")
}

func wmClass(conn *xgb.Conn, win xproto.Window) string {
	reply, err := xproto.GetProperty(conn, false, win, xproto.AtomWmClass, xproto.AtomString, 0, 1<<16).Reply()
	if err != nil || len(reply.Value) == 0 {
		return ""
	}
	parts := strings.Split(strings.TrimRight(string(reply.Value), "\x00"), "\x00")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func cardinalProperty(conn *xgb.Conn, win xproto.Window, property xproto.Atom) uint32 {
	reply, err := xproto.GetProperty(conn, false, win, property, xproto.AtomCardinal, 0, 1).Reply()
	if err != nil || len(reply.Value) < 4 {
		return 0
	}
	return xgb.Get32(reply.Value)
}
