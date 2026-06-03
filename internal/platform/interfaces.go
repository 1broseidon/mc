package platform

import (
	"context"
	"image"
	"time"

	"github.com/1broseidon/mc/internal/contract"
)

// Pointer injects mouse motion and button events. The service layer owns
// all coordinate-space resolution (contract.Resolve), logical-coords
// scaling, drag interpolation, and bounds validation; the adapter only
// performs the resolved primitive in absolute screen pixels.
type Pointer interface {
	// MoveTo warps the pointer to an absolute screen coordinate.
	MoveTo(ctx context.Context, x, y int) error
	// Button presses or releases a pointer button at the current pointer
	// position. The service issues Press/Release pairs to compose clicks.
	Button(ctx context.Context, btn Button, action PressAction) error
	// Scroll emits scroll ticks. Positive dy scrolls down, positive dx
	// scrolls right; magnitude is the number of discrete ticks. The
	// service translates the direction+amount request into dx/dy.
	Scroll(ctx context.Context, dx, dy int) error
}

// Keyboard injects keystrokes. The service layer owns the type_text
// routing policy (xtest vs paste), chord parsing, and the auto-route
// heuristics; the adapter implements only the two injection primitives.
type Keyboard interface {
	// TypeText types a literal string using layout-aware key synthesis.
	// Adapters that cannot reach a given character from the active layout
	// MUST return contract Validation("INPUT_LAYOUT_UNREACHABLE", …) and
	// type nothing, so the service can fall back to the paste route.
	TypeText(ctx context.Context, text string) error
	// KeyCombo presses the given keys in order, then releases them in
	// reverse order (a chord). Used for both single named keys and
	// modifier combos like ctrl+v.
	KeyCombo(ctx context.Context, combo []Key) error
}

// ScreenGrabber captures pixels and reports display geometry. Image
// post-processing (downscale, zoom crop, JPEG/PNG encode, logical-coords
// scaling) and cursor compositing remain portable service-layer concerns.
type ScreenGrabber interface {
	// Grab returns the pixels within an absolute screen-space rectangle.
	// The returned Bounds is the actual captured rectangle after the
	// adapter clamps it to the real screen.
	Grab(ctx context.Context, region contract.Bounds) (*image.RGBA, contract.Bounds, error)
	// ScreenBounds reports the full virtual-screen rectangle, used by the
	// service for out-of-bounds checks before input.
	ScreenBounds(ctx context.Context) (contract.Bounds, error)
	// Monitors enumerates physical displays. An empty slice is valid and
	// makes the service synthesize a single root monitor.
	Monitors(ctx context.Context) ([]contract.MonitorInfo, error)
	// CursorPos reports the pointer position in screen space.
	CursorPos(ctx context.Context) (contract.Point, error)
	// CursorImage returns the current cursor sprite for overlay. ok=false
	// (with nil error) means the platform cannot supply a cursor image;
	// the service simply skips the overlay in that case.
	CursorImage(ctx context.Context) (img *CursorImage, ok bool, err error)
}

// WindowManager lists and controls top-level windows. The service layer
// owns target matching (contract.MatchWindowInfo), ambiguity handling,
// post-op geometry divergence detection, and warning construction; the
// adapter performs the raw list/control operations and reports what the
// platform can actually do via Capabilities.
type WindowManager interface {
	List(ctx context.Context) ([]contract.WindowInfo, error)
	Focus(ctx context.Context, id NativeID) error
	Raise(ctx context.Context, id NativeID) error
	Minimize(ctx context.Context, id NativeID) error
	Maximize(ctx context.Context, id NativeID, axis MaximizeAxis) error
	// Move and Resize issue best-effort geometry changes. The service
	// re-reads bounds afterward and decides whether to raise a
	// WINDOW_GEOMETRY_REFUSED warning, so the adapter need not verify.
	Move(ctx context.Context, id NativeID, x, y int) error
	Resize(ctx context.Context, id NativeID, w, h int) error
	Workspace(ctx context.Context, id NativeID, index int) error
	Close(ctx context.Context, id NativeID) error
	// Capabilities reports platform-supported window operations as short
	// tokens (e.g. "ewmh.move", "ewmh.workspace"). Replaces the X11
	// _NET_SUPPORTED probe; the doctor x11/window row surfaces the list.
	Capabilities(ctx context.Context) ([]string, error)
}

// WindowWorkspaceReader is an OPTIONAL capability for backends that can
// report which virtual desktop / workspace a window currently occupies.
// The window service uses it to verify a window_workspace move was
// honored (an X11 tiling-WM / pager can silently ignore the request).
// Discover it with a type assertion on Provider.Windows():
//
//	if r, ok := platform.Current().Windows().(platform.WindowWorkspaceReader); ok { … }
//
// Platforms without an addressable per-window workspace index (or where
// the concept differs, e.g. macOS Spaces) do NOT implement this, and the
// service skips the honored-check rather than erroring.
type WindowWorkspaceReader interface {
	// WorkspaceOf returns the zero-based workspace index of the window, or
	// a negative value when it cannot be determined. (Named WorkspaceOf to
	// avoid colliding with WindowManager.Workspace, which is a mutator.)
	WorkspaceOf(ctx context.Context, id NativeID) (int, error)
}

