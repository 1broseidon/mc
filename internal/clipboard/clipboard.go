// Package clipboard implements native X11 selection-protocol clipboard
// access (CLIPBOARD + PRIMARY, text/plain + text/uri-list). It does NOT
// shell out to xclip/xsel — instead a process-lifetime owner goroutine
// holds an invisible X11 window, takes ownership of the selection, and
// answers incoming SelectionRequest events from other clients.
package clipboard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
)

// Supported MIME types. text/plain is the default for text payloads;
// text/uri-list carries a newline-separated list of file URIs.
const (
	MimeTextPlain   = "text/plain"
	MimeTextURIList = "text/uri-list"
)

// Selection names accepted on the wire. "both" writes to CLIPBOARD and
// PRIMARY at the same time and is only valid for Write.
const (
	SelectionClipboard = "clipboard"
	SelectionPrimary   = "primary"
	SelectionBoth      = "both"
)

// Result envelopes returned to callers (CLI/MCP/pipeline).

type ReadResult struct {
	Content   string `json:"content"`
	Mime      string `json:"mime"`
	Selection string `json:"selection"`
	// Bytes is the byte length of the returned payload. Surfaced so the
	// audit log can record write/read sizes without leaking content.
	Bytes int `json:"bytes"`
}

type WriteResult struct {
	Selection string `json:"selection"`
	Mime      string `json:"mime"`
	Bytes     int    `json:"bytes"`
}

// Owner is the package-level singleton that owns the clipboard selection
// for the lifetime of the process. It maintains its own X11 connection
// so the SelectionRequest event loop never competes with the main
// connection's reply pump.
type Owner struct {
	mu      sync.RWMutex
	conn    *xgb.Conn
	window  xproto.Window
	atoms   atomSet
	storage map[xproto.Atom]storedSelection // keyed by selection atom
	started bool
	stopCh  chan struct{}
	// lostMu guards the lost-ownership channel. lostCh is closed by
	// handleSelectionClear when this owner loses any selection it was
	// holding. lostOnce ensures the close happens at most once even if
	// multiple SelectionClear events arrive (e.g., when "both" was used
	// and PRIMARY+CLIPBOARD each generate a clear).
	lostMu   sync.Mutex
	lostCh   chan struct{}
	lostOnce sync.Once
}

type storedSelection struct {
	content string
	mime    string
	// mimeAtom is the X11 atom matching the MIME type (UTF8_STRING for
	// text/plain, or the text/uri-list atom).
	mimeAtom xproto.Atom
}

type atomSet struct {
	clipboard   xproto.Atom
	primary     xproto.Atom
	targets     xproto.Atom
	utf8String  xproto.Atom
	textPlain   xproto.Atom
	uriList     xproto.Atom
	xmycomputer xproto.Atom // _MYCOMPUTER_CLIPBOARD: private property for receiving data
	incr        xproto.Atom
}

var (
	singletonMu sync.Mutex
	singleton   *Owner
)

// instance returns the lazily-initialized process-wide Owner. It opens
// the dedicated X11 connection and starts the SelectionRequest event
// loop on first use. Subsequent calls reuse the same instance.
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

// start opens the dedicated X11 connection, creates the owner window,
// interns atoms, and launches the event loop.
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
	// 1x1 InputOnly off-screen window. PropertyChange events are required
	// to drive INCR transfer machinery on receive (we use INCR-less reads
	// for now but enable the mask so we can extend later without churn).
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

