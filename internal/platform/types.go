package platform

import (
	"image"
	"time"

	"github.com/1broseidon/mc/internal/contract"
)

// Button identifies a pointer button in OS-neutral terms. Adapters map
// these onto native button codes (X11 button numbers 1/2/3, Quartz
// kCGMouseButton* constants, …).
type Button int

const (
	ButtonLeft Button = iota
	ButtonMiddle
	ButtonRight
)

// PressAction distinguishes the two halves of a physical button or key
// actuation. The service layer composes clicks, drags, and chords out of
// Press/Release pairs so the adapter only implements the primitive.
type PressAction int

const (
	Press PressAction = iota
	Release
)

// Key is an OS-neutral key token. Canonical names are lowercase and match
// the tokens produced by the service layer's chord parser:
//
//   - Named keys: "enter", "tab", "esc", "backspace", "delete", "home",
//     "end", "pageup", "pagedown", "up", "down", "left", "right", "f1".."f35".
//   - Modifiers: "ctrl", "alt", "shift", "super".
//   - Single printable characters carry the character itself as the token
//     (e.g. "a", "/", "5").
//
// Mapping a Key to a native keycode/keysym is the adapter's responsibility:
// the X11 adapter resolves tokens through the active XKB keymap (and may
// return INPUT_LAYOUT_UNREACHABLE), while the macOS adapter resolves them
// through the current keyboard layout / Unicode synthesis.
type Key string

// Selection names a clipboard selection. X11 distinguishes CLIPBOARD from
// PRIMARY; macOS exposes only the general pasteboard. The service layer
// validates a requested selection against Provider.Clipboard().Selections()
// and emits CLIPBOARD_SELECTION_UNSUPPORTED when a platform lacks it.
type Selection string

const (
	SelectionClipboard Selection = "clipboard"
	SelectionPrimary   Selection = "primary"
)

// NativeID is an opaque, adapter-resolvable handle to a window. The service
// layer obtains one from a matched contract.WindowInfo (see NativeIDOf) and
// passes it back into WindowManager control methods. Callers MUST NOT
// interpret either field.
//
//   - ID mirrors contract.WindowInfo.ID and is the stable cross-reference
//     used by every adapter.
//   - Raw carries the platform-native handle when one exists as an integer
//     (X11 XID, CGWindowID); it is 0 when the platform identifies windows
//     by something else (e.g. an AXUIElement the adapter maps internally
//     keyed on ID).
type NativeID struct {
	ID  string
	Raw uint64
}

// NativeIDOf builds a NativeID from a resolved window record. The X11 XID
// doubles as Raw; other adapters ignore Raw and key on ID.
func NativeIDOf(w contract.WindowInfo) NativeID {
	return NativeID{ID: w.ID, Raw: uint64(w.XID)}
}

// MaximizeAxis selects which axes a maximize request affects. Mirrors the
// window_maximize action's axis argument.
type MaximizeAxis int

const (
	MaximizeBoth MaximizeAxis = iota
	MaximizeHorizontal
	MaximizeVertical
)

// CursorImage is a decoded pointer-cursor sprite plus its hotspot and
// current screen position. Adapters acquire the raw pixels (XFixes
// GetCursorImage, CGCursor, …) and hand back a ready-to-composite RGBA so
// the screen service can overlay it onto a screenshot without any
// OS-specific pixel math.
type CursorImage struct {
	Image *image.RGBA
	// HotX, HotY locate the active pixel within Image.
	HotX int
	HotY int
	// X, Y are the cursor's current top-left position in screen space
	// (already adjusted by the hotspot by the adapter is NOT assumed —
	// the service subtracts the hotspot during compositing).
	X int
	Y int
}

// Node is an OS-neutral accessibility element. Adapters flatten their
// native accessibility tree (AT-SPI over D-Bus, the macOS AX API, …) into
// a depth-ordered slice of Node values; the service layer owns the
// cross-platform policy that runs on top — window-id correlation, role
// filtering, and the depth/node caps.
//
// The slice returned by Accessibility.Tree is breadth-first and
// self-describing: Depth and ParentID let the service reconstruct
// parent/child relationships (needed so a labeled control can inherit its
// frame's correlated window id) without the adapter pre-computing any of
// that policy.
type Node struct {
	// ID is an opaque, adapter-scoped identifier addressable by
	// PerformAction and SetText. Stable for the lifetime of one Tree call.
	ID       string
	ParentID string
	Depth    int

	Name       string
	Role       string
	Bounds     contract.Bounds
	Actions    []string
	Interfaces []string

	// App and Toolkit name the owning application and UI toolkit when the
	// platform exposes them; empty string otherwise (never a placeholder).
	App     string
	Toolkit string
	// PID is optional native process metadata used by the portable a11y
	// service for window correlation. Zero means unknown.
	PID uint32
}

// ActivityEvent is a single observed human input event reported by a
// UserActivityWatcher. It deliberately carries no coordinates or key
// identities — the watcher's only job is to tell the pipeline that a real
// user (not MyComputer's own synthetic input) is driving the desktop so a
// --respect-user batch can pause.
type ActivityEvent struct {
	// Kind is a backend event token (X11 uses RawMotion,
	// RawButtonPress, RawKeyPress).
	Kind string
	// Optional native metadata used only for result/audit reporting.
	DeviceID int
	SourceID int
	Detail   int
	TS       time.Time
}

// IMEStatus describes the current state of any Input Method Editor.
// It is read-only; callers must never try to disable an active IME.
type IMEStatus struct {
	Active       bool   `json:"active"`
	Engine       string `json:"engine,omitempty"`
	InputContext string `json:"input_context,omitempty"`
}

// DisplayAutoDetectResult describes a backend's attempt to infer a missing
// display environment. X11 fills these from /tmp/.X11-unix; other platforms
// generally leave the optional capability unimplemented.
type DisplayAutoDetectResult struct {
	Display   string
	Source    string
	Ambiguous []string
	Empty     bool
}
