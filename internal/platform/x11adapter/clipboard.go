//go:build linux

// Clipboard backend: native X11 selection-protocol access (CLIPBOARD +
// PRIMARY, text/plain + text/uri-list). It does NOT shell out to
// xclip/xsel — a process-lifetime owner goroutine holds an invisible X11
// window, takes ownership of the selection, and answers incoming
// SelectionRequest events from other clients.
//
// This machinery is X11-specific: selection ownership dies when the owning
// client exits, which is why the adapter also implements
// platform.ClipboardDaemon (Done) so a detached owner daemon can wait for
// ownership loss. macOS NSPasteboard has no such requirement.

package x11adapter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// MIME atoms the selection owner can serve. Mirrors the service-layer
// MIME constants but lives here because the adapter maps them onto X11
// target atoms.
const (
	mimeTextPlain   = "text/plain"
	mimeTextURIList = "text/uri-list"
)

// clipboard implements platform.Clipboard (+ platform.ClipboardDaemon)
// over the X11 selection protocol. The service layer owns MIME/selection
// validation, the "both" expansion, the save/restore paste orchestration,
// and byte-count bookkeeping; this type owns the selection-owner window,
// the SelectionRequest event loop, and the ConvertSelection read path.
type clipboard struct{}

func (clipboard) Selections() []platform.Selection {
	return []platform.Selection{platform.SelectionClipboard, platform.SelectionPrimary}
}

// Write stores content for the requested selection, takes ownership of the
// selection atom, and returns once the owner change is in flight. The
// service expands "both" into two Write calls (clipboard + primary).
func (clipboard) Write(ctx context.Context, sel platform.Selection, content, mime string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("clipboard write cancelled")
	}
	o, err := instance()
	if err != nil {
		return err
	}
	atom, err := o.selectionAtom(sel)
	if err != nil {
		return err
	}
	var mimeAtom xproto.Atom
	if mime == mimeTextURIList {
		mimeAtom = o.atoms.uriList
	} else {
		mimeAtom = o.atoms.utf8String
	}
	o.mu.Lock()
	o.storage[atom] = storedSelection{content: content, mime: mime, mimeAtom: mimeAtom}
	o.mu.Unlock()
	if err := xproto.SetSelectionOwnerChecked(o.conn, o.window, atom, xproto.TimeCurrentTime).Check(); err != nil {
		return contract.Dependency("CLIPBOARD_OWNERSHIP_FAILED", "failed to take selection ownership", map[string]any{"error": err.Error()})
	}
	return nil
}

// Read retrieves the current value of the given selection via a throwaway
// X11 connection so the owner event loop is not blocked while we wait for
// the SelectionNotify reply. Returns the content plus the MIME it was
// requested under. An absent owner yields empty content with no error.
func (clipboard) Read(ctx context.Context, sel platform.Selection, mime string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", contract.Cancelled("clipboard read cancelled")
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return "", "", contract.Dependency("CLIPBOARD_DISPLAY_UNAVAILABLE", "clipboard reader could not connect to X11", map[string]any{"error": err.Error()})
	}
	defer conn.Close()
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		return "", "", contract.Dependency("CLIPBOARD_WINDOW_ALLOC_FAILED", "failed to allocate reader window id", map[string]any{"error": err.Error()})
	}
	if err := xproto.CreateWindowChecked(conn, 0, wid, screen.Root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual, xproto.CwEventMask, []uint32{uint32(xproto.EventMaskPropertyChange)}).Check(); err != nil {
		return "", "", contract.Dependency("CLIPBOARD_WINDOW_CREATE_FAILED", "failed to create reader window", map[string]any{"error": err.Error()})
	}
	defer xproto.DestroyWindow(conn, wid)

	atoms, err := internAtoms(conn)
	if err != nil {
		return "", "", err
	}
	selAtom := atoms.clipboard
	if sel == platform.SelectionPrimary {
		selAtom = atoms.primary
	}

	owner, err := xproto.GetSelectionOwner(conn, selAtom).Reply()
	if err != nil {
		return "", "", contract.Dependency("CLIPBOARD_OWNER_QUERY_FAILED", "failed to query selection owner", map[string]any{"error": err.Error()})
	}
	if owner.Owner == 0 {
		return "", mime, nil
	}

	target := atoms.utf8String
	if mime == mimeTextURIList {
		target = atoms.uriList
	}
	if err := xproto.ConvertSelectionChecked(conn, wid, selAtom, target, atoms.xmycomputer, xproto.TimeCurrentTime).Check(); err != nil {
		return "", "", contract.Dependency("CLIPBOARD_CONVERT_FAILED", "ConvertSelection request failed", map[string]any{"error": err.Error()})
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return "", "", contract.Cancelled("clipboard read cancelled")
		}
		if time.Now().After(deadline) {
			return "", "", contract.Dependency("CLIPBOARD_READ_TIMEOUT", "no SelectionNotify reply within timeout", map[string]any{"selection": string(sel)})
		}
		ev, xerr := waitForEventWithTimeout(conn, 200*time.Millisecond)
		if xerr != nil || ev == nil {
			continue
		}
		notify, ok := ev.(xproto.SelectionNotifyEvent)
		if !ok {
			continue
		}
		if notify.Property == 0 {
			return "", mime, nil
		}
		const maxBytes = 8 * 1024 * 1024
		var buf []byte
		offset := uint32(0)
		for {
			reply, err := xproto.GetProperty(conn, true, wid, notify.Property, xproto.AtomAny, offset, 65536).Reply()
			if err != nil {
				return "", "", contract.Dependency("CLIPBOARD_PROPERTY_READ_FAILED", "failed to read selection property", map[string]any{"error": err.Error()})
			}
			if reply.Type == atoms.incr {
				return "", "", contract.Dependency("CLIPBOARD_INCR_UNSUPPORTED", "INCR-transferred selections are not supported", map[string]any{"selection": string(sel)})
			}
			buf = append(buf, reply.Value...)
			if len(buf) > maxBytes {
				return "", "", contract.Dependency("CLIPBOARD_PAYLOAD_TOO_LARGE", "selection payload exceeded internal cap", map[string]any{"bytes": len(buf)})
			}
			if reply.BytesAfter == 0 {
				break
			}
			offset += uint32(len(reply.Value)) / 4
		}
		return string(buf), mime, nil
	}
}

