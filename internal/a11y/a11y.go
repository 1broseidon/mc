package a11y

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/1broseidon/mc/internal/contract"
)

const (
	ifaceAccessible   = "org.a11y.atspi.Accessible"
	ifaceComponent    = "org.a11y.atspi.Component"
	ifaceAction       = "org.a11y.atspi.Action"
	ifaceEditableText = "org.a11y.atspi.EditableText"
	ifaceApplication  = "org.a11y.atspi.Application"
	rootBus           = "org.a11y.atspi.Registry"
	rootPath          = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
	// appRootPath is the conventional path of an AT-SPI application's
	// root accessible object; the Application interface (Name,
	// ToolkitName) lives on this object for every well-behaved bus.
	appRootPath = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
)

type ObjectRef struct {
	Bus  string
	Path dbus.ObjectPath
}

type Element struct {
	ID         string          `json:"id"`
	Bus        string          `json:"bus"`
	Path       string          `json:"path"`
	Name       string          `json:"name,omitempty"`
	Role       string          `json:"role,omitempty"`
	Interfaces []string        `json:"interfaces,omitempty"`
	Bounds     contract.Bounds `json:"bounds,omitempty"`
	Depth      int             `json:"depth"`
	Actions    []string        `json:"actions,omitempty"`
	// App is the owning application name resolved from
	// org.a11y.atspi.Application.Name on the bus root, cached once per
	// bus name per observe call. Always present as a string field —
	// empty string when AT-SPI does not expose it, never "?" or null.
	App string `json:"app"`
	// Toolkit is the owning toolkit name resolved from
	// org.a11y.atspi.Application.ToolkitName (e.g. "gtk", "Qt",
	// "Chromium"). Always present; empty when unavailable.
	Toolkit string `json:"toolkit"`
	// WindowID cross-references the X11 window list when the element
	// belongs to a top-level frame whose bounds match a window's
	// outer rectangle. Empty when no correlation can be established;
	// the field is always emitted (omitempty would resurface the
	// "missing key" ambiguity the v0.1 stress test surfaced).
	WindowID string `json:"window_id"`
	// Extra carries source-specific metadata that does not warrant a
	// dedicated typed field. When window_id correlation succeeds,
	// Extra["correlation"] records which strategy matched: one of
	// "bounds", "pid", or "title". Absent (nil map) for elements
	// whose window_id is empty.
	Extra map[string]any `json:"extra,omitempty"`
}

type TreeResult struct {
	Status   string    `json:"status"`
	Message  string    `json:"message,omitempty"`
	Elements []Element `json:"elements,omitempty"`
}

func Probe() contract.BackendStatus {
	now := time.Now().UTC()
	address, err := Address()
	if err != nil {
		return contract.BackendStatus{Name: "at_spi", Ready: false, Required: false, Message: err.Error(), CheckedAt: now}
	}
	return contract.BackendStatus{Name: "at_spi", Ready: address != "", Required: false, Message: "available", Details: map[string]any{"address": address}, CheckedAt: now}
}

func Address() (string, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", fmt.Errorf("session D-Bus unavailable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	obj := conn.Object("org.a11y.Bus", "/org/a11y/bus")
	var address string
	if err := obj.Call("org.a11y.Bus.GetAddress", 0).Store(&address); err != nil {
		return "", fmt.Errorf("AT-SPI bus unavailable: %w", err)
	}
	return address, nil
}

func Connect() (*dbus.Conn, error) {
	address, err := Address()
	if err != nil {
		return nil, contract.Dependency("ATSPI_UNAVAILABLE", "AT-SPI bus is unavailable", map[string]any{"error": err.Error()})
	}
	conn, err := dbus.Dial(address)
	if err != nil {
		return nil, contract.Dependency("ATSPI_CONNECT_FAILED", "failed to connect to AT-SPI bus", map[string]any{"error": err.Error()})
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, contract.Dependency("ATSPI_AUTH_FAILED", "failed to authenticate to AT-SPI bus", map[string]any{"error": err.Error()})
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, contract.Dependency("ATSPI_HELLO_FAILED", "failed to initialize AT-SPI bus connection", map[string]any{"error": err.Error()})
	}
	return conn, nil
}

