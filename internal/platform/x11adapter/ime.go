//go:build linux

package x11adapter

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// imeProbeTimeout is the per-DBus-call deadline applied to every IME
// probe. godbus' default reply timeout is 25 seconds; if ibus-daemon or
// fcitx5 hangs on the session bus a stock `mycomputer doctor` call would
// block for up to 25s on each probe. We cap at 250ms per call so the IME
// row in doctor remains bounded. Timeout is treated as "no active IME".
const imeProbeTimeout = 250 * time.Millisecond

// DetectIME implements platform.InputMethodProbe over Linux session D-Bus
// (IBus/Fcitx5). The keyboard service uses this to decide whether XTest
// typing should be blocked and whether via:auto should route through paste.
func (keyboard) DetectIME(ctx context.Context) platform.IMEStatus {
	status := platform.IMEStatus{}
	if err := ctx.Err(); err != nil {
		return status
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return status
	}
	// Do not Close the session-bus singleton — it is shared.
	if engine, inputCtx, ok := probeIBus(ctx, conn); ok {
		status.Active = true
		status.Engine = engine
		status.InputContext = inputCtx
		return status
	}
	if engine, inputCtx, ok := probeFcitx5(ctx, conn); ok {
		status.Active = true
		status.Engine = engine
		status.InputContext = inputCtx
		return status
	}
	return status
}

// ProbeIME implements platform.InputMethodProbe for doctor. Required:false;
// we never block readiness on the presence/absence of an IME.
func (k keyboard) ProbeIME(ctx context.Context) contract.BackendStatus {
	now := time.Now().UTC()
	status := k.DetectIME(ctx)
	details := map[string]any{"active": status.Active}
	if status.Engine != "" {
		details["engine"] = status.Engine
	}
	if status.InputContext != "" {
		details["input_context"] = status.InputContext
	}
	msg := "no active IME"
	if status.Active {
		msg = "IME active: " + status.Engine
	}
	return contract.BackendStatus{
		Name:      "ime",
		Ready:     true,
		Required:  false,
		Message:   msg,
		Details:   details,
		CheckedAt: now,
	}
}

func callIMEWithTimeout(ctx context.Context, obj dbus.BusObject, method string, args ...interface{}) *dbus.Call {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, imeProbeTimeout)
	defer cancel()
	return obj.CallWithContext(cctx, method, 0, args...)
}

func isIMETimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func probeIBus(ctx context.Context, conn *dbus.Conn) (engine, inputCtx string, active bool) {
	if !imeNameHasOwner(ctx, conn, "org.freedesktop.IBus") {
		return "", "", false
	}
	obj := conn.Object("org.freedesktop.IBus", dbus.ObjectPath("/org/freedesktop/IBus"))
	var variant dbus.Variant
	call := callIMEWithTimeout(ctx, obj, "org.freedesktop.DBus.Properties.Get", "org.freedesktop.IBus", "GlobalEngine")
	if call.Err != nil {
		_ = isIMETimeout(call.Err)
		return "ibus", "", false
	}
	if err := call.Store(&variant); err == nil {
		if m, ok := variant.Value().(map[string]dbus.Variant); ok {
			if name, ok := m["Name"].Value().(string); ok && name != "" {
				return name, "", true
			}
		}
	}
	return "ibus", "", false
}

func probeFcitx5(ctx context.Context, conn *dbus.Conn) (engine, inputCtx string, active bool) {
	if !imeNameHasOwner(ctx, conn, "org.fcitx.Fcitx5") {
		return "", "", false
	}
	obj := conn.Object("org.fcitx.Fcitx5", dbus.ObjectPath("/controller"))
	var engineName string
	call := callIMEWithTimeout(ctx, obj, "org.fcitx.Fcitx.Controller1.CurrentInputMethod")
	if call.Err != nil {
		_ = isIMETimeout(call.Err)
		return "fcitx5", "", false
	}
	if err := call.Store(&engineName); err == nil {
		if engineName != "" && engineName != "keyboard-us" {
			return engineName, "", true
		}
	}
	return "fcitx5", "", false
}

func imeNameHasOwner(ctx context.Context, conn *dbus.Conn, name string) bool {
	obj := conn.BusObject()
	var has bool
	call := callIMEWithTimeout(ctx, obj, "org.freedesktop.DBus.NameHasOwner", name)
	if call.Err != nil {
		return false
	}
	if err := call.Store(&has); err != nil {
		return false
	}
	return has
}
