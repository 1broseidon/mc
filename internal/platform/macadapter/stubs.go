//go:build darwin

package macadapter

import (
	"context"
	"image"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// This file holds the not-yet-implemented capability stubs for the macOS
// backend. Each mutating op returns a canonical PLATFORM_CAP_NOT_IMPLEMENTED
// dependency error; read-only enumerations return empty results so callers
// degrade gracefully. As each capability lands (see docs/platform-adapters.md),
// delete the matching stub block and add a real implementation file
// (screen_darwin.go, input_darwin.go, …).
//
// Implementation order: screen -> input -> clipboard -> windows ->
// optional a11y/activity.

func notImplemented(capability, op string) error {
	return contract.Dependency(
		"PLATFORM_CAP_NOT_IMPLEMENTED",
		"this platform capability is not implemented in the macOS backend yet",
		map[string]any{"capability": capability, "operation": op, "backend": "darwin"},
	)
}

// --- pointer (pending) ---

type pointer struct{}

func (pointer) MoveTo(context.Context, int, int) error { return notImplemented("pointer", "move_to") }
func (pointer) Button(context.Context, platform.Button, platform.PressAction) error {
	return notImplemented("pointer", "button")
}
func (pointer) Scroll(context.Context, int, int) error { return notImplemented("pointer", "scroll") }

// --- keyboard (pending) ---

type keyboard struct{}

func (keyboard) TypeText(context.Context, string) error {
	return notImplemented("keyboard", "type_text")
}
func (keyboard) KeyCombo(context.Context, []platform.Key) error {
	return notImplemented("keyboard", "key_combo")
}

// --- screen (pending) ---

type screenGrabber struct{}

func (screenGrabber) Grab(context.Context, contract.Bounds) (*image.RGBA, contract.Bounds, error) {
	return nil, contract.Bounds{}, notImplemented("screen", "grab")
}
func (screenGrabber) ScreenBounds(context.Context) (contract.Bounds, error) {
	return contract.Bounds{}, notImplemented("screen", "bounds")
}
func (screenGrabber) Monitors(context.Context) ([]contract.MonitorInfo, error) {
	return nil, notImplemented("screen", "monitors")
}
func (screenGrabber) CursorPos(context.Context) (contract.Point, error) {
	return contract.Point{}, notImplemented("screen", "cursor_pos")
}
func (screenGrabber) CursorImage(context.Context) (*platform.CursorImage, bool, error) {
	return nil, false, nil
}

// --- windows (pending) ---

type windowManager struct{}

func (windowManager) List(context.Context) ([]contract.WindowInfo, error) {
	return nil, notImplemented("windows", "list")
}
func (windowManager) Focus(context.Context, platform.NativeID) error {
	return notImplemented("windows", "focus")
}
func (windowManager) Raise(context.Context, platform.NativeID) error {
	return notImplemented("windows", "raise")
}
func (windowManager) Minimize(context.Context, platform.NativeID) error {
	return notImplemented("windows", "minimize")
}
func (windowManager) Maximize(context.Context, platform.NativeID, platform.MaximizeAxis) error {
	return notImplemented("windows", "maximize")
}
func (windowManager) Move(context.Context, platform.NativeID, int, int) error {
	return notImplemented("windows", "move")
}
func (windowManager) Resize(context.Context, platform.NativeID, int, int) error {
	return notImplemented("windows", "resize")
}
func (windowManager) Workspace(context.Context, platform.NativeID, int) error {
	return notImplemented("windows", "workspace")
}
func (windowManager) Close(context.Context, platform.NativeID) error {
	return notImplemented("windows", "close")
}
func (windowManager) Capabilities(context.Context) ([]string, error) { return nil, nil }

// --- clipboard (pending) ---
//
// NOTE: macOS NSPasteboard persists after process exit, so the macOS
// clipboard deliberately does NOT implement platform.ClipboardDaemon, and
// Selections() returns only {clipboard} (no PRIMARY). The portable
// clipboard service already degrades a "both" write to clipboard-only.

type clipboard struct{}

func (clipboard) Read(context.Context, platform.Selection, string) (string, string, error) {
	return "", "", notImplemented("clipboard", "read")
}
func (clipboard) Write(context.Context, platform.Selection, string, string) error {
	return notImplemented("clipboard", "write")
}
func (clipboard) Selections() []platform.Selection {
	return []platform.Selection{platform.SelectionClipboard}
}