func Tree(ctx context.Context, maxDepth int) (map[string]any, error) {
	return TreeWithWindows(ctx, maxDepth, nil)
}

// TreeWithWindows is the window-aware variant of Tree. Callers pass the
// current X11 window list so the walker can correlate top-level AT-SPI
// frames to X11 window ids (see CorrelateWindowID for the strategy).
// Passing a nil/empty window list disables correlation; every element's
// WindowID stays empty.
func TreeWithWindows(ctx context.Context, maxDepth int, windows []contract.WindowInfo) (map[string]any, error) {
	tree, err := TreeElementsWithWindows(ctx, maxDepth, 500, windows)
	if err != nil {
		return map[string]any{"status": "unavailable", "message": err.Error()}, nil
	}
	return map[string]any{"status": tree.Status, "message": tree.Message, "elements": tree.Elements}, nil
}

func TreeElements(ctx context.Context, maxDepth int, maxNodes int) (TreeResult, error) {
	return TreeElementsWithWindows(ctx, maxDepth, maxNodes, nil)
}

// TreeElementsWithWindows walks the AT-SPI tree and, when windows is
// non-empty, correlates each top-level frame (and its descendants) to an
// X11 window id via the bounds → pid → title fallback chain.
func TreeElementsWithWindows(ctx context.Context, maxDepth int, maxNodes int, windows []contract.WindowInfo) (TreeResult, error) {
	if err := ctx.Err(); err != nil {
		return TreeResult{}, contract.Cancelled("accessibility tree cancelled")
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxNodes <= 0 {
		maxNodes = 250
	}
	conn, err := Connect()
	if err != nil {
		return TreeResult{}, err
	}
	defer func() { _ = conn.Close() }()
	var out []Element
	seen := map[string]bool{}
	apps := newAppCache()
	pids := newPidCache()
	if err := walkBreadthFirst(ctx, conn, ObjectRef{Bus: rootBus, Path: rootPath}, maxDepth, maxNodes, seen, apps, pids, windows, &out); err != nil {
		return TreeResult{}, err
	}
	return TreeResult{Status: "available", Elements: out}, nil
}

func PerformAction(ctx context.Context, elementID string, actionName string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI action cancelled")
	}
	ref, err := ParseElementID(elementID)
	if err != nil {
		return err
	}
	conn, err := Connect()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	obj := conn.Object(ref.Bus, ref.Path)
	actions, err := actionNames(obj)
	if err != nil {
		return err
	}
	index := 0
	if actionName != "" {
		index = -1
		for i, name := range actions {
			if strings.EqualFold(name, actionName) {
				index = i
				break
			}
		}
		if index < 0 {
			return contract.NotFound("ATSPI_ACTION_NOT_FOUND", "AT-SPI action was not found on element", map[string]any{"element_id": elementID, "action": actionName, "available_actions": actions})
		}
	}
	var ok bool
	if err := obj.Call(ifaceAction+".DoAction", 0, int32(index)).Store(&ok); err != nil {
		return contract.Dependency("ATSPI_ACTION_FAILED", "AT-SPI action call failed", map[string]any{"element_id": elementID, "action": actionName, "error": err.Error()})
	}
	if !ok {
		return contract.Precondition("ATSPI_ACTION_REJECTED", "AT-SPI action returned false", map[string]any{"element_id": elementID, "action": actionName})
	}
	return nil
}

func SetText(ctx context.Context, elementID string, text string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI set text cancelled")
	}
	ref, err := ParseElementID(elementID)
	if err != nil {
		return err
	}
	conn, err := Connect()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	obj := conn.Object(ref.Bus, ref.Path)
	if !hasInterface(obj, ifaceEditableText) {
		return contract.Precondition("ATSPI_EDITABLE_TEXT_UNAVAILABLE", "element does not expose AT-SPI EditableText", map[string]any{"element_id": elementID})
	}
	if call := obj.Call(ifaceEditableText+".SetTextContents", 0, text); call.Err != nil {
		return contract.Dependency("ATSPI_SET_TEXT_FAILED", "AT-SPI SetTextContents failed", map[string]any{"element_id": elementID, "error": call.Err.Error()})
	}
	return nil
}

