//go:build linux

package x11adapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
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

type x11Accessibility struct{}

type a11yObjectRef struct {
	Bus  string
	Path dbus.ObjectPath
}

func (x11Accessibility) Tree(ctx context.Context, maxDepth, maxNodes int) ([]platform.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("accessibility tree cancelled")
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxNodes <= 0 {
		maxNodes = 250
	}
	conn, err := connectATSPI()
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	seen := map[string]bool{}
	apps := newAppCache()
	pids := newPidCache()
	out := make([]platform.Node, 0, min(maxNodes, 64))
	if err := walkAccessibility(ctx, conn, a11yObjectRef{Bus: rootBus, Path: rootPath}, "", maxDepth, maxNodes, seen, apps, pids, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (x11Accessibility) PerformAction(ctx context.Context, nodeID string, actionName string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI action cancelled")
	}
	ref, err := parseA11yNodeID(nodeID)
	if err != nil {
		return err
	}
	conn, err := connectATSPI()
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
			return contract.NotFound("ATSPI_ACTION_NOT_FOUND", "AT-SPI action was not found on element", map[string]any{"element_id": nodeID, "action": actionName, "available_actions": actions})
		}
	}
	var ok bool
	if err := obj.Call(ifaceAction+".DoAction", 0, int32(index)).Store(&ok); err != nil {
		return contract.Dependency("ATSPI_ACTION_FAILED", "AT-SPI action call failed", map[string]any{"element_id": nodeID, "action": actionName, "error": err.Error()})
	}
	if !ok {
		return contract.Precondition("ATSPI_ACTION_REJECTED", "AT-SPI action returned false", map[string]any{"element_id": nodeID, "action": actionName})
	}
	return nil
}

func (x11Accessibility) SetText(ctx context.Context, nodeID string, text string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI set text cancelled")
	}
	ref, err := parseA11yNodeID(nodeID)
	if err != nil {
		return err
	}
	conn, err := connectATSPI()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	obj := conn.Object(ref.Bus, ref.Path)
	if !hasInterface(obj, ifaceEditableText) {
		return contract.Precondition("ATSPI_EDITABLE_TEXT_UNAVAILABLE", "element does not expose AT-SPI EditableText", map[string]any{"element_id": nodeID})
	}
	if call := obj.Call(ifaceEditableText+".SetTextContents", 0, text); call.Err != nil {
		return contract.Dependency("ATSPI_SET_TEXT_FAILED", "AT-SPI SetTextContents failed", map[string]any{"element_id": nodeID, "error": call.Err.Error()})
	}
	return nil
}

func atspiAddress() (string, error) {
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

func connectATSPI() (*dbus.Conn, error) {
	address, err := atspiAddress()
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

func walkAccessibility(ctx context.Context, conn *dbus.Conn, root a11yObjectRef, parentID string, maxDepth, maxNodes int, seen map[string]bool, apps *appCache, pids *pidCache, out *[]platform.Node) error {
	type queued struct {
		ref      a11yObjectRef
		parentID string
		depth    int
	}
	queue := []queued{{ref: root}}
	for len(queue) > 0 && len(*out) < maxNodes {
		if ctx.Err() != nil {
			return contract.Cancelled("accessibility tree cancelled")
		}
		item := queue[0]
		queue = queue[1:]
		id := a11yNodeID(item.ref)
		if seen[id] {
			continue
		}
		seen[id] = true
		obj := conn.Object(item.ref.Bus, item.ref.Path)
		node := platform.Node{
			ID:         id,
			ParentID:   item.parentID,
			Depth:      item.depth,
			Name:       stringProp(obj, ifaceAccessible+".Name"),
			Role:       roleName(obj),
			Interfaces: interfaces(obj),
		}
		node.Bounds = extentsFromInterfaces(obj, node.Interfaces)
		if contains(node.Interfaces, ifaceAction) {
			node.Actions, _ = actionNamesFromInterfaces(obj, node.Interfaces)
		}
		if item.ref.Bus != rootBus {
			app := apps.lookup(conn, item.ref.Bus)
			node.App = app.name
			node.Toolkit = app.toolkit
			node.PID = pids.lookup(conn, item.ref.Bus)
		}
		*out = append(*out, node)
		if item.depth >= maxDepth || len(*out) >= maxNodes {
			continue
		}
		var children []a11yObjectRef
		if err := obj.Call(ifaceAccessible+".GetChildren", 0).Store(&children); err != nil {
			continue
		}
		for _, child := range children {
			queue = append(queue, queued{ref: child, parentID: id, depth: item.depth + 1})
		}
	}
	_ = parentID
	return nil
}

func a11yNodeID(ref a11yObjectRef) string { return ref.Bus + "|" + string(ref.Path) }

func parseA11yNodeID(id string) (a11yObjectRef, error) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return a11yObjectRef{}, contract.Validation("INVALID_ELEMENT_ID", "accessibility element id must be bus|path", map[string]any{"element_id": id})
	}
	return a11yObjectRef{Bus: parts[0], Path: dbus.ObjectPath(parts[1])}, nil
}