// eventLoop reads events from the owner connection and answers
// SelectionRequest events. It exits when the connection closes or the
// stop channel is signalled.
func (o *Owner) eventLoop() {
	for {
		select {
		case <-o.stopCh:
			return
		default:
		}
		ev, xerr := o.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			// Connection closed.
			return
		}
		if xerr != nil {
			// Non-fatal protocol error; keep looping.
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
	// Signal any waiter (e.g., the daemon's runClipboardOwnerDaemon
	// select) that we've lost ownership. Close-once semantics: the
	// channel never re-opens, matching the daemon lifecycle (one-shot
	// "I no longer own the selection" notification).
	o.lostOnce.Do(func() {
		o.lostMu.Lock()
		ch := o.lostCh
		o.lostMu.Unlock()
		close(ch)
	})
}

// Done returns a channel that is closed the first time this process'
// owner goroutine receives a SelectionClear event (i.e., another X11
// client took ownership of a selection we were holding). Used by the
// detached owner daemon to exit cleanly when it loses ownership.
func (o *Owner) Done() <-chan struct{} {
	o.lostMu.Lock()
	defer o.lostMu.Unlock()
	return o.lostCh
}

// Done returns the package-singleton owner's Done channel. Lazily
// initializes the owner so callers do not need to call Write/Read first.
func Done() (<-chan struct{}, error) {
	o, err := instance()
	if err != nil {
		return nil, err
	}
	return o.Done(), nil
}

func (o *Owner) handleSelectionRequest(e xproto.SelectionRequestEvent) {
	o.mu.RLock()
	stored, ok := o.storage[e.Selection]
	o.mu.RUnlock()

	// Default reply: refusal (Property=0).
	reply := xproto.SelectionNotifyEvent{
		Time:      e.Time,
		Requestor: e.Requestor,
		Selection: e.Selection,
		Target:    e.Target,
		Property:  0,
	}
	property := e.Property
	if property == 0 {
		// Legacy clients use the target atom as the property name.
		property = e.Target
	}

	if !ok {
		o.sendSelectionNotify(reply)
		return
	}

	switch e.Target {
	case o.atoms.targets:
		// Advertise the targets we can serve.
		targets := []xproto.Atom{
			o.atoms.targets,
			o.atoms.utf8String,
			o.atoms.textPlain,
			xproto.AtomString,
		}
		if stored.mime == MimeTextURIList {
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
		if stored.mime == MimeTextPlain || stored.mime == MimeTextURIList {
			data := []byte(stored.content)
			if err := xproto.ChangePropertyChecked(o.conn, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(data)), data).Check(); err == nil {
				reply.Property = property
			}
		}
	case o.atoms.uriList:
		if stored.mime == MimeTextURIList {
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

// resolveSelectionAtom maps a user-facing selection name onto a list of
// X11 selection atoms. "both" expands to {CLIPBOARD, PRIMARY}.
func (o *Owner) resolveSelectionAtoms(selection string) ([]xproto.Atom, error) {
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "", SelectionClipboard:
		return []xproto.Atom{o.atoms.clipboard}, nil
	case SelectionPrimary:
		return []xproto.Atom{o.atoms.primary}, nil
	case SelectionBoth:
		return []xproto.Atom{o.atoms.clipboard, o.atoms.primary}, nil
	default:
		return nil, contract.Validation("CLIPBOARD_SELECTION_INVALID", "selection must be clipboard, primary, or both", map[string]any{"selection": selection})
	}
}

// Write stores content for the requested selection(s), takes ownership
// of each selection atom, and returns once the owner change is in flight.
// "both" writes to CLIPBOARD and PRIMARY simultaneously.
func Write(ctx context.Context, selection, content, mime string) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, contract.Cancelled("clipboard write cancelled")
	}
	mime = canonicalMime(mime)
	if mime != MimeTextPlain && mime != MimeTextURIList {
		return WriteResult{}, contract.Validation("CLIPBOARD_MIME_INVALID", "mime must be text/plain or text/uri-list", map[string]any{"mime": mime})
	}
	o, err := instance()
	if err != nil {
		return WriteResult{}, err
	}
	atoms, err := o.resolveSelectionAtoms(selection)
	if err != nil {
		return WriteResult{}, err
	}
	var mimeAtom xproto.Atom
	if mime == MimeTextURIList {
		mimeAtom = o.atoms.uriList
	} else {
		mimeAtom = o.atoms.utf8String
	}
	o.mu.Lock()
	for _, atom := range atoms {
		o.storage[atom] = storedSelection{content: content, mime: mime, mimeAtom: mimeAtom}
	}
	o.mu.Unlock()
	for _, atom := range atoms {
		if err := xproto.SetSelectionOwnerChecked(o.conn, o.window, atom, xproto.TimeCurrentTime).Check(); err != nil {
			return WriteResult{}, contract.Dependency("CLIPBOARD_OWNERSHIP_FAILED", "failed to take selection ownership", map[string]any{"error": err.Error()})
		}
	}
	canonical := canonicalSelectionName(selection)
	return WriteResult{Selection: canonical, Mime: mime, Bytes: len(content)}, nil
}

// Read retrieves the current value of the given selection. Uses a
// throwaway X11 connection so the owner event loop is not blocked while
// we wait for the SelectionNotify reply.
func Read(ctx context.Context, selection, mime string) (ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ReadResult{}, contract.Cancelled("clipboard read cancelled")
	}
	mime = canonicalMime(mime)
	if mime != MimeTextPlain && mime != MimeTextURIList {
		return ReadResult{}, contract.Validation("CLIPBOARD_MIME_INVALID", "mime must be text/plain or text/uri-list", map[string]any{"mime": mime})
	}
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "", SelectionClipboard, SelectionPrimary:
	default:
		return ReadResult{}, contract.Validation("CLIPBOARD_SELECTION_INVALID", "selection must be clipboard or primary for read", map[string]any{"selection": selection})
	}
	canonicalSel := canonicalSelectionName(selection)

	// Open a dedicated connection so the owner loop and this reader are
	// independent. This also avoids accidentally short-circuiting reads
	// from our own selection through in-process state.
	conn, err := xgb.NewConn()
	if err != nil {
		return ReadResult{}, contract.Dependency("CLIPBOARD_DISPLAY_UNAVAILABLE", "clipboard reader could not connect to X11", map[string]any{"error": err.Error()})
	}
	defer conn.Close()
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		return ReadResult{}, contract.Dependency("CLIPBOARD_WINDOW_ALLOC_FAILED", "failed to allocate reader window id", map[string]any{"error": err.Error()})
	}
	if err := xproto.CreateWindowChecked(conn, 0, wid, screen.Root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual, xproto.CwEventMask, []uint32{uint32(xproto.EventMaskPropertyChange)}).Check(); err != nil {
		return ReadResult{}, contract.Dependency("CLIPBOARD_WINDOW_CREATE_FAILED", "failed to create reader window", map[string]any{"error": err.Error()})
	}
	defer xproto.DestroyWindow(conn, wid)

	atoms, err := internAtoms(conn)
	if err != nil {
		return ReadResult{}, err
	}
	selAtom := atoms.clipboard
	if canonicalSel == SelectionPrimary {
		selAtom = atoms.primary
	}

	// Check there is an owner at all. If not, return empty content.
	owner, err := xproto.GetSelectionOwner(conn, selAtom).Reply()
	if err != nil {
		return ReadResult{}, contract.Dependency("CLIPBOARD_OWNER_QUERY_FAILED", "failed to query selection owner", map[string]any{"error": err.Error()})
	}
	if owner.Owner == 0 {
		return ReadResult{Content: "", Mime: mime, Selection: canonicalSel, Bytes: 0}, nil
	}

	target := atoms.utf8String
	if mime == MimeTextURIList {
		target = atoms.uriList
	}
	if err := xproto.ConvertSelectionChecked(conn, wid, selAtom, target, atoms.xmycomputer, xproto.TimeCurrentTime).Check(); err != nil {
		return ReadResult{}, contract.Dependency("CLIPBOARD_CONVERT_FAILED", "ConvertSelection request failed", map[string]any{"error": err.Error()})
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return ReadResult{}, contract.Cancelled("clipboard read cancelled")
		}
		if time.Now().After(deadline) {
			return ReadResult{}, contract.Dependency("CLIPBOARD_READ_TIMEOUT", "no SelectionNotify reply within timeout", map[string]any{"selection": canonicalSel})
		}
		ev, xerr := waitForEventWithTimeout(conn, 200*time.Millisecond)
		if xerr != nil {
			continue
		}
		if ev == nil {
			continue
		}
		notify, ok := ev.(xproto.SelectionNotifyEvent)
		if !ok {
			continue
		}
		if notify.Property == 0 {
			// Requested target unsupported by owner; return empty.
			return ReadResult{Content: "", Mime: mime, Selection: canonicalSel, Bytes: 0}, nil
		}
		// Read property in chunks. Cap total payload to a generous limit
		// to avoid runaway allocation from a misbehaving peer.
		const maxBytes = 8 * 1024 * 1024
		var buf []byte
		offset := uint32(0)
		for {
			reply, err := xproto.GetProperty(conn, true, wid, notify.Property, xproto.AtomAny, offset, 65536).Reply()
			if err != nil {
				return ReadResult{}, contract.Dependency("CLIPBOARD_PROPERTY_READ_FAILED", "failed to read selection property", map[string]any{"error": err.Error()})
			}
			if reply.Type == atoms.incr {
				return ReadResult{}, contract.Dependency("CLIPBOARD_INCR_UNSUPPORTED", "INCR-transferred selections are not supported", map[string]any{"selection": canonicalSel})
			}
			buf = append(buf, reply.Value...)
			if len(buf) > maxBytes {
				return ReadResult{}, contract.Dependency("CLIPBOARD_PAYLOAD_TOO_LARGE", "selection payload exceeded internal cap", map[string]any{"bytes": len(buf)})
			}
			if reply.BytesAfter == 0 {
				break
			}
			offset += uint32(len(reply.Value)) / 4
		}
		content := string(buf)
		return ReadResult{Content: content, Mime: mime, Selection: canonicalSel, Bytes: len(content)}, nil
	}
}