func walkBreadthFirst(ctx context.Context, conn *dbus.Conn, root ObjectRef, maxDepth int, maxNodes int, seen map[string]bool, apps *appCache, pids *pidCache, windows []contract.WindowInfo, out *[]Element) error {
	type queued struct {
		ref            ObjectRef
		depth          int
		inheritWindow  string
		inheritCorrSrc string
	}
	queue := []queued{{ref: root}}
	for len(queue) > 0 && len(*out) < maxNodes {
		if ctx.Err() != nil {
			return contract.Cancelled("accessibility tree cancelled")
		}
		item := queue[0]
		queue = queue[1:]
		id := ElementID(item.ref)
		if seen[id] {
			continue
		}
		seen[id] = true
		obj := conn.Object(item.ref.Bus, item.ref.Path)
		el := Element{ID: id, Bus: item.ref.Bus, Path: string(item.ref.Path), Depth: item.depth}
		el.Name = stringProp(obj, ifaceAccessible+".Name")
		el.Role = roleName(obj)
		el.Interfaces = interfaces(obj)
		el.Bounds = extentsFromInterfaces(obj, el.Interfaces)
		if contains(el.Interfaces, ifaceAction) {
			el.Actions, _ = actionNamesFromInterfaces(obj, el.Interfaces)
		}
		// Resolve owning application name/toolkit. The Registry root
		// itself does not belong to an application bus — leave both
		// empty rather than emitting "?".
		if item.ref.Bus != rootBus {
			app := apps.lookup(conn, item.ref.Bus)
			el.App = app.name
			el.Toolkit = app.toolkit
		}
		// WindowID correlation: top-level frames trigger a fresh
		// correlation pass against the window list; descendants
		// inherit whatever the frame matched (so a labeled button
		// inside gnome-calculator carries the same window_id as the
		// frame). Gio/ImGui apps don't expose AT-SPI elements at
		// all — they never enter this loop, so they cannot create
		// false positives here.
		winID := item.inheritWindow
		corrSrc := item.inheritCorrSrc
		if winID == "" && item.ref.Bus != rootBus && isFrameRole(el.Role) && len(windows) > 0 {
			winID, corrSrc = CorrelateWindowID(el, windows, func() uint32 {
				return pids.lookup(conn, item.ref.Bus)
			})
		}
		el.WindowID = winID
		if winID != "" && corrSrc != "" {
			el.Extra = map[string]any{"correlation": corrSrc}
		}
		*out = append(*out, el)
		if item.depth >= maxDepth || len(*out) >= maxNodes {
			continue
		}
		var children []ObjectRef
		if err := obj.Call(ifaceAccessible+".GetChildren", 0).Store(&children); err != nil {
			continue
		}
		for _, child := range children {
			queue = append(queue, queued{ref: child, depth: item.depth + 1, inheritWindow: winID, inheritCorrSrc: corrSrc})
		}
	}
	return nil
}

// isFrameRole reports whether an AT-SPI role name marks a top-level
// window-like element worth correlating against the X11 window list.
// AT-SPI exposes top-level windows under role names like "frame"
// (typical GTK), "window" (Qt sometimes), and "dialog" (modal popups).
// Children of these elements inherit the correlation result rather
// than re-running the per-frame match — saves DBus round-trips and
// avoids ambiguous cross-frame matches when a single app has multiple
// top-level windows of nearly identical sizes.
func isFrameRole(role string) bool {
	switch strings.ToLower(role) {
	case "frame", "window", "dialog", "alert":
		return true
	}
	return false
}

