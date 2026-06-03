//go:build linux

package x11adapter

import (
	"context"
	"strings"
	"unicode"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// keyboard implements platform.Keyboard over XTest + the active XKB
// keymap. The service owns type_text routing (xtest vs paste), chord
// parsing into tokens, and the auto-route heuristics; this type owns the
// X11-specific keysym resolution and the layout-reachability contract
// (INPUT_LAYOUT_UNREACHABLE).
type keyboard struct{}

// TypeText types a literal string via layout-aware XTest key synthesis.
// It validates every rune is reachable from the active XKB keymap before
// emitting any keystroke, so a partial type never happens: an unreachable
// character returns INPUT_LAYOUT_UNREACHABLE and types nothing, letting
// the service fall back to the paste route.
func (keyboard) TypeText(ctx context.Context, text string) error {
	return withInputDisplay(ctx, func(d *x11.Display) error {
		mapper, err := newKeyMapper(d)
		if err != nil {
			return err
		}
		shift, _ := mapper.lookup(xkShiftL)
		// First pass: validate reachability so we never partially type.
		for i, r := range text {
			ks := keysymForRune(r)
			if ks == 0 {
				return layoutUnreachable(i, r)
			}
			if _, err := mapper.lookup(ks); err != nil {
				return layoutUnreachable(i, r)
			}
		}
		for _, r := range text {
			ks := keysymForRune(r)
			press, err := mapper.lookup(ks)
			if err != nil {
				return layoutUnreachable(-1, r)
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

// KeyCombo presses the given key tokens in order then releases them in
// reverse order (a chord). Tokens are the lowercase names produced by the
// service's chord parser ("ctrl", "enter", "a", "f5", …). A single
// printable token that the layout cannot reach returns
// INPUT_LAYOUT_UNREACHABLE so the service can recommend the paste route.
func (keyboard) KeyCombo(ctx context.Context, combo []platform.Key) error {
	if len(combo) == 0 {
		return contract.Validation("INVALID_KEY", "key chord is empty", nil)
	}
	return withInputDisplay(ctx, func(d *x11.Display) error {
		mapper, err := newKeyMapper(d)
		if err != nil {
			return err
		}
		var codes []keyPress
		for _, key := range combo {
			name := string(key)
			ks := keysymForName(name)
			press, err := mapper.lookup(ks)
			if err != nil {
				if runes := []rune(name); len(runes) == 1 && ks != 0 {
					return layoutUnreachable(-1, runes[0])
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

func layoutUnreachable(index int, r rune) error {
	details := map[string]any{
		"rune":      string(r),
		"codepoint": uint32(r),
		"recommend": "via:paste",
	}
	if index >= 0 {
		details["index"] = index
	}
	return contract.Validation("INPUT_LAYOUT_UNREACHABLE", "character is not reachable from the active XKB layout", details)
}

// --- keymap (migrated from internal/input) ---

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