// waitForEventWithTimeout polls the connection for an event with a
// soft timeout. xgb has no native deadline API so we approximate by
// polling in a goroutine.
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

// SaveAndWrite captures the current value of the selection (if any) and
// then overwrites it. The returned RestoreFn writes the original payload
// back. RestoreFn returns nil if restore succeeded (or there was nothing
// to restore); the returned bool indicates whether the restore actually
// completed.
//
// Used by the type_text paste route to leave the user's clipboard
// untouched.
type RestoreFn func(context.Context) (restored bool, err error)

func SaveAndWrite(ctx context.Context, selection, content, mime string) (WriteResult, RestoreFn, error) {
	prev, prevErr := Read(ctx, selection, MimeTextPlain)
	writeRes, err := Write(ctx, selection, content, mime)
	if err != nil {
		return WriteResult{}, nil, err
	}
	restore := func(rctx context.Context) (bool, error) {
		if prevErr != nil {
			return false, nil
		}
		if prev.Content == "" {
			return false, nil
		}
		if _, werr := Write(rctx, selection, prev.Content, MimeTextPlain); werr != nil {
			return false, werr
		}
		return true, nil
	}
	return writeRes, restore, nil
}

func canonicalSelectionName(selection string) string {
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "", SelectionClipboard:
		return SelectionClipboard
	case SelectionPrimary:
		return SelectionPrimary
	case SelectionBoth:
		return SelectionBoth
	default:
		return selection
	}
}

func canonicalMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "", MimeTextPlain, "text/plain;charset=utf-8":
		return MimeTextPlain
	case MimeTextURIList:
		return MimeTextURIList
	default:
		return strings.ToLower(strings.TrimSpace(mime))
	}
}

// Probe reports clipboard readiness for doctor. Best-effort: a Cinnamon
// session without a running X11 display will report not-ready with the
// underlying X11 error. Required:false in all cases.
func Probe() contract.BackendStatus {
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