// CorrelateWindowID resolves an AT-SPI frame element to an X11 window
// id using a three-strategy fallback chain.
//
//  1. Bounds: top-level frame extents within ~10px of any window's
//     ClientBounds (preferred) or outer Bounds wins. Tightest fit
//     across all candidates is selected so two windows of similar size
//     don't both match.
//  2. PID: AT-SPI bus → process id resolved via the lazy resolvePid
//     closure (DBus GetConnectionUnixProcessID under the hood) is
//     compared against WindowInfo.PID. Skipped silently when the PID
//     lookup fails — many sandboxed/Java apps don't expose a PID.
//  3. Title: lowercase substring match between frame.Name and
//     window.Title. Last-resort because non-unique titles are common.
//
// Returns the matched window's id and the strategy tag, or ("", "")
// when no candidate matched. Gio/ImGui apps never reach this function
// because they don't expose AT-SPI elements at all — the empty-string
// return therefore reflects "no AT-SPI exposure" or "no plausible
// match", never a misclassification.
func CorrelateWindowID(el Element, windows []contract.WindowInfo, resolvePid func() uint32) (string, string) {
	if len(windows) == 0 {
		return "", ""
	}
	// Strategy 1: bounds match. Pick the window with the smallest
	// total |dx|+|dy|+|dw|+|dh| delta within the 10px tolerance.
	if !el.Bounds.Empty() {
		const tol = 10
		bestID := ""
		bestDelta := -1
		for _, w := range windows {
			target := w.ClientBounds
			if target.Empty() {
				target = w.Bounds
			}
			if target.Empty() {
				continue
			}
			dx := absInt(el.Bounds.X - target.X)
			dy := absInt(el.Bounds.Y - target.Y)
			dw := absInt(el.Bounds.Width - target.Width)
			dh := absInt(el.Bounds.Height - target.Height)
			if dx > tol || dy > tol || dw > tol || dh > tol {
				continue
			}
			delta := dx + dy + dw + dh
			if bestDelta < 0 || delta < bestDelta {
				bestDelta = delta
				bestID = w.ID
			}
		}
		if bestID != "" {
			return bestID, "bounds"
		}
	}
	// Strategy 2: PID match. Resolve the AT-SPI bus's PID once and
	// compare with WindowInfo.PID. resolvePid returns 0 when the
	// lookup fails — fall through to title rather than erroring.
	if resolvePid != nil {
		if pid := resolvePid(); pid != 0 {
			for _, w := range windows {
				if w.PID != 0 && w.PID == pid {
					return w.ID, "pid"
				}
			}
		}
	}
	// Strategy 3: title match. Substring (lowercase) between the
	// frame's Name and the window's Title. Requires a non-empty
	// frame name — empty-name frames would match every empty-title
	// window.
	if el.Name != "" {
		needle := strings.ToLower(el.Name)
		for _, w := range windows {
			if w.Title == "" {
				continue
			}
			hay := strings.ToLower(w.Title)
			if strings.Contains(hay, needle) || strings.Contains(needle, hay) {
				return w.ID, "title"
			}
		}
	}
	return "", ""
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// pidCache memoizes the per-bus DBus GetConnectionUnixProcessID lookup
// so a busy AT-SPI tree only pays one round-trip per application bus.
// Entries (including zero-PID negatives) are cached for the lifetime
// of a single observe call. PID 0 means "unknown" — the freedesktop
// DBus daemon never assigns process id 0, so using it as the sentinel
// is unambiguous.
type pidCache struct {
	entries map[string]uint32
}

func newPidCache() *pidCache { return &pidCache{entries: map[string]uint32{}} }

func (c *pidCache) lookup(conn *dbus.Conn, bus string) uint32 {
	if c == nil || bus == "" || bus == rootBus {
		return 0
	}
	if v, ok := c.entries[bus]; ok {
		return v
	}
	pid := resolveBusPid(conn, bus)
	c.entries[bus] = pid
	return pid
}

// resolveBusPid asks the AT-SPI bus daemon for the unix process id
// behind a given bus name. Used by the PID-fallback correlation
// strategy when frame bounds don't match any X11 window. Returns 0
// on any failure — the daemon may refuse the call for sandboxed
// connections (Flatpak/Snap) and that's fine: the caller falls through
// to the title-match strategy.
func resolveBusPid(conn *dbus.Conn, bus string) uint32 {
	obj := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var pid uint32
	if err := obj.Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, bus).Store(&pid); err != nil {
		return 0
	}
	return pid
}

// appInfo caches the resolved Application.Name / ToolkitName for a
// single AT-SPI bus name. Both fields are always strings — empty
// when the app does not expose the Application interface — and
// callers must never re-emit "?" or null in their place.
type appInfo struct {
	name    string
	toolkit string
}

