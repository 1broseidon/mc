//go:build linux

package x11adapter

import (
	"context"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// pointer implements platform.Pointer over XTest. It owns only the raw
// event injection; coordinate resolution, logical-coords scaling, drag
// interpolation, and bounds validation remain in the input service so
// those contracts stay identical across platforms.
type pointer struct{}

// MoveTo warps the pointer to an absolute screen coordinate via an XTest
// MotionNotify on the root window.
func (pointer) MoveTo(ctx context.Context, x, y int) error {
	return withInputDisplay(ctx, func(d *x11.Display) error {
		xtest.FakeInput(d.Conn, xproto.MotionNotify, 0, xproto.TimeCurrentTime, d.Screen.Root, int16(x), int16(y), 0)
		d.Conn.Sync()
		return nil
	})
}

// Button presses or releases a pointer button at the current pointer
// position. The service composes clicks and drags from Press/Release pairs.
func (pointer) Button(ctx context.Context, btn platform.Button, action platform.PressAction) error {
	code, err := buttonCode(btn)
	if err != nil {
		return err
	}
	return withInputDisplay(ctx, func(d *x11.Display) error {
		xtest.FakeInput(d.Conn, buttonEventType(action), code, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
		d.Conn.Sync()
		return nil
	})
}

// Scroll emits scroll-wheel button events. X11 models scrolling as button
// 4 (up), 5 (down), 6 (left), 7 (right); each tick is a press+release.
// Positive dy scrolls down, positive dx scrolls right.
func (pointer) Scroll(ctx context.Context, dx, dy int) error {
	return withInputDisplay(ctx, func(d *x11.Display) error {
		emit := func(button byte, n int) {
			for i := 0; i < n; i++ {
				xtest.FakeInput(d.Conn, xproto.ButtonPress, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
				xtest.FakeInput(d.Conn, xproto.ButtonRelease, button, xproto.TimeCurrentTime, d.Screen.Root, 0, 0, 0)
			}
		}
		if dy > 0 {
			emit(5, dy)
		} else if dy < 0 {
			emit(4, -dy)
		}
		if dx > 0 {
			emit(7, dx)
		} else if dx < 0 {
			emit(6, -dx)
		}
		d.Conn.Sync()
		return nil
	})
}

func buttonCode(btn platform.Button) (byte, error) {
	switch btn {
	case platform.ButtonLeft:
		return 1, nil
	case platform.ButtonMiddle:
		return 2, nil
	case platform.ButtonRight:
		return 3, nil
	default:
		return 0, contract.Validation("INVALID_BUTTON", "button must be left, middle, or right", map[string]any{"button": int(btn)})
	}
}

func buttonEventType(action platform.PressAction) byte {
	if action == platform.Release {
		return xproto.ButtonRelease
	}
	return xproto.ButtonPress
}
