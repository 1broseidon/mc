//go:build linux

package x11adapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// windowManager implements platform.WindowManager (+ the optional
// platform.WindowWorkspaceReader) over EWMH/ICCCM. It owns only the raw
// X11 protocol: enumeration, property reads, and the client-message
// senders. Target matching, ambiguity handling, post-op geometry
// divergence detection, and warning construction remain in the window
// service so those contracts stay identical across platforms.
//
// The control verbs are fire-and-send: they issue the EWMH client message
// and Sync, but do NOT re-read geometry or build warnings — the service
// re-lists and decides. The short post-send settle sleeps that used to
// live in the verbs move with the service (it owns the post-op read), so
// the adapter primitives return as soon as the request is in flight.
type windowManager struct{}

// EWMH state-action selectors and source indication.
const (
	netWMStateAdd           = 1
	moveResizeSourcePager   = 2
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

func (windowManager) List(ctx context.Context) ([]contract.WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("window list cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	atomMap, err := atoms(d.Conn)
	if err != nil {
		return nil, err
	}
	active, _ := activeWindow(d.Conn, d.Screen.Root, atomMap)
	ids, err := clientList(d.Conn, d.Screen.Root, atomMap)
	if err != nil {
		return nil, err
	}
	windows := make([]contract.WindowInfo, 0, len(ids))
	for _, id := range ids {
		info, err := infoFor(d.Conn, d.Screen.Root, id, atomMap, active)
		if err == nil {
			windows = append(windows, info)
		}
	}
	return windows, nil
}

func (windowManager) Focus(ctx context.Context, id platform.NativeID) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		data := xproto.ClientMessageDataUnionData32New([]uint32{1, xproto.TimeCurrentTime, 0, 0, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_ACTIVE_WINDOW"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		xproto.ConfigureWindow(d.Conn, xid, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
		xproto.SetInputFocus(d.Conn, xproto.InputFocusPointerRoot, xid, xproto.TimeCurrentTime)
		d.Conn.Sync()
		return nil
	})
}

func (windowManager) Raise(ctx context.Context, id platform.NativeID) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		data := xproto.ClientMessageDataUnionData32New([]uint32{moveResizeSourcePager, xproto.TimeCurrentTime, 0, 0, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_ACTIVE_WINDOW"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		xproto.ConfigureWindow(d.Conn, xid, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
		d.Conn.Sync()
		return nil
	})
}

func (windowManager) Minimize(ctx context.Context, id platform.NativeID) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		// 3 = IconicState per ICCCM 4.1.4.
		data := xproto.ClientMessageDataUnionData32New([]uint32{3, 0, 0, 0, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["WM_CHANGE_STATE"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		d.Conn.Sync()
		return nil
	})
}

func (windowManager) Maximize(ctx context.Context, id platform.NativeID, axis platform.MaximizeAxis) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		var first, second xproto.Atom
		switch axis {
		case platform.MaximizeBoth:
			first = atomMap["_NET_WM_STATE_MAXIMIZED_HORZ"]
			second = atomMap["_NET_WM_STATE_MAXIMIZED_VERT"]
		case platform.MaximizeHorizontal:
			first = atomMap["_NET_WM_STATE_MAXIMIZED_HORZ"]
		case platform.MaximizeVertical:
			first = atomMap["_NET_WM_STATE_MAXIMIZED_VERT"]
		}
		data := xproto.ClientMessageDataUnionData32New([]uint32{netWMStateAdd, uint32(first), uint32(second), moveResizeSourcePager, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_WM_STATE"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		d.Conn.Sync()
		return nil
	})
}

func (windowManager) Move(ctx context.Context, id platform.NativeID, x, y int) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		return sendMoveResize(d, atomMap, xid, &x, &y, nil, nil)
	})
}

func (windowManager) Resize(ctx context.Context, id platform.NativeID, w, h int) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		return sendMoveResize(d, atomMap, xid, nil, nil, &w, &h)
	})
}

func (windowManager) Workspace(ctx context.Context, id platform.NativeID, index int) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		data := xproto.ClientMessageDataUnionData32New([]uint32{uint32(index), moveResizeSourcePager, 0, 0, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_WM_DESKTOP"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		d.Conn.Sync()
		return nil
	})
}

func (windowManager) Close(ctx context.Context, id platform.NativeID) error {
	return withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		data := xproto.ClientMessageDataUnionData32New([]uint32{xproto.TimeCurrentTime, moveResizeSourcePager, 0, 0, 0})
		event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_CLOSE_WINDOW"], Data: data}
		xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
		d.Conn.Sync()
		return nil
	})
}

