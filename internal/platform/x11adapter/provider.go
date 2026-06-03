//go:build linux

// Package x11adapter is the Linux/X11 implementation of the
// platform.Provider seam. It turns the OS-neutral capability interfaces
// declared in internal/platform into concrete X11/XTest/RandR/XFixes (and,
// for later capabilities, AT-SPI/XInput2) operations.
//
// It depends on internal/x11 (a leaf package: contract + xgb only),
// internal/contract, and internal/platform. It MUST NOT import a service
// package (input, screen, window, …) — those depend on platform, so an
// import here would create a cycle. The X11 protocol code therefore lives
// in this package, migrated out of the service layer one capability at a
// time.
//
// Migration status:
//
//	screen     — DONE (capture, monitors, cursor)
//	pointer    — DONE (XTest move/button/scroll)
//	keyboard   — DONE (XTest + XKB keymap)
//	clipboard  — DONE (selection-owner protocol)
//	windows    — DONE (EWMH/ICCCM list + control)
//	a11y       — DONE (AT-SPI over D-Bus)
//	activity   — DONE (XInput2 xinput watcher)
package x11adapter

import (
	"context"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// Provider is the X11 platform backend.
type Provider struct{}

// New constructs the X11 provider. Cheap and stateless: each capability
// call opens its own short-lived X11 connection via internal/x11, matching
// the service layer's existing per-call connection model.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "x11" }

func (*Provider) Pointer() platform.Pointer       { return pointer{} }
func (*Provider) Keyboard() platform.Keyboard     { return keyboard{} }
func (*Provider) Screen() platform.ScreenGrabber  { return screenGrabber{} }
func (*Provider) Windows() platform.WindowManager { return windowManager{} }
func (*Provider) Clipboard() platform.Clipboard   { return clipboard{} }

// Accessibility exposes AT-SPI through the platform seam.
func (*Provider) Accessibility() (platform.Accessibility, bool) { return x11Accessibility{}, true }

// Activity exposes the XInput2 yield watcher through the platform seam.
func (*Provider) Activity() (platform.UserActivityWatcher, bool) { return xinputActivity{}, true }

// Probe reports the X11 backend readiness rows. Delegates to the existing
// internal/x11 probe, which covers DISPLAY/XAUTHORITY/x11/xtest/randr/xfixes.
func (*Provider) Probe(ctx context.Context) []contract.BackendStatus {
	return x11.Probe()
}

// MaybeAutoDetectDisplay implements platform.DisplayAutoDetector over the
// existing X11 socket probe. It is intentionally X11-only: platforms without
// DISPLAY semantics simply do not implement DisplayAutoDetector.
func (*Provider) MaybeAutoDetectDisplay() platform.DisplayAutoDetectResult {
	res := x11.MaybeAutoDetectDisplay()
	return platform.DisplayAutoDetectResult{
		Display:   res.Display,
		Source:    res.Source,
		Ambiguous: res.Ambiguous,
		Empty:     res.Empty,
	}
}
