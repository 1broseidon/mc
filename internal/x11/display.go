package x11

import (
	"os"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/contract"
)

type Display struct {
	Conn        *xgb.Conn
	Setup       *xproto.SetupInfo
	Screen      *xproto.ScreenInfo
	DisplayName string
}

func Open() (*Display, error) {
	name := os.Getenv("DISPLAY")
	var (
		conn *xgb.Conn
		err  error
	)
	if name == "" {
		return nil, contract.Precondition("DISPLAY_UNSET", "DISPLAY is not set; MyComputer needs an X11 display", nil)
	}
	conn, err = xgb.NewConnDisplay(name)
	if err != nil {
		return nil, contract.Dependency("DISPLAY_UNAVAILABLE", "cannot connect to X11 display", map[string]any{"display": name, "error": err.Error()})
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	return &Display{Conn: conn, Setup: setup, Screen: screen, DisplayName: name}, nil
}

func (d *Display) Close() {
	if d != nil && d.Conn != nil {
		d.Conn.Close()
	}
}

func Probe() []contract.BackendStatus {
	now := time.Now().UTC()
	statuses := []contract.BackendStatus{
		envStatus("DISPLAY", os.Getenv("DISPLAY") != "", true, now),
		envStatus("XAUTHORITY", os.Getenv("XAUTHORITY") != "", false, now),
	}
	d, err := Open()
	if err != nil {
		statuses = append(statuses, contract.BackendStatus{
			Name:      "x11",
			Ready:     false,
			Required:  true,
			Message:   err.Error(),
			CheckedAt: now,
		})
		return statuses
	}
	defer d.Close()
	statuses = append(statuses, contract.BackendStatus{
		Name:      "x11",
		Ready:     true,
		Required:  true,
		Message:   "connected",
		Details:   map[string]any{"display": d.DisplayName, "root": uint32(d.Screen.Root), "width": d.Screen.WidthInPixels, "height": d.Screen.HeightInPixels},
		CheckedAt: now,
	})
	statuses = append(statuses, extensionStatus("xtest", true, xtest.Init(d.Conn), now))
	statuses = append(statuses, extensionStatus("randr", true, randr.Init(d.Conn), now))
	statuses = append(statuses, extensionStatus("xfixes", false, xfixes.Init(d.Conn), now))
	return statuses
}

func envStatus(name string, ready bool, required bool, now time.Time) contract.BackendStatus {
	message := "set"
	if !ready {
		message = "not set"
	}
	return contract.BackendStatus{Name: name, Ready: ready, Required: required, Message: message, CheckedAt: now}
}

func extensionStatus(name string, required bool, err error, now time.Time) contract.BackendStatus {
	if err != nil {
		return contract.BackendStatus{Name: name, Ready: false, Required: required, Message: err.Error(), CheckedAt: now}
	}
	return contract.BackendStatus{Name: name, Ready: true, Required: required, Message: "available", CheckedAt: now}
}

func ScreenBounds(d *Display) contract.Bounds {
	return contract.Bounds{X: 0, Y: 0, Width: int(d.Screen.WidthInPixels), Height: int(d.Screen.HeightInPixels)}
}

func RootVisual(d *Display) (xproto.VisualInfo, bool) {
	for _, depth := range d.Screen.AllowedDepths {
		for _, visual := range depth.Visuals {
			if visual.VisualId == d.Screen.RootVisual {
				return visual, true
			}
		}
	}
	return xproto.VisualInfo{}, false
}

func PixmapFormat(d *Display, depth byte) (xproto.Format, bool) {
	for _, format := range d.Setup.PixmapFormats {
		if format.Depth == depth {
			return format, true
		}
	}
	return xproto.Format{}, false
}

func InternAtom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Atom, nil
}
