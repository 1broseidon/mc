// Package clipboard is the portable policy layer over the platform
// clipboard backend. It owns MIME validation, selection-name
// canonicalization, the "both" → [clipboard, primary] expansion, the
// save/restore paste orchestration, and byte-count bookkeeping for the
// audit log. The OS-specific selection mechanism (the X11 selection-owner
// daemon, NSPasteboard, …) lives behind platform.Provider.Clipboard().
//
// IME detection (ime.go) is a separate session-DBus concern and is not
// part of the clipboard mechanism; it remains here until it gets its own
// platform capability.
package clipboard

// Anvil · target: internal/clipboard · kind: package · scope: package
// caller profile: agent,script · surface pattern: package-API (delegating) · risk class: R0
// contracts: Read/Write/SaveAndWrite/Done/Probe + ReadResult/WriteResult + error codes preserved
// obligations: selection mechanism delegated to platform.Provider.Clipboard()

import (
	"context"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
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

// Write stores content for the requested selection(s) via the platform
// backend. "both" expands to every selection the platform supports
// (CLIPBOARD + PRIMARY on X11; CLIPBOARD only on platforms without a
// primary selection).
func Write(ctx context.Context, selection, content, mime string) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, contract.Cancelled("clipboard write cancelled")
	}
	mime = canonicalMime(mime)
	if mime != MimeTextPlain && mime != MimeTextURIList {
		return WriteResult{}, contract.Validation("CLIPBOARD_MIME_INVALID", "mime must be text/plain or text/uri-list", map[string]any{"mime": mime})
	}
	cb := platform.Current().Clipboard()
	sels, err := writeSelections(cb, selection)
	if err != nil {
		return WriteResult{}, err
	}
	for _, sel := range sels {
		if err := cb.Write(ctx, sel, content, mime); err != nil {
			return WriteResult{}, err
		}
	}
	return WriteResult{Selection: canonicalSelectionName(selection), Mime: mime, Bytes: len(content)}, nil
}

// Read retrieves the current value of the given selection via the platform
// backend. "both" is not valid for read.
func Read(ctx context.Context, selection, mime string) (ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ReadResult{}, contract.Cancelled("clipboard read cancelled")
	}
	mime = canonicalMime(mime)
	if mime != MimeTextPlain && mime != MimeTextURIList {
		return ReadResult{}, contract.Validation("CLIPBOARD_MIME_INVALID", "mime must be text/plain or text/uri-list", map[string]any{"mime": mime})
	}
	sel, err := readSelection(selection)
	if err != nil {
		return ReadResult{}, err
	}
	content, gotMime, err := platform.Current().Clipboard().Read(ctx, sel, mime)
	if err != nil {
		return ReadResult{}, err
	}
	if gotMime == "" {
		gotMime = mime
	}
	return ReadResult{Content: content, Mime: gotMime, Selection: string(sel), Bytes: len(content)}, nil
}

// writeSelections maps a user-facing selection name onto the platform
// selections to write. "both" intersects {clipboard, primary} with the
// platform's supported set, so a platform without a primary selection
// silently writes only the clipboard. Unknown names are rejected.
func writeSelections(cb platform.Clipboard, selection string) ([]platform.Selection, error) {
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "", SelectionClipboard:
		return []platform.Selection{platform.SelectionClipboard}, nil
	case SelectionPrimary:
		if !supportsSelection(cb, platform.SelectionPrimary) {
			return nil, contract.Validation("CLIPBOARD_SELECTION_UNSUPPORTED", "the primary selection is not supported on this platform", map[string]any{"selection": selection, "backend": platform.Current().Name()})
		}
		return []platform.Selection{platform.SelectionPrimary}, nil
	case SelectionBoth:
		out := []platform.Selection{platform.SelectionClipboard}
		if supportsSelection(cb, platform.SelectionPrimary) {
			out = append(out, platform.SelectionPrimary)
		}
		return out, nil
	default:
		return nil, contract.Validation("CLIPBOARD_SELECTION_INVALID", "selection must be clipboard, primary, or both", map[string]any{"selection": selection})
	}
}

// readSelection validates a read selection name (clipboard or primary;
// "both" is write-only).
func readSelection(selection string) (platform.Selection, error) {
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "", SelectionClipboard:
		return platform.SelectionClipboard, nil
	case SelectionPrimary:
		if !supportsSelection(platform.Current().Clipboard(), platform.SelectionPrimary) {
			return "", contract.Validation("CLIPBOARD_SELECTION_UNSUPPORTED", "the primary selection is not supported on this platform", map[string]any{"selection": selection, "backend": platform.Current().Name()})
		}
		return platform.SelectionPrimary, nil
	default:
		return "", contract.Validation("CLIPBOARD_SELECTION_INVALID", "selection must be clipboard or primary for read", map[string]any{"selection": selection})
	}
}

func supportsSelection(cb platform.Clipboard, want platform.Selection) bool {
	for _, s := range cb.Selections() {
		if s == want {
			return true
		}
	}
	return false
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

// Done returns a channel closed when this process loses clipboard
// selection ownership. Only platforms whose clipboard is bound to a live
// owner process (X11) implement platform.ClipboardDaemon; on platforms
// whose clipboard persists after exit this returns
// CLIPBOARD_DAEMON_UNSUPPORTED.
func Done() (<-chan struct{}, error) {
	cb := platform.Current().Clipboard()
	daemon, ok := cb.(platform.ClipboardDaemon)
	if !ok {
		return nil, contract.Dependency("CLIPBOARD_DAEMON_UNSUPPORTED", "the active clipboard backend does not require an owner daemon", map[string]any{"backend": platform.Current().Name()})
	}
	return daemon.Done()
}

// clipboardProber is the optional capability a backend implements to
// report its own readiness row for doctor. The X11 adapter implements it
// (it can surface display-connection failures and the uri_list cap); a
// backend that does not implement it gets a generic reachability probe.
type clipboardProber interface {
	Probe() contract.BackendStatus
}

// Probe reports clipboard readiness for doctor. Delegates to the backend's
// own probe when available; otherwise performs a generic reachability read.
// Required:false in all cases.
func Probe() contract.BackendStatus {
	now := time.Now().UTC()
	cb := platform.Current().Clipboard()
	if p, ok := cb.(clipboardProber); ok {
		return p.Probe()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := cb.Read(ctx, platform.SelectionClipboard, MimeTextPlain); err != nil {
		return contract.BackendStatus{Name: "clipboard", Ready: false, Required: false, Message: err.Error(), CheckedAt: now}
	}
	caps := []string{"uri_list"}
	for _, s := range cb.Selections() {
		caps = append(caps, string(s))
	}
	return contract.BackendStatus{Name: "clipboard", Ready: true, Required: false, Message: "available", Capabilities: caps, CheckedAt: now}
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
