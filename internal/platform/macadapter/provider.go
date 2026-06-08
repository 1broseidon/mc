//go:build darwin

// Package macadapter is the macOS implementation of the platform.Provider
// seam. It turns the OS-neutral capability interfaces declared in
// internal/platform into native macOS operations (CoreGraphics capture,
// Quartz Event Services input, NSPasteboard clipboard, CGWindowList +
// Accessibility window control, the AX API, and CGEventTap activity).
//
// STATUS: skeleton. The provider registers as "darwin" so the binary,
// doctor, and all portable packages resolve a real backend instead of the
// generic unsupported{} fallback. Every capability currently returns a
// canonical PLATFORM_CAP_NOT_IMPLEMENTED error; implement them in the order
// documented in docs/platform-adapters.md (screen -> input -> clipboard ->
// windows -> optional a11y/activity).
//
// Implementation rule: this package will use cgo (Quartz/AppKit/AX have no
// pure-Go bindings) behind the darwin build tag ONLY, so the Linux build
// stays cgo-free. It must NOT import internal/x11, internal/yield,
// jezek/xgb, or godbus/dbus (enforced by `make platform-boundary`); those
// are Linux-specific. Keep policy in the portable service layer — implement
// only OS primitives here.
package macadapter

import (
	"context"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// Provider is the macOS platform backend.
type Provider struct{}

// New constructs the macOS provider. Stateless today; once capabilities
// land it may cache an AX element map or a CGEventSource.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "darwin" }

// Labels are the user-facing backend strings surfaced in action results and
// observe summaries. These are provisional macOS names; adjust them as the
// concrete backends land (e.g. Capture may become "coregraphics" or
// "screencapturekit").
func (*Provider) Labels() platform.BackendLabels {
	return platform.BackendLabels{
		Platform:      "darwin",
		Screen:        "coregraphics",
		Capture:       "coregraphics.capture",
		Window:        "ax",
		Input:         "quartz",
		Clipboard:     "pasteboard",
		Accessibility: "ax",
	}
}

func (*Provider) Pointer() platform.Pointer       { return pointer{} }
func (*Provider) Keyboard() platform.Keyboard     { return keyboard{} }
func (*Provider) Screen() platform.ScreenGrabber  { return screenGrabber{} }
func (*Provider) Windows() platform.WindowManager { return windowManager{} }
func (*Provider) Clipboard() platform.Clipboard   { return clipboard{} }

// Accessibility (macOS AX API) is not yet implemented; report unavailable
// so callers degrade gracefully rather than erroring per-call.
func (*Provider) Accessibility() (platform.Accessibility, bool) { return nil, false }

// Activity (CGEventTap yield watcher) is not yet implemented; report
// unavailable so --respect-user simply disables yield.
func (*Provider) Activity() (platform.UserActivityWatcher, bool) { return nil, false }

// Probe reports macOS backend readiness rows for doctor. The skeleton emits
// a single not-ready row; real implementations should add per-capability
// rows including the TCC permission states (Screen Recording, Accessibility)
// that gate capture and input on macOS.
func (*Provider) Probe(ctx context.Context) []contract.BackendStatus {
	return []contract.BackendStatus{{
		Name:     "darwin",
		Ready:    false,
		Required: true,
		Message:  "macOS backend not yet implemented; see docs/platform-adapters.md",
	}}
}
