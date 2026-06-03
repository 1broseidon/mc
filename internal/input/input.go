package input

// Anvil · target: internal/input · kind: package · scope: package
// caller profile: agent,script · surface pattern: package-API (delegating) · risk class: R0
// contracts: Move/Click/Drag/Scroll/PressKey/TypeText/TypeTextWith + error codes preserved
// obligations: XTest injection + XKB keymap delegated to platform.Provider

import (
	"context"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/screen"
	"github.com/1broseidon/mc/internal/window"
)

// TypeTextVia enumerates the routing modes for TypeText.
//   - "auto" (default): if len(text) > 64 OR text contains non-ASCII OR
//     text contains control chars → "paste"; else "xtest". If an IME is
//     active "paste" is forced regardless of length.
//   - "xtest": layout-aware XTest keystroke injection. Refuses chars not
//     reachable from the active XKB layout with INPUT_LAYOUT_UNREACHABLE.
//     Rejected with INPUT_IME_ACTIVE when an IME is detected.
//   - "paste": save current CLIPBOARD → write text → ctrl+v → restore.
//     Surfaces clipboard_restored:bool in the result.
const (
	TypeTextViaAuto  = "auto"
	TypeTextViaXTest = "xtest"
	TypeTextViaPaste = "paste"
)

// TypeTextRequest is the structured input for the upgraded TypeText.
// Empty Via is treated as "auto". A zero-value request types empty
// string, which is a no-op.
type TypeTextRequest struct {
	Text string `json:"text"`
	Via  string `json:"via,omitempty" jsonschema:"xtest, paste, or auto (default auto)"`
}

// TypeTextResult surfaces the chosen route plus paste-route bookkeeping.
type TypeTextResult struct {
	Via               string `json:"via"`
	ClipboardRestored bool   `json:"clipboard_restored,omitempty"`
	IMEActive         bool   `json:"ime_active,omitempty"`
	IMEEngine         string `json:"ime_engine,omitempty"`
}

type ClickRequest struct {
	Point  contract.Point `json:"point"`
	Button string         `json:"button,omitempty" jsonschema:"left, middle, right"`
	Count  int            `json:"count,omitempty"`
}

type DragRequest struct {
	From       contract.Point `json:"from"`
	To         contract.Point `json:"to"`
	Button     string         `json:"button,omitempty"`
	DurationMS int            `json:"duration_ms,omitempty"`
}

type ScrollRequest struct {
	Point     contract.Point `json:"point"`
	Direction string         `json:"direction" jsonschema:"up, down, left, right"`
	Amount    int            `json:"amount,omitempty"`
}

// ResolveContextFor builds a ResolveContext populated for the spaces
// the given Points actually use. We only spend the round-trip cost on
// window/monitor lists when at least one point requires it. Every
// coordinate-space conversion still routes through contract.Resolve —
// this helper just supplies the ambient inputs Resolve needs.
//
// Exported so the pipeline dry-run path can hydrate a ResolveContext
// without going through a mutating Move/Click call. The unexported
// alias resolveContextFor is retained for in-package callers.
func ResolveContextFor(ctx context.Context, points ...contract.Point) (contract.ResolveContext, error) {
	needWindows := false
	needMonitors := false
	for _, p := range points {
		switch p.Space {
		case contract.CoordSpaceWindow, contract.CoordSpaceWindowFrame:
			needWindows = true
		case contract.CoordSpaceMonitor:
			needMonitors = true
		}
	}
	rctx := contract.ResolveContext{}
	if needWindows {
		wins, err := window.List(ctx)
		if err != nil {
			return rctx, err
		}
		rctx.Windows = wins
	}
	if needMonitors {
		info, err := screen.Info(ctx)
		if err != nil {
			return rctx, err
		}
		rctx.Monitors = info.Monitors
	}
	return rctx, nil
}

