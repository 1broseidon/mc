package x11

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/contract"
)

// x11SocketDir is the canonical X11 unix-socket directory. Override in
// tests via AutoDetectDisplayIn() rather than mutating this var directly.
const x11SocketDir = "/tmp/.X11-unix"

// x11SocketName matches the X11 server socket pattern "X<N>" where N is
// the display number (typically 0, 1, 2, …). Anchored to reject
// adjacent helper sockets some compositors drop in the same directory.
var x11SocketName = regexp.MustCompile(`^X(\d+)$`)

// AutoDetectResult describes the outcome of an auto-probe over
// /tmp/.X11-unix/. Exactly one of Display / Ambiguous / Empty is the
// authoritative outcome (in that priority order):
//
//   - Display != "" → exactly one live socket; safe to os.Setenv("DISPLAY", Display).
//   - len(Ambiguous) > 1 → multiple live sockets; caller must NOT auto-set.
//   - Empty=true → no live sockets found.
//
// Source records the socket path (e.g. /tmp/.X11-unix/X1) that produced
// the singular Display value, for surfacing in doctor messages.
type AutoDetectResult struct {
	Display   string
	Source    string
	Ambiguous []string
	Empty     bool
}

// AutoDetectDisplay scans /tmp/.X11-unix/ for active X server sockets
// and returns the canonical result. It does NOT mutate process env —
// callers decide whether to os.Setenv based on the result. Probe is
// cheap: net.Dial("unix", ...) with a 100ms timeout per candidate.
func AutoDetectDisplay() AutoDetectResult {
	return AutoDetectDisplayIn(x11SocketDir, 100*time.Millisecond)
}

// AutoDetectDisplayIn is the parameterised form of AutoDetectDisplay
// used by tests. The dir argument is the X11 socket directory and
// perCandidateTimeout caps each individual unix-socket dial.
func AutoDetectDisplayIn(dir string, perCandidateTimeout time.Duration) AutoDetectResult {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return AutoDetectResult{Empty: true}
	}
	type candidate struct {
		n      int
		socket string
	}
	var live []candidate
	for _, e := range entries {
		m := x11SocketName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		sockPath := filepath.Join(dir, e.Name())
		conn, err := net.DialTimeout("unix", sockPath, perCandidateTimeout)
		if err != nil {
			continue
		}
		_ = conn.Close()
		live = append(live, candidate{n: n, socket: sockPath})
	}
	if len(live) == 0 {
		return AutoDetectResult{Empty: true}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].n < live[j].n })
	if len(live) == 1 {
		return AutoDetectResult{
			Display: ":" + strconv.Itoa(live[0].n),
			Source:  live[0].socket,
		}
	}
	amb := make([]string, 0, len(live))
	for _, c := range live {
		amb = append(amb, ":"+strconv.Itoa(c.n))
	}
	return AutoDetectResult{Ambiguous: amb}
}

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