type appInfo struct {
	name    string
	toolkit string
}

type appCache struct{ entries map[string]appInfo }

func newAppCache() *appCache { return &appCache{entries: map[string]appInfo{}} }

func (c *appCache) lookup(conn *dbus.Conn, bus string) appInfo {
	if c == nil || bus == "" || bus == rootBus {
		return appInfo{}
	}
	if v, ok := c.entries[bus]; ok {
		return v
	}
	info := resolveAppInfo(conn, bus)
	c.entries[bus] = info
	return info
}

func resolveAppInfo(conn *dbus.Conn, bus string) appInfo {
	obj := conn.Object(bus, appRootPath)
	name := stringProp(obj, ifaceApplication+".Name")
	if name == "" {
		name = stringProp(obj, ifaceAccessible+".Name")
	}
	return appInfo{name: name, toolkit: stringProp(obj, ifaceApplication+".ToolkitName")}
}

type pidCache struct{ entries map[string]uint32 }

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

func resolveBusPid(conn *dbus.Conn, bus string) uint32 {
	obj := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var pid uint32
	if err := obj.Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, bus).Store(&pid); err != nil {
		return 0
	}
	return pid
}

func stringProp(obj dbus.BusObject, full string) string {
	iface, prop, ok := splitProperty(full)
	if !ok {
		return ""
	}
	var variant dbus.Variant
	if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, iface, prop).Store(&variant); err != nil {
		return ""
	}
	if s, ok := variant.Value().(string); ok {
		return s
	}
	return ""
}

func splitProperty(full string) (iface, prop string, ok bool) {
	idx := strings.LastIndex(full, ".")
	if idx <= 0 || idx == len(full)-1 {
		return "", "", false
	}
	return full[:idx], full[idx+1:], true
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

func extentsFromInterfaces(obj dbus.BusObject, ifaces []string) contract.Bounds {
	if !contains(ifaces, ifaceComponent) {
		return contract.Bounds{}
	}
	var x, y, w, h int32
	// 0 = ATSPI_COORD_TYPE_SCREEN.
	if err := obj.Call(ifaceComponent+".GetExtents", 0, uint32(0)).Store(&x, &y, &w, &h); err != nil {
		return contract.Bounds{}
	}
	return contract.Bounds{X: int(x), Y: int(y), Width: int(w), Height: int(h)}
}

func hasInterface(obj dbus.BusObject, iface string) bool {
	return contains(interfaces(obj), iface)
}

func actionNames(obj dbus.BusObject) ([]string, error) {
	if !hasInterface(obj, ifaceAction) {
		return nil, contract.Precondition("ATSPI_ACTION_UNAVAILABLE", "element does not expose AT-SPI Action", nil)
	}
	return actionNamesFromInterfaces(obj, []string{ifaceAction})
}

func actionNamesFromInterfaces(obj dbus.BusObject, ifaces []string) ([]string, error) {
	if !contains(ifaces, ifaceAction) {
		return nil, nil
	}
	var n int32
	if err := obj.Call(ifaceAction+".GetNActions", 0).Store(&n); err != nil || n <= 0 {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := int32(0); i < n; i++ {
		var name string
		if err := obj.Call(ifaceAction+".GetName", 0, i).Store(&name); err == nil {
			out = append(out, name)
		}
	}
	return out, nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
