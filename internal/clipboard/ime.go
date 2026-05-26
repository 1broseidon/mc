package clipboard

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/1broseidon/mc/internal/contract"
)

// imeProbeTimeout is the per-DBus-call deadline applied to every IME
// probe. godbus' default reply timeout is 25 seconds; if ibus-daemon or
// fcitx5 hangs on the session bus a stock `mycomputer doctor` call would
// block for up to 25s on each probe. We cap at 250ms per call so the
// whole IME row of `doctor --json` finishes within ~500ms upper bound
// (ibus probe + fcitx probe, including a possible NameHasOwner round
// trip each). Timeout is treated as "no active IME" rather than an
// error: the IME row in doctor is required:false and we never want a
// flaky session bus to surface as a doctor failure.
const imeProbeTimeout = 250 * time.Millisecond

// IMEStatus describes the current state of any Input Method Editor
// (IBus/Fcitx5) detected on the session DBus. It is READ-ONLY — the
// caller must never try to disable an active IME.
type IMEStatus struct {
	Active       bool   `json:"active"`
	Engine       string `json:"engine,omitempty"`
	InputContext string `json:"input_context,omitempty"`
}

// DetectIME probes the session bus for IBus and Fcitx5. It returns a
// best-effort status; any DBus failure or per-call timeout surfaces as
// active:false.
//
// Detection rules:
//   - active==true when a known bus name is registered AND the IME
//     reports a non-empty global engine name (IBus) or an active input
//     context (Fcitx5).
//   - active==false when neither service is present, the service is
//     present but reports no engine, or any DBus call times out.
func DetectIME() IMEStatus {
	status := IMEStatus{}
	conn, err := dbus.SessionBus()
	if err != nil {
		return status
	}
	// Do not Close the session-bus singleton — it is shared.
	if engine, ctx, ok := probeIBus(conn); ok {
		status.Active = true
		status.Engine = engine
		status.InputContext = ctx
		return status
	}
	if engine, ctx, ok := probeFcitx5(conn); ok {
		status.Active = true
		status.Engine = engine
		status.InputContext = ctx
		return status
	}
	return status
}

// callWithTimeout invokes obj.CallWithContext using a fresh
// imeProbeTimeout-bounded context. Returns the call error (including
// context.DeadlineExceeded when the godbus reply does not arrive in
// time). Callers translate any non-nil error into active:false.
func callWithTimeout(obj dbus.BusObject, method string, args ...interface{}) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), imeProbeTimeout)
	defer cancel()
	return obj.CallWithContext(ctx, method, 0, args...)
}

// isTimeout reports whether err is the deadline-exceeded sentinel from
// a CallWithContext invocation that ran past imeProbeTimeout. Kept as a
// helper so we can extend the rule (e.g., classify godbus' own
// io.ErrNoMessage as timeout) without touching call sites.
func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func probeIBus(conn *dbus.Conn) (engine, inputCtx string, active bool) {
	if !nameHasOwner(conn, "org.freedesktop.IBus") {
		return "", "", false
	}
	// Best-effort: ask for the current global engine. If the call fails,
	// times out, or returns an empty string we still report ibus as
	// present-but-inactive.
	obj := conn.Object("org.freedesktop.IBus", dbus.ObjectPath("/org/freedesktop/IBus"))
	var variant dbus.Variant
	call := callWithTimeout(obj, "org.freedesktop.DBus.Properties.Get", "org.freedesktop.IBus", "GlobalEngine")
	if call.Err != nil {
		// Timeout or transport error — treat as inactive.
		_ = isTimeout(call.Err)
		return "ibus", "", false
	}
	if err := call.Store(&variant); err == nil {
		if m, ok := variant.Value().(map[string]dbus.Variant); ok {
			if name, ok := m["Name"].Value().(string); ok && name != "" {
				return name, "", true
			}
		}
	}
	// IBus present but no engine resolved. Treat as inactive so we do
	// not over-trigger paste fallback when the user just happens to have
	// ibus-daemon running with no engine selected.
	return "ibus", "", false
}

func probeFcitx5(conn *dbus.Conn) (engine, inputCtx string, active bool) {
	if !nameHasOwner(conn, "org.fcitx.Fcitx5") {
		return "", "", false
	}
	obj := conn.Object("org.fcitx.Fcitx5", dbus.ObjectPath("/controller"))
	var engineName string
	call := callWithTimeout(obj, "org.fcitx.Fcitx.Controller1.CurrentInputMethod")
	if call.Err != nil {
		_ = isTimeout(call.Err)
		return "fcitx5", "", false
	}
	if err := call.Store(&engineName); err == nil {
		if engineName != "" && engineName != "keyboard-us" {
			return engineName, "", true
		}
	}
	return "fcitx5", "", false
}

func nameHasOwner(conn *dbus.Conn, name string) bool {
	obj := conn.BusObject()
	var has bool
	call := callWithTimeout(obj, "org.freedesktop.DBus.NameHasOwner", name)
	if call.Err != nil {
		// Treat timeout/transport as "no owner" so we exit the probe
		// chain quickly instead of trying the actual property call.
		return false
	}
	if err := call.Store(&has); err != nil {
		return false
	}
	return has
}

// ProbeIME returns a BackendStatus describing IME state for doctor.
// Required:false; we never block readiness on the presence/absence of
// an IME.
func ProbeIME() contract.BackendStatus {
	now := time.Now().UTC()
	status := DetectIME()
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