// appCache memoizes per-bus Application.Name and Application.ToolkitName
// lookups within a single observe call. The cache lives for the
// lifetime of TreeElements; reusing it across calls would risk stale
// app metadata after apps restart. Every bus is resolved at most
// once — entries (including empty-string negatives) are cached so a
// busy desktop with thousands of accessible elements still incurs
// exactly one round-trip per bus.
type appCache struct {
	entries map[string]appInfo
}

func newAppCache() *appCache {
	return &appCache{entries: map[string]appInfo{}}
}

func (c *appCache) lookup(conn *dbus.Conn, bus string) appInfo {
	if c == nil {
		return appInfo{}
	}
	if v, ok := c.entries[bus]; ok {
		return v
	}
	info := resolveAppInfo(conn, bus)
	c.entries[bus] = info
	return info
}

// resolveAppInfo issues at most one DBus round-trip per bus to read
// the Application interface's Name and ToolkitName properties from
// the conventional application root object. When Application.Name
// is unset (common for some GTK/Java apps) we fall back to the
// Accessible.Name on the same root object so agents still get a
// usable label. Failures (missing interface, transient errors)
// yield an empty-string appInfo — never a placeholder like "?".
func resolveAppInfo(conn *dbus.Conn, bus string) appInfo {
	if bus == "" || bus == rootBus {
		return appInfo{}
	}
	obj := conn.Object(bus, appRootPath)
	name := stringProp(obj, ifaceApplication+".Name")
	if name == "" {
		name = stringProp(obj, ifaceAccessible+".Name")
	}
	return appInfo{
		name:    name,
		toolkit: stringProp(obj, ifaceApplication+".ToolkitName"),
	}
}

func ElementID(ref ObjectRef) string {
	return ref.Bus + "|" + string(ref.Path)
}

func ParseElementID(id string) (ObjectRef, error) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ObjectRef{}, contract.Validation("INVALID_ELEMENT_ID", "AT-SPI element id must be bus|path", map[string]any{"element_id": id})
	}
	return ObjectRef{Bus: parts[0], Path: dbus.ObjectPath(parts[1])}, nil
}

func stringProp(obj dbus.BusObject, prop string) string {
	v, err := obj.GetProperty(prop)
	if err != nil {
		return ""
	}
	if s, ok := v.Value().(string); ok {
		return s
	}
	return ""
}

func roleName(obj dbus.BusObject) string {
	var role string
	if err := obj.Call(ifaceAccessible+".GetRoleName", 0).Store(&role); err != nil {
		return ""
	}
	return role
}

func interfaces(obj dbus.BusObject) []string {
	var out []string
	if err := obj.Call(ifaceAccessible+".GetInterfaces", 0).Store(&out); err != nil {
		return nil
	}
	return out
}

func extentsFromInterfaces(obj dbus.BusObject, available []string) contract.Bounds {
	if !contains(available, ifaceComponent) {
		return contract.Bounds{}
	}
	var rect struct {
		X      int32
		Y      int32
		Width  int32
		Height int32
	}
	if err := obj.Call(ifaceComponent+".GetExtents", 0, uint32(0)).Store(&rect); err != nil {
		return contract.Bounds{}
	}
	return contract.Bounds{X: int(rect.X), Y: int(rect.Y), Width: int(rect.Width), Height: int(rect.Height)}
}

func actionNames(obj dbus.BusObject) ([]string, error) {
	return actionNamesFromInterfaces(obj, interfaces(obj))
}

func actionNamesFromInterfaces(obj dbus.BusObject, available []string) ([]string, error) {
	if !contains(available, ifaceAction) {
		return nil, contract.Precondition("ATSPI_ACTION_INTERFACE_UNAVAILABLE", "element does not expose AT-SPI Action", nil)
	}
	var n int32
	v, err := obj.GetProperty(ifaceAction + ".NActions")
	if err == nil {
		if value, ok := v.Value().(int32); ok {
			n = value
		}
	}
	var out []string
	for i := int32(0); i < n; i++ {
		var name string
		if err := obj.Call(ifaceAction+".GetName", 0, i).Store(&name); err == nil {
			out = append(out, name)
		}
	}
	return out, nil
}

func hasInterface(obj dbus.BusObject, want string) bool {
	return contains(interfaces(obj), want)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