// Done implements platform.ClipboardDaemon: it returns a channel closed
// when this process loses selection ownership.
func (clipboard) Done() (<-chan struct{}, error) {
	o, err := instance()
	if err != nil {
		return nil, err
	}
	return o.Done(), nil
}

// Probe reports clipboard readiness for doctor. Best-effort: a session
// without a running X11 display reports not-ready with the underlying X11
// error. Required:false in all cases.
func (clipboard) Probe() contract.BackendStatus {
	now := time.Now().UTC()
	if _, err := instance(); err != nil {
		return contract.BackendStatus{Name: "clipboard", Ready: false, Required: false, Message: err.Error(), CheckedAt: now}
	}
	return contract.BackendStatus{
		Name:         "clipboard",
		Ready:        true,
		Required:     false,
		Message:      "available",
		Capabilities: []string{"clipboard", "primary", "uri_list"},
		CheckedAt:    now,
	}
}

// --- selection-owner machinery (migrated from internal/clipboard) ---

// Owner is the package-level singleton that owns the clipboard selection
// for the lifetime of the process. It maintains its own X11 connection so
// the SelectionRequest event loop never competes with read-side reply
// pumps.
type Owner struct {
	mu       sync.RWMutex
	conn     *xgb.Conn
	window   xproto.Window
	atoms    atomSet
	storage  map[xproto.Atom]storedSelection
	started  bool
	stopCh   chan struct{}
	lostMu   sync.Mutex
	lostCh   chan struct{}
	lostOnce sync.Once
}

type storedSelection struct {
	content  string
	mime     string
	mimeAtom xproto.Atom
}

type atomSet struct {
	clipboard   xproto.Atom
	primary     xproto.Atom
	targets     xproto.Atom
	utf8String  xproto.Atom
	textPlain   xproto.Atom
	uriList     xproto.Atom
	xmycomputer xproto.Atom
	incr        xproto.Atom
}

var (
	singletonMu sync.Mutex
	singleton   *Owner
)

func instance() (*Owner, error) {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	if singleton != nil && singleton.started {
		return singleton, nil
	}
	o := &Owner{storage: map[xproto.Atom]storedSelection{}, stopCh: make(chan struct{}), lostCh: make(chan struct{})}
	if err := o.start(); err != nil {
		return nil, err
	}
	singleton = o
	return o, nil
}

func (o *Owner) start() error {
	conn, err := xgb.NewConn()
	if err != nil {
		return contract.Dependency("CLIPBOARD_DISPLAY_UNAVAILABLE", "clipboard owner could not connect to X11", map[string]any{"error": err.Error()})
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return contract.Dependency("CLIPBOARD_WINDOW_ALLOC_FAILED", "failed to allocate clipboard owner window id", map[string]any{"error": err.Error()})
	}
	mask := uint32(xproto.CwEventMask)
	values := []uint32{uint32(xproto.EventMaskPropertyChange)}
	if err := xproto.CreateWindowChecked(conn, 0, wid, screen.Root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual, mask, values).Check(); err != nil {
		conn.Close()
		return contract.Dependency("CLIPBOARD_WINDOW_CREATE_FAILED", "failed to create clipboard owner window", map[string]any{"error": err.Error()})
	}
	atoms, err := internAtoms(conn)
	if err != nil {
		_ = xproto.DestroyWindowChecked(conn, wid).Check()
		conn.Close()
		return err
	}
	o.conn = conn
	o.window = wid
	o.atoms = atoms
	o.started = true
	go o.eventLoop()
	return nil
}

