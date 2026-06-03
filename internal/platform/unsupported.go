package platform

import (
	"context"
	"image"
	"runtime"

	"github.com/1broseidon/mc/internal/contract"
)

// unsupported is the default Provider used when no adapter has registered
// for the running OS (or when a test has not installed a fake). Every
// operation fails with a canonical PLATFORM_UNSUPPORTED dependency error so
// callers get a consistent, machine-readable envelope instead of a nil
// dereference. Read-only enumerations return empty results.
type unsupported struct{}

func unsupportedErr(op string) error {
	return contract.Dependency(
		"PLATFORM_UNSUPPORTED",
		"no MyComputer platform backend is available for this operating system",
		map[string]any{"os": runtime.GOOS, "operation": op},
	)
}

func (unsupported) Name() string { return "unsupported" }

func (u unsupported) Pointer() Pointer       { return u }
func (u unsupported) Keyboard() Keyboard     { return u }
func (u unsupported) Screen() ScreenGrabber  { return u }
func (u unsupported) Windows() WindowManager { return u }
func (u unsupported) Clipboard() Clipboard   { return u }

func (unsupported) Accessibility() (Accessibility, bool)  { return nil, false }
func (unsupported) Activity() (UserActivityWatcher, bool) { return nil, false }

func (unsupported) Probe(context.Context) []contract.BackendStatus {
	return []contract.BackendStatus{{
		Name:     "platform",
		Ready:    false,
		Required: true,
		Message:  "no platform backend registered for " + runtime.GOOS,
	}}
}

// Pointer
func (unsupported) MoveTo(context.Context, int, int) error { return unsupportedErr("pointer.move") }
func (unsupported) Button(context.Context, Button, PressAction) error {
	return unsupportedErr("pointer.button")
}
func (unsupported) Scroll(context.Context, int, int) error { return unsupportedErr("pointer.scroll") }

// Keyboard
func (unsupported) TypeText(context.Context, string) error {
	return unsupportedErr("keyboard.type_text")
}
func (unsupported) KeyCombo(context.Context, []Key) error {
	return unsupportedErr("keyboard.key_combo")
}

// ScreenGrabber
func (unsupported) Grab(context.Context, contract.Bounds) (*image.RGBA, contract.Bounds, error) {
	return nil, contract.Bounds{}, unsupportedErr("screen.grab")
}
func (unsupported) ScreenBounds(context.Context) (contract.Bounds, error) {
	return contract.Bounds{}, unsupportedErr("screen.bounds")
}
func (unsupported) Monitors(context.Context) ([]contract.MonitorInfo, error) {
	return nil, unsupportedErr("screen.monitors")
}
func (unsupported) CursorPos(context.Context) (contract.Point, error) {
	return contract.Point{}, unsupportedErr("screen.cursor_pos")
}
func (unsupported) CursorImage(context.Context) (*CursorImage, bool, error) {
	return nil, false, nil
}

// WindowManager
func (unsupported) List(context.Context) ([]contract.WindowInfo, error) {
	return nil, unsupportedErr("windows.list")
}
func (unsupported) Focus(context.Context, NativeID) error { return unsupportedErr("windows.focus") }
func (unsupported) Raise(context.Context, NativeID) error { return unsupportedErr("windows.raise") }
func (unsupported) Minimize(context.Context, NativeID) error {
	return unsupportedErr("windows.minimize")
}
func (unsupported) Maximize(context.Context, NativeID, MaximizeAxis) error {
	return unsupportedErr("windows.maximize")
}
func (unsupported) Move(context.Context, NativeID, int, int) error {
	return unsupportedErr("windows.move")
}
func (unsupported) Resize(context.Context, NativeID, int, int) error {
	return unsupportedErr("windows.resize")
}
func (unsupported) Workspace(context.Context, NativeID, int) error {
	return unsupportedErr("windows.workspace")
}
func (unsupported) Close(context.Context, NativeID) error { return unsupportedErr("windows.close") }
func (unsupported) Capabilities(context.Context) ([]string, error) {
	return nil, nil
}

// Clipboard
func (unsupported) Read(context.Context, Selection, string) (string, string, error) {
	return "", "", unsupportedErr("clipboard.read")
}
func (unsupported) Write(context.Context, Selection, string, string) error {
	return unsupportedErr("clipboard.write")
}
func (unsupported) Selections() []Selection { return nil }