// resolveScreenPoint converts a Point in any coordinate space into an
// absolute, logical-coords-adjusted screen coordinate, then validates it
// against the screen bounds. POINT_OUT_OF_BOUNDS is owned here (not in the
// adapter) so the contract is identical across platforms.
func resolveScreenPoint(ctx context.Context, point contract.Point) (int, int, error) {
	rctx, err := ResolveContextFor(ctx, point)
	if err != nil {
		return 0, 0, err
	}
	x, y, err := contract.Resolve(point, rctx)
	if err != nil {
		return 0, 0, err
	}
	x, y = applyLogicalCoords(ctx, x, y)
	bounds, err := platform.Current().Screen().ScreenBounds(ctx)
	if err != nil {
		return 0, 0, err
	}
	if !bounds.Contains(x, y) {
		return 0, 0, contract.Validation("POINT_OUT_OF_BOUNDS", "screen coordinate is outside the screen", map[string]any{"x": x, "y": y, "screen": bounds})
	}
	return x, y, nil
}

func Move(ctx context.Context, point contract.Point) error {
	x, y, err := resolveScreenPoint(ctx, point)
	if err != nil {
		return err
	}
	return platform.Current().Pointer().MoveTo(ctx, x, y)
}

// applyLogicalCoords translates a resolved physical-space coordinate
// from logical pixels back to physical pixels when the experimental
// --logical-coords flag is active. The primary monitor's RandR scale
// is used because XTest input is screen-wide and per-window monitor
// affinity is non-trivial without a focused-window probe (would defeat
// the purpose of having one stable transform). When the flag is off
// (the default) this is a no-op and physical coordinates flow through
// untouched. Document caveat: agents driving a non-primary HiDPI
// monitor with a different scale will see drift — production deploys
// should stick with physical pixels and translate at the agent layer.
func applyLogicalCoords(ctx context.Context, x, y int) (int, int) {
	if !screen.LogicalCoordsEnabled() {
		return x, y
	}
	scale := screen.PrimaryScale(ctx)
	if scale <= 0 || scale == 1.0 {
		return x, y
	}
	return int(float64(x)*scale + 0.5), int(float64(y)*scale + 0.5)
}