func internAtoms(conn *xgb.Conn) (atomSet, error) {
	intern := func(name string) (xproto.Atom, error) {
		reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			return 0, contract.Dependency("CLIPBOARD_ATOM_INTERN_FAILED", "failed to intern X11 atom", map[string]any{"atom": name, "error": err.Error()})
		}
		return reply.Atom, nil
	}
	a := atomSet{}
	var err error
	if a.clipboard, err = intern("CLIPBOARD"); err != nil {
		return a, err
	}
	a.primary = xproto.AtomPrimary
	if a.targets, err = intern("TARGETS"); err != nil {
		return a, err
	}
	if a.utf8String, err = intern("UTF8_STRING"); err != nil {
		return a, err
	}
	if a.textPlain, err = intern("text/plain;charset=utf-8"); err != nil {
		return a, err
	}
	if a.uriList, err = intern("text/uri-list"); err != nil {
		return a, err
	}
	if a.xmycomputer, err = intern("_MYCOMPUTER_CLIPBOARD"); err != nil {
		return a, err
	}
	if a.incr, err = intern("INCR"); err != nil {
		return a, err
	}
	return a, nil
}

func (o *Owner) selectionAtom(sel platform.Selection) (xproto.Atom, error) {
	switch sel {
	case platform.SelectionClipboard:
		return o.atoms.clipboard, nil
	case platform.SelectionPrimary:
		return o.atoms.primary, nil
	default:
		return 0, contract.Validation("CLIPBOARD_SELECTION_INVALID", "selection must be clipboard or primary", map[string]any{"selection": string(sel)})
	}
}

func (o *Owner) eventLoop() {
	for {
		select {
		case <-o.stopCh:
			return
		default:
		}
		ev, xerr := o.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			return
		}
		if xerr != nil {
			continue
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			o.handleSelectionRequest(e)
		case xproto.SelectionClearEvent:
			o.handleSelectionClear(e)
		}
	}
}

func (o *Owner) handleSelectionClear(e xproto.SelectionClearEvent) {
	o.mu.Lock()
	delete(o.storage, e.Selection)
	o.mu.Unlock()
	o.lostOnce.Do(func() {
		o.lostMu.Lock()
		ch := o.lostCh
		o.lostMu.Unlock()
		close(ch)
	})
}

func (o *Owner) Done() <-chan struct{} {
	o.lostMu.Lock()
	defer o.lostMu.Unlock()
	return o.lostCh
}

func (o *Owner) handleSelectionRequest(e xproto.SelectionRequestEvent) {
	o.mu.RLock()
	stored, ok := o.storage[e.Selection]
	o.mu.RUnlock()

	reply := xproto.SelectionNotifyEvent{
		Time:      e.Time,
		Requestor: e.Requestor,
		Selection: e.Selection,
		Target:    e.Target,
		Property:  0,
	}
	property := e.Property
	if property == 0 {
		property = e.Target
	}

	if !ok {
		o.sendSelectionNotify(reply)
		return
	}

	switch e.Target {
	case o.atoms.targets:
		targets := []xproto.Atom{
			o.atoms.targets,
			o.atoms.utf8String,
			o.atoms.textPlain,
			xproto.AtomString,
		}
		if stored.mime == mimeTextURIList {
			targets = append(targets, o.atoms.uriList)
		}
		buf := make([]byte, 4*len(targets))
		for i, atom := range targets {
			xgb.Put32(buf[i*4:], uint32(atom))
		}
		if err := xproto.ChangePropertyChecked(o.conn, xproto.PropModeReplace, e.Requestor, property, xproto.AtomAtom, 32, uint32(len(targets)), buf).Check(); err == nil {
			reply.Property = property
		}
	case o.atoms.utf8String, o.atoms.textPlain, xproto.AtomString:
		if stored.mime == mimeTextPlain || stored.mime == mimeTextURIList {
			data := []byte(stored.content)
			if err := xproto.ChangePropertyChecked(o.conn, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(data)), data).Check(); err == nil {
				reply.Property = property
			}
		}
	case o.atoms.uriList:
		if stored.mime == mimeTextURIList {
			data := []byte(stored.content)
			if err := xproto.ChangePropertyChecked(o.conn, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(data)), data).Check(); err == nil {
				reply.Property = property
			}
		}
	}
	o.sendSelectionNotify(reply)
}

func (o *Owner) sendSelectionNotify(ev xproto.SelectionNotifyEvent) {
	_ = xproto.SendEventChecked(o.conn, false, ev.Requestor, 0, string(ev.Bytes())).Check()
}

func waitForEventWithTimeout(conn *xgb.Conn, d time.Duration) (xgb.Event, error) {
	type result struct {
		ev  xgb.Event
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ev, err := conn.WaitForEvent()
		ch <- result{ev, err}
	}()
	select {
	case r := <-ch:
		return r.ev, r.err
	case <-time.After(d):
		return nil, errors.New("timeout")
	}
}