// Clipboard reads and writes selections. The service layer owns MIME
// validation, the save/restore paste orchestration, and audit byte-count
// bookkeeping; the adapter handles the platform clipboard mechanism (the
// X11 selection-owner daemon, NSPasteboard, …).
type Clipboard interface {
	Read(ctx context.Context, sel Selection, mime string) (content string, gotMime string, err error)
	Write(ctx context.Context, sel Selection, content, mime string) error
	// Selections lists the selections this platform supports. Linux
	// returns {clipboard, primary}; macOS returns {clipboard}. The service
	// validates requests against this set.
	Selections() []Selection
}

// ClipboardDaemon is an OPTIONAL capability implemented by clipboard
// backends whose writes are bound to a live owner process — X11 selection
// ownership dies when the owning client exits, so a long-lived process (or
// a detached daemon) must hold it. Discover it with a type assertion on the
// value returned by Provider.Clipboard():
//
//	if d, ok := platform.Current().Clipboard().(platform.ClipboardDaemon); ok { … }
//
// Platforms whose clipboard persists after process exit (macOS
// NSPasteboard) do NOT implement this interface, and callers degrade to a
// no-daemon model.
type ClipboardDaemon interface {
	// Done returns a channel closed the first time in-process selection
	// ownership is lost (another client took a selection this process was
	// holding), letting a detached owner daemon exit cleanly.
	Done() (<-chan struct{}, error)
}

// DisplayAutoDetector is an OPTIONAL capability for backends that can
// repair a missing display environment from a local desktop session. X11
// uses it to detect a single live /tmp/.X11-unix socket when MCP hosts are
// launched from a non-X-aware shell. Platforms without DISPLAY semantics do
// not implement it.
type DisplayAutoDetector interface {
	MaybeAutoDetectDisplay() DisplayAutoDetectResult
}

// InputMethodProbe is an OPTIONAL capability for keyboard/input backends
// that can detect the current Input Method Editor state. The input service
// uses it to decide whether type_text via:xtest should be blocked and
// whether via:auto should route through paste.
type InputMethodProbe interface {
	DetectIME(ctx context.Context) IMEStatus
	ProbeIME(ctx context.Context) contract.BackendStatus
}

// Accessibility exposes the platform accessibility tree and its actions.
// Optional — see Provider.Accessibility. The service layer owns the
// breadth-first traversal policy, window-id correlation, and per-bus
// caching; the adapter supplies a flattened, normalized snapshot and the
// node-addressed mutators.
type Accessibility interface {
	// Tree returns a breadth-first, depth-ordered snapshot. maxDepth and
	// maxNodes bound the walk; adapters honor them to cap cost.
	Tree(ctx context.Context, maxDepth, maxNodes int) ([]Node, error)
	// PerformAction invokes a named action (empty selects the default
	// action) on the node addressed by id.
	PerformAction(ctx context.Context, nodeID, action string) error
	// SetText replaces the editable text contents of the node.
	SetText(ctx context.Context, nodeID, text string) error
}

// UserActivityWatcher observes real human input so a --respect-user batch
// can yield. Optional — see Provider.Activity. The adapter is responsible
// for filtering out MyComputer's own synthetic input before emitting.
type UserActivityWatcher interface {
	// Available reports whether the watcher can run, plus a short detail
	// string (e.g. the resolved helper-binary path) for the doctor row.
	Available() (ok bool, detail string)
	// Start begins watching and returns a channel of human-input events
	// plus a stop function. The channel closes when watching ends.
	Start(ctx context.Context) (events <-chan ActivityEvent, stop func(), err error)
	// Sample starts a short watcher run and counts user events. Used by doctor.
	Sample(ctx context.Context, d time.Duration) (count int, err error)
}

// Provider is the per-OS aggregate of every capability. Exactly one
// Provider is active per process, selected by build tag and installed via
// SetProvider. Mandatory capabilities are returned directly; optional ones
// use a comma-ok accessor so callers branch on support explicitly rather
// than nil-checking.
type Provider interface {
	// Name identifies the active backend for diagnostics (e.g. "x11",
	// "darwin", "unsupported").
	Name() string

	Pointer() Pointer
	Keyboard() Keyboard
	Screen() ScreenGrabber
	Windows() WindowManager
	Clipboard() Clipboard

	// Accessibility returns the accessibility capability when the platform
	// exposes one. ok=false means callers should report the feature as
	// unavailable rather than erroring per-call.
	Accessibility() (a Accessibility, ok bool)
	// Activity returns the user-activity watcher when available.
	Activity() (w UserActivityWatcher, ok bool)

	// Probe reports backend readiness rows for the doctor command. Each
	// adapter contributes its own platform-specific rows (DISPLAY/xtest on
	// Linux, Screen-Recording/Accessibility TCC grants on macOS); the
	// portable diagnostic layer aggregates them into readiness blockers
	// and warnings.
	Probe(ctx context.Context) []contract.BackendStatus
}