func Click(ctx context.Context, req ClickRequest) error {
	button, err := buttonFor(req.Button)
	if err != nil {
		return err
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	if err := Move(ctx, req.Point); err != nil {
		return err
	}
	p := platform.Current().Pointer()
	for i := 0; i < count; i++ {
		if err := p.Button(ctx, button, platform.Press); err != nil {
			return err
		}
		if err := p.Button(ctx, button, platform.Release); err != nil {
			return err
		}
	}
	return nil
}

func Drag(ctx context.Context, req DragRequest) error {
	button, err := buttonFor(req.Button)
	if err != nil {
		return err
	}
	rctx, err := ResolveContextFor(ctx, req.From, req.To)
	if err != nil {
		return err
	}
	fromX, fromY, err := contract.Resolve(req.From, rctx)
	if err != nil {
		return err
	}
	toX, toY, err := contract.Resolve(req.To, rctx)
	if err != nil {
		return err
	}
	fromX, fromY = applyLogicalCoords(ctx, fromX, fromY)
	toX, toY = applyLogicalCoords(ctx, toX, toY)
	// Validate BOTH endpoints before pressing so we never strand the
	// button down with a mid-drag POINT_OUT_OF_BOUNDS. Interior points
	// lie on the segment between two in-bounds endpoints, so they need
	// no per-step re-validation.
	bounds, err := platform.Current().Screen().ScreenBounds(ctx)
	if err != nil {
		return err
	}
	if !bounds.Contains(fromX, fromY) || !bounds.Contains(toX, toY) {
		return contract.Validation("POINT_OUT_OF_BOUNDS", "drag coordinates must be inside the screen", map[string]any{"from": req.From, "to": req.To, "screen": bounds})
	}
	duration := time.Duration(req.DurationMS) * time.Millisecond
	if duration <= 0 {
		duration = 250 * time.Millisecond
	}
	p := platform.Current().Pointer()
	if err := p.MoveTo(ctx, fromX, fromY); err != nil {
		return err
	}
	if err := p.Button(ctx, button, platform.Press); err != nil {
		return err
	}
	steps := 12
	for i := 1; i <= steps; i++ {
		x := fromX + (toX-fromX)*i/steps
		y := fromY + (toY-fromY)*i/steps
		if err := p.MoveTo(ctx, x, y); err != nil {
			return err
		}
		time.Sleep(duration / time.Duration(steps))
	}
	return p.Button(ctx, button, platform.Release)
}

func Scroll(ctx context.Context, req ScrollRequest) error {
	amount := req.Amount
	if amount <= 0 {
		amount = 3
	}
	var dx, dy int
	switch strings.ToLower(req.Direction) {
	case "up":
		dy = -amount
	case "down", "":
		dy = amount
	case "left":
		dx = -amount
	case "right":
		dx = amount
	default:
		return contract.Validation("INVALID_SCROLL_DIRECTION", "scroll direction must be up, down, left, or right", map[string]any{"direction": req.Direction})
	}
	if err := Move(ctx, req.Point); err != nil {
		return err
	}
	return platform.Current().Pointer().Scroll(ctx, dx, dy)
}

func PressKey(ctx context.Context, chord string) error {
	keys, err := parseChord(chord)
	if err != nil {
		return err
	}
	return platform.Current().Keyboard().KeyCombo(ctx, keys)
}

// TypeText is the legacy entry point. It defaults to auto routing and
// discards the rich result. New callers should prefer TypeTextWith.
func TypeText(ctx context.Context, text string) error {
	_, err := TypeTextWith(ctx, TypeTextRequest{Text: text, Via: TypeTextViaAuto})
	return err
}

// TypeTextWith is the v0.2 entry point with explicit via routing.
//
// auto policy is FIXED and documented here verbatim:
//
//	len(text) > 64 OR contains non-ASCII OR contains control chars → paste
//	else → xtest
//
// When an IME is active, "auto" always routes through paste regardless
// of length. "xtest" with an active IME is rejected with
// INPUT_IME_ACTIVE; "paste" is always allowed.
func TypeTextWith(ctx context.Context, req TypeTextRequest) (TypeTextResult, error) {
	via := strings.ToLower(strings.TrimSpace(req.Via))
	switch via {
	case "", TypeTextViaAuto:
		via = TypeTextViaAuto
	case TypeTextViaXTest, TypeTextViaPaste:
	default:
		return TypeTextResult{}, contract.Validation("INVALID_TYPE_TEXT_VIA", "via must be xtest, paste, or auto", map[string]any{"via": req.Via})
	}

	ime := clipboard.DetectIME()
	result := TypeTextResult{IMEActive: ime.Active, IMEEngine: ime.Engine}

	// Resolve auto → concrete route now so callers can branch on
	// result.Via after success.
	if via == TypeTextViaAuto {
		via = autoTypeTextRoute(req.Text, ime.Active)
	} else if via == TypeTextViaXTest && ime.Active {
		return result, contract.Precondition("INPUT_IME_ACTIVE", "an Input Method Editor is active; xtest input may be intercepted", map[string]any{
			"engine":    ime.Engine,
			"recommend": "via:paste",
		})
	}
	result.Via = via

	switch via {
	case TypeTextViaPaste:
		restored, err := typeTextViaPaste(ctx, req.Text)
		if err != nil {
			return result, err
		}
		result.ClipboardRestored = restored
		return result, nil
	case TypeTextViaXTest:
		if err := platform.Current().Keyboard().TypeText(ctx, req.Text); err != nil {
			return result, err
		}
		return result, nil
	default:
		// Unreachable — keeps the compiler honest.
		return result, contract.Validation("INVALID_TYPE_TEXT_VIA", "via must be xtest, paste, or auto", map[string]any{"via": via})
	}
}

// Route-reason tags surfaced by DecideTypeTextRoute. Stable strings —
// agents may compare these literally when previewing a type_text via
// dry-run. Keep in sync with the documented values in conventions.yaml.
const (
	TypeTextReasonIMEActive  = "ime-active"
	TypeTextReasonLengthGt64 = "len>64"
	TypeTextReasonNonASCII   = "non-ascii"
	TypeTextReasonControl    = "control-char"
	TypeTextReasonShortASCII = "short-ascii"
)

// DecideTypeTextRoute is the pure, read-only via-decision used by the
// auto policy. It returns the concrete route ("xtest" | "paste") and a
// stable reason tag explaining why. Used by:
//
//   - TypeTextWith's auto branch (real execution path).
//   - The pipeline dry-run preview, which calls this to surface
//     details.via + details.route_reason without performing XTest /
//     paste side effects.
//
// Auto policy (FIXED — pinned by tests):
//
//	IME active                              → paste, "ime-active"
//	len(text) > 64                          → paste, "len>64"
//	contains non-ASCII (r > 0x7e)           → paste, "non-ascii"
//	contains control char other than \n,\t  → paste, "control-char"
//	otherwise                               → xtest, "short-ascii"
func DecideTypeTextRoute(text string, imeActive bool) (route, reason string) {
	if imeActive {
		return TypeTextViaPaste, TypeTextReasonIMEActive
	}
	if len(text) > 64 {
		return TypeTextViaPaste, TypeTextReasonLengthGt64
	}
	for _, r := range text {
		if r > 0x7e {
			return TypeTextViaPaste, TypeTextReasonNonASCII
		}
		// Allow tab and newline as printable control chars in the auto
		// policy — they are routine in pasted multi-line buffers. Any
		// other control char forces paste.
		if r < 0x20 && r != '\n' && r != '\t' {
			return TypeTextViaPaste, TypeTextReasonControl
		}
	}
	return TypeTextViaXTest, TypeTextReasonShortASCII
}

// autoTypeTextRoute is the legacy in-package alias retained so the
// existing call sites (and tests) keep working. It delegates to
// DecideTypeTextRoute and discards the reason.
func autoTypeTextRoute(text string, imeActive bool) string {
	route, _ := DecideTypeTextRoute(text, imeActive)
	return route
}

// typeTextViaPaste saves the current CLIPBOARD value, writes text,
// sends ctrl+v, then restores the previous clipboard content (best
// effort). The returned bool reports whether the restore actually fired.
func typeTextViaPaste(ctx context.Context, text string) (bool, error) {
	_, restore, err := clipboard.SaveAndWrite(ctx, clipboard.SelectionClipboard, text, clipboard.MimeTextPlain)
	if err != nil {
		return false, err
	}
	// Tiny settle delay so the clipboard ownership change propagates
	// before the target app processes ctrl+v.
	time.Sleep(20 * time.Millisecond)
	if err := PressKey(ctx, "ctrl+v"); err != nil {
		// Still attempt restore so we don't strand the user's clipboard.
		restored, _ := restore(ctx)
		return restored, err
	}
	// Give the receiving app a brief window to read the selection
	// before we overwrite it with the restored value.
	time.Sleep(50 * time.Millisecond)
	restored, restoreErr := restore(ctx)
	if restoreErr != nil {
		// Restore failed but paste succeeded — surface as a soft warning
		// via restored:false; do not fail the action.
		return false, nil
	}
	return restored, nil
}

func buttonFor(name string) (platform.Button, error) {
	switch strings.ToLower(name) {
	case "", "left":
		return platform.ButtonLeft, nil
	case "middle":
		return platform.ButtonMiddle, nil
	case "right":
		return platform.ButtonRight, nil
	default:
		return 0, contract.Validation("INVALID_BUTTON", "button must be left, middle, or right", map[string]any{"button": name})
	}
}

func parseChord(chord string) ([]platform.Key, error) {
	var keys []platform.Key
	for _, part := range strings.Split(chord, "+") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			keys = append(keys, platform.Key(part))
		}
	}
	if len(keys) == 0 {
		return nil, contract.Validation("INVALID_KEY", "key chord is empty", nil)
	}
	return keys, nil
}