// Capabilities reads _NET_SUPPORTED from the root window and returns the
// raw atom names the WM advertises. The diagnostic layer maps these onto
// short ewmh.* capability tokens.
func (windowManager) Capabilities(ctx context.Context) ([]string, error) {
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
	var out []string
	for i := 0; i+3 < len(reply.Value); i += 4 {
		atom := xproto.Atom(xgb.Get32(reply.Value[i:]))
		name, err := xproto.GetAtomName(d.Conn, atom).Reply()
		if err == nil {
			out = append(out, name.Name)
		}
	}
	return out, nil
}

// Workspace implements platform.WindowWorkspaceReader: it reads the
// window's current _NET_WM_DESKTOP index, returning -1 when unknown.
func (windowManager) WorkspaceOf(ctx context.Context, id platform.NativeID) (int, error) {
	var got int
	err := withWindow(ctx, id, func(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window) error {
		got = readDesktop(d.Conn, xid, atomMap)
		return nil
	})
	if err != nil {
		return -1, err
	}
	return got, nil
}

// withWindow opens a short-lived X11 connection, interns the EWMH atom
// set, and invokes fn with the resolved window id. Centralizes the
// per-verb connection/atom boilerplate.
func withWindow(ctx context.Context, id platform.NativeID, fn func(*x11.Display, map[string]xproto.Atom, xproto.Window) error) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("window operation cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return err
	}
	defer d.Close()
	atomMap, err := atoms(d.Conn)
	if err != nil {
		return err
	}
	return fn(d, atomMap, xproto.Window(uint32(id.Raw)))
}

func sendMoveResize(d *x11.Display, atomMap map[string]xproto.Atom, xid xproto.Window, x, y, w, h *int) error {
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
	data := xproto.ClientMessageDataUnionData32New([]uint32{flags, xv, yv, wv, hv})
	event := xproto.ClientMessageEvent{Format: 32, Window: xid, Type: atomMap["_NET_MOVERESIZE_WINDOW"], Data: data}
	xproto.SendEvent(d.Conn, false, d.Screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
	d.Conn.Sync()
	return nil
}

// --- EWMH/ICCCM property readers (migrated from internal/window) ---

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

func clientList(conn *xgb.Conn, root xproto.Window, atomMap map[string]xproto.Atom) ([]xproto.Window, error) {
	reply, err := xproto.GetProperty(conn, false, root, atomMap["_NET_CLIENT_LIST"], xproto.AtomWindow, 0, 1<<20).Reply()
	if err != nil {
		return nil, contract.Dependency("WINDOW_LIST_FAILED", "failed to query _NET_CLIENT_LIST", map[string]any{"error": err.Error()})
	}
	var ids []xproto.Window
	for i := 0; i+3 < len(reply.Value); i += 4 {
		ids = append(ids, xproto.Window(xgb.Get32(reply.Value[i:])))
	}
	return ids, nil
}

func activeWindow(conn *xgb.Conn, root xproto.Window, atomMap map[string]xproto.Atom) (xproto.Window, error) {
	reply, err := xproto.GetProperty(conn, false, root, atomMap["_NET_ACTIVE_WINDOW"], xproto.AtomWindow, 0, 1).Reply()
	if err != nil || len(reply.Value) < 4 {
		return 0, err
	}
	return xproto.Window(xgb.Get32(reply.Value)), nil
}

func infoFor(conn *xgb.Conn, root xproto.Window, id xproto.Window, atomMap map[string]xproto.Atom, active xproto.Window) (contract.WindowInfo, error) {
	geom, err := xproto.GetGeometry(conn, xproto.Drawable(id)).Reply()
	if err != nil {
		return contract.WindowInfo{}, err
	}
	translated, err := xproto.TranslateCoordinates(conn, id, root, 0, 0).Reply()
	x, y := int(geom.X), int(geom.Y)
	if err == nil {
		x, y = int(translated.DstX), int(translated.DstY)
	}
	title := stringProperty(conn, id, atomMap["_NET_WM_NAME"], atomMap["UTF8_STRING"])
	if title == "" {
		title = stringProperty(conn, id, xproto.AtomWmName, xproto.AtomString)
	}
	class := wmClass(conn, id)
	pid := cardinalProperty(conn, id, atomMap["_NET_WM_PID"])
	bounds := contract.Bounds{X: x, Y: y, Width: int(geom.Width), Height: int(geom.Height)}
	insets := frameExtents(conn, id, atomMap)
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

func frameExtents(conn *xgb.Conn, win xproto.Window, atomMap map[string]xproto.Atom) contract.DecorationInsets {
	reply, err := xproto.GetProperty(conn, false, win, atomMap["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, 0, 4).Reply()
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

func readDesktop(conn *xgb.Conn, win xproto.Window, atomMap map[string]xproto.Atom) int {
	reply, err := xproto.GetProperty(conn, false, win, atomMap["_NET_WM_DESKTOP"], xproto.AtomCardinal, 0, 1).Reply()
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
