package input

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/screen"
	"github.com/1broseidon/mc/internal/window"
	"github.com/1broseidon/mc/internal/x11"
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

func Move(ctx context.Context, point contract.Point) error {
	// All coordinate-space translation routes through contract.Resolve.
	rctx, err := ResolveContextFor(ctx, point)
	if err != nil {
		return err
	}
	x, y, err := contract.Resolve(point, rctx)
	if err != nil {
		return err
	}
	x, y = applyLogicalCoords(ctx, x, y)
	return withDisplay(ctx, func(d *x11.Display) error {
		if !x11.ScreenBounds(d).Contains(x, y) {
			return contract.Validation("POINT_OUT_OF_BOUNDS", "screen coordinate is outside the screen", map[string]any{"x": x, "y": y, "screen": x11.ScreenBounds(d)})
		}
		xtest.FakeInput(d.Conn, xproto.MotionNotify, 0, xproto.TimeCurrentTime, d.Screen.Root, int16(x), int16(y), 0)
		d.Conn.Sync()
		return nil
	})
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
	button, err := buttonNumber(req.Button)
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
	return withDisplay(ctx, func(d *x11.Display) error {
		for i := 0; i < count; i++ {
			xtest.FakeInput(d.Conn, xproto.ButtonPress, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			xtest.FakeInput(d.Conn, xproto.ButtonRelease, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
		}
		d.Conn.Sync()
		return nil
	})
}

func Drag(ctx context.Context, req DragRequest) error {
	button, err := buttonNumber(req.Button)
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
	duration := time.Duration(req.DurationMS) * time.Millisecond
	if duration <= 0 {
		duration = 250 * time.Millisecond
	}
	return withDisplay(ctx, func(d *x11.Display) error {
		bounds := x11.ScreenBounds(d)
		if !bounds.Contains(fromX, fromY) || !bounds.Contains(toX, toY) {
			return contract.Validation("POINT_OUT_OF_BOUNDS", "drag coordinates must be inside the screen", map[string]any{"from": req.From, "to": req.To, "screen": bounds})
		}
		steps := 12
		xtest.FakeInput(d.Conn, xproto.MotionNotify, 0, xproto.TimeCurrentTime, d.Screen.Root, int16(fromX), int16(fromY), 0)
		xtest.FakeInput(d.Conn, xproto.ButtonPress, button, xproto.TimeCurrentTime, d.Screen.Root, int16(fromX), int16(fromY), 0)
		for i := 1; i <= steps; i++ {
			x := fromX + (toX-fromX)*i/steps
			y := fromY + (toY-fromY)*i/steps
			xtest.FakeInput(d.Conn, xproto.MotionNotify, 0, xproto.TimeCurrentTime, d.Screen.Root, int16(x), int16(y), 0)
			d.Conn.Sync()
			time.Sleep(duration / time.Duration(steps))
		}
		xtest.FakeInput(d.Conn, xproto.ButtonRelease, button, xproto.TimeCurrentTime, d.Screen.Root, int16(toX), int16(toY), 0)
		d.Conn.Sync()
		return nil
	})
}

func Scroll(ctx context.Context, req ScrollRequest) error {
	button := byte(5)
	switch strings.ToLower(req.Direction) {
	case "up":
		button = 4
	case "down", "":
		button = 5
	case "left":
		button = 6
	case "right":
		button = 7
	default:
		return contract.Validation("INVALID_SCROLL_DIRECTION", "scroll direction must be up, down, left, or right", map[string]any{"direction": req.Direction})
	}
	amount := req.Amount
	if amount <= 0 {
		amount = 3
	}
	if err := Move(ctx, req.Point); err != nil {
		return err
	}
	return withDisplay(ctx, func(d *x11.Display) error {
		for i := 0; i < amount; i++ {
			xtest.FakeInput(d.Conn, xproto.ButtonPress, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			xtest.FakeInput(d.Conn, xproto.ButtonRelease, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
		}
		d.Conn.Sync()
		return nil
	})
}

func PressKey(ctx context.Context, chord string) error {
	keys, err := parseChord(chord)
	if err != nil {
		return err
	}
	return withDisplay(ctx, func(d *x11.Display) error {
		mapper, err := newKeyMapper(d)
		if err != nil {
			return err
		}
		var codes []keyPress
		for _, key := range keys {
			ks := keysymForName(key)
			press, err := mapper.lookup(ks)
			if err != nil {
				// For single printable characters, surface the same
				// INPUT_LAYOUT_UNREACHABLE error the type_text xtest
				// path uses — the chord layer is the public seam for
				// both presses and types of a single rune.
				if runes := []rune(key); len(runes) == 1 && ks != 0 {
					return contract.Validation("INPUT_LAYOUT_UNREACHABLE", "character is not reachable from the active XKB layout", map[string]any{
						"rune":      key,
						"codepoint": uint32(runes[0]),
						"recommend": "via:paste (type_text)",
					})
				}
				return err
			}
			codes = append(codes, press)
		}
		for _, press := range codes {
			if press.shift {
				shift, err := mapper.lookup(xkShiftL)
				if err != nil {
					return err
				}
				xtest.FakeInput(d.Conn, xproto.KeyPress, byte(shift.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			}
			xtest.FakeInput(d.Conn, xproto.KeyPress, byte(press.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
		}
		for i := len(codes) - 1; i >= 0; i-- {
			xtest.FakeInput(d.Conn, xproto.KeyRelease, byte(codes[i].code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			if codes[i].shift {
				shift, err := mapper.lookup(xkShiftL)
				if err != nil {
					return err
				}
				xtest.FakeInput(d.Conn, xproto.KeyRelease, byte(shift.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			}
		}
		d.Conn.Sync()
		return nil
	})
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
		if err := typeTextViaXTest(ctx, req.Text); err != nil {
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

// typeTextViaXTest is the layout-aware XTest path. It refuses to type
// characters that the active XKB keymap cannot produce (returning
// INPUT_LAYOUT_UNREACHABLE) instead of silently emitting wrong chars.
func typeTextViaXTest(ctx context.Context, text string) error {
	return withDisplay(ctx, func(d *x11.Display) error {
		mapper, err := newKeyMapper(d)
		if err != nil {
			return err
		}
		shift, _ := mapper.lookup(xkShiftL)
		// First pass: validate every rune is reachable from the current
		// keymap. We never partially type; either the full string maps
		// or we surface INPUT_LAYOUT_UNREACHABLE with the offending rune.
		for i, r := range text {
			ks := keysymForRune(r)
			if ks == 0 {
				return contract.Validation("INPUT_LAYOUT_UNREACHABLE", "character is not reachable from the active XKB layout", map[string]any{
					"index":     i,
					"rune":      string(r),
					"codepoint": uint32(r),
					"recommend": "via:paste",
				})
			}
			if _, err := mapper.lookup(ks); err != nil {
				return contract.Validation("INPUT_LAYOUT_UNREACHABLE", "character is not reachable from the active XKB layout", map[string]any{
					"index":     i,
					"rune":      string(r),
					"codepoint": uint32(r),
					"recommend": "via:paste",
				})
			}
		}
		for _, r := range text {
			ks := keysymForRune(r)
			press, err := mapper.lookup(ks)
			if err != nil {
				return contract.Validation("INPUT_LAYOUT_UNREACHABLE", "character is not reachable from the active XKB layout", map[string]any{
					"rune": string(r), "codepoint": uint32(r), "recommend": "via:paste",
				})
			}
			if press.shift {
				xtest.FakeInput(d.Conn, xproto.KeyPress, byte(shift.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			}
			xtest.FakeInput(d.Conn, xproto.KeyPress, byte(press.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			xtest.FakeInput(d.Conn, xproto.KeyRelease, byte(press.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			if press.shift {
				xtest.FakeInput(d.Conn, xproto.KeyRelease, byte(shift.code), xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			}
		}
		d.Conn.Sync()
		return nil
	})
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

func withDisplay(ctx context.Context, fn func(*x11.Display) error) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("input operation cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := xtest.Init(d.Conn); err != nil {
		return contract.Dependency("XTEST_UNAVAILABLE", "XTest extension is not available", map[string]any{"error": err.Error()})
	}
	return fn(d)
}

func buttonNumber(name string) (byte, error) {
	switch strings.ToLower(name) {
	case "", "left":
		return 1, nil
	case "middle":
		return 2, nil
	case "right":
		return 3, nil
	default:
		return 0, contract.Validation("INVALID_BUTTON", "button must be left, middle, or right", map[string]any{"button": name})
	}
}

func parseChord(chord string) ([]string, error) {
	var keys []string
	for _, part := range strings.Split(chord, "+") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			keys = append(keys, part)
		}
	}
	if len(keys) == 0 {
		return nil, contract.Validation("INVALID_KEY", "key chord is empty", nil)
	}
	return keys, nil
}

type keyPress struct {
	code  xproto.Keycode
	shift bool
}

type keyMapper struct {
	min     xproto.Keycode
	perCode int
	keysyms []xproto.Keysym
}

func newKeyMapper(d *x11.Display) (*keyMapper, error) {
	minCode := d.Setup.MinKeycode
	maxCode := d.Setup.MaxKeycode
	count := byte(maxCode - minCode + 1)
	reply, err := xproto.GetKeyboardMapping(d.Conn, minCode, count).Reply()
	if err != nil {
		return nil, contract.Dependency("KEYMAP_UNAVAILABLE", "failed to query X11 keyboard mapping", map[string]any{"error": err.Error()})
	}
	return &keyMapper{min: minCode, perCode: int(reply.KeysymsPerKeycode), keysyms: reply.Keysyms}, nil
}

func (m *keyMapper) lookup(ks xproto.Keysym) (keyPress, error) {
	if ks == 0 {
		return keyPress{}, contract.Validation("INVALID_KEY", "key is not supported by the MVP key mapper", nil)
	}
	for i, value := range m.keysyms {
		if value != ks {
			continue
		}
		keyIndex := i / m.perCode
		column := i % m.perCode
		return keyPress{code: m.min + xproto.Keycode(keyIndex), shift: column%2 == 1}, nil
	}
	return keyPress{}, contract.Validation("KEY_UNMAPPED", "key is not available in current X11 keymap", map[string]any{"keysym": uint32(ks)})
}

const (
	xkBackspace = xproto.Keysym(0xff08)
	xkTab       = xproto.Keysym(0xff09)
	xkReturn    = xproto.Keysym(0xff0d)
	xkEscape    = xproto.Keysym(0xff1b)
	xkDelete    = xproto.Keysym(0xffff)
	xkHome      = xproto.Keysym(0xff50)
	xkLeft      = xproto.Keysym(0xff51)
	xkUp        = xproto.Keysym(0xff52)
	xkRight     = xproto.Keysym(0xff53)
	xkDown      = xproto.Keysym(0xff54)
	xkPageUp    = xproto.Keysym(0xff55)
	xkPageDown  = xproto.Keysym(0xff56)
	xkEnd       = xproto.Keysym(0xff57)
	xkShiftL    = xproto.Keysym(0xffe1)
	xkControlL  = xproto.Keysym(0xffe3)
	xkAltL      = xproto.Keysym(0xffe9)
	xkSuperL    = xproto.Keysym(0xffeb)
)

func keysymForName(name string) xproto.Keysym {
	switch strings.ToLower(name) {
	case "enter", "return":
		return xkReturn
	case "tab":
		return xkTab
	case "esc", "escape":
		return xkEscape
	case "backspace":
		return xkBackspace
	case "delete", "del":
		return xkDelete
	case "home":
		return xkHome
	case "end":
		return xkEnd
	case "pageup", "page_up":
		return xkPageUp
	case "pagedown", "page_down":
		return xkPageDown
	case "up", "arrowup":
		return xkUp
	case "down", "arrowdown":
		return xkDown
	case "left", "arrowleft":
		return xkLeft
	case "right", "arrowright":
		return xkRight
	case "ctrl", "control":
		return xkControlL
	case "alt", "option":
		return xkAltL
	case "shift":
		return xkShiftL
	case "cmd", "command", "meta", "super":
		return xkSuperL
	}
	if strings.HasPrefix(strings.ToLower(name), "f") && len(name) <= 3 {
		n := 0
		for _, r := range name[1:] {
			if r < '0' || r > '9' {
				return xproto.Keysym(0)
			}
			n = n*10 + int(r-'0')
		}
		if n >= 1 && n <= 35 {
			return xproto.Keysym(0xffbd + n)
		}
	}
	if len([]rune(name)) == 1 {
		return keysymForRune([]rune(name)[0])
	}
	return xproto.Keysym(0)
}

func keysymForRune(r rune) xproto.Keysym {
	switch r {
	case '\n':
		return xkReturn
	case '\t':
		return xkTab
	}
	if r >= 0x20 && r <= 0x7e {
		return xproto.Keysym(r)
	}
	if unicode.IsPrint(r) {
		return xproto.Keysym(r)
	}
	return xproto.Keysym(0)
}
