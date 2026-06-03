package a11y

import (
	"context"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

type ObjectRef struct {
	Bus  string
	Path string
}

type Element struct {
	ID         string          `json:"id"`
	Bus        string          `json:"bus"`
	Path       string          `json:"path"`
	Name       string          `json:"name,omitempty"`
	Role       string          `json:"role,omitempty"`
	Interfaces []string        `json:"interfaces,omitempty"`
	Bounds     contract.Bounds `json:"bounds,omitempty"`
	Depth      int             `json:"depth"`
	Actions    []string        `json:"actions,omitempty"`
	// App is the owning application name resolved by the platform backend.
	// Always present as a string field — empty string when unavailable,
	// never "?" or null.
	App string `json:"app"`
	// Toolkit is the owning toolkit name (e.g. "gtk", "Qt",
	// "Chromium"). Always present; empty when unavailable.
	Toolkit string `json:"toolkit"`
	// WindowID cross-references the window list when a top-level frame can
	// be correlated to a native window. Empty when no correlation can be
	// established; always emitted to avoid missing-key ambiguity.
	WindowID string `json:"window_id"`
	// Extra carries source-specific metadata that does not warrant a
	// dedicated typed field. When window_id correlation succeeds,
	// Extra["correlation"] records which strategy matched: one of
	// "bounds", "pid", or "title". Absent (nil map) for elements whose
	// window_id is empty.
	Extra map[string]any `json:"extra,omitempty"`
}

type TreeResult struct {
	Status   string    `json:"status"`
	Message  string    `json:"message,omitempty"`
	Elements []Element `json:"elements,omitempty"`
}

func Probe() contract.BackendStatus {
	now := time.Now().UTC()
	if a, ok := platform.Current().Accessibility(); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if _, err := a.Tree(ctx, 1, 1); err != nil {
			return contract.BackendStatus{Name: "at_spi", Ready: false, Required: false, Message: err.Error(), CheckedAt: now}
		}
		return contract.BackendStatus{Name: "at_spi", Ready: true, Required: false, Message: "available", CheckedAt: now}
	}
	return contract.BackendStatus{Name: "at_spi", Ready: false, Required: false, Message: "accessibility backend unavailable", CheckedAt: now}
}

func Tree(ctx context.Context, maxDepth int) (map[string]any, error) {
	return TreeWithWindows(ctx, maxDepth, nil)
}

// TreeWithWindows is the window-aware variant of Tree. Callers pass the
// current window list so this portable policy layer can correlate top-level
// accessibility frames to window ids (see CorrelateWindowID for the strategy).
// Passing a nil/empty window list disables correlation; every element's
// WindowID stays empty.
func TreeWithWindows(ctx context.Context, maxDepth int, windows []contract.WindowInfo) (map[string]any, error) {
	tree, err := TreeElementsWithWindows(ctx, maxDepth, 500, windows)
	if err != nil {
		return map[string]any{"status": "unavailable", "message": err.Error()}, nil
	}
	return map[string]any{"status": tree.Status, "message": tree.Message, "elements": tree.Elements}, nil
}

func TreeElements(ctx context.Context, maxDepth int, maxNodes int) (TreeResult, error) {
	return TreeElementsWithWindows(ctx, maxDepth, maxNodes, nil)
}

// TreeElementsWithWindows walks the platform accessibility tree and, when
// windows is non-empty, correlates each top-level frame (and its descendants)
// to a native window id via the bounds → pid → title fallback chain.
func TreeElementsWithWindows(ctx context.Context, maxDepth int, maxNodes int, windows []contract.WindowInfo) (TreeResult, error) {
	if err := ctx.Err(); err != nil {
		return TreeResult{}, contract.Cancelled("accessibility tree cancelled")
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxNodes <= 0 {
		maxNodes = 250
	}
	a, ok := platform.Current().Accessibility()
	if !ok {
		return TreeResult{}, contract.Dependency("ACCESSIBILITY_UNAVAILABLE", "accessibility backend is unavailable", map[string]any{"backend": platform.Current().Name()})
	}
	nodes, err := a.Tree(ctx, maxDepth, maxNodes)
	if err != nil {
		return TreeResult{}, err
	}
	out := make([]Element, 0, len(nodes))
	windowsByNode := map[string]string{}
	corrByNode := map[string]string{}
	for _, node := range nodes {
		el := elementFromNode(node)
		winID := ""
		corrSrc := ""
		if node.ParentID != "" {
			winID = windowsByNode[node.ParentID]
			corrSrc = corrByNode[node.ParentID]
		}
		if winID == "" && isFrameRole(el.Role) && len(windows) > 0 {
			pid := node.PID
			winID, corrSrc = CorrelateWindowID(el, windows, func() uint32 { return pid })
		}
		el.WindowID = winID
		if winID != "" && corrSrc != "" {
			el.Extra = map[string]any{"correlation": corrSrc}
		}
		if node.ID != "" {
			windowsByNode[node.ID] = winID
			corrByNode[node.ID] = corrSrc
		}
		out = append(out, el)
	}
	return TreeResult{Status: "available", Elements: out}, nil
}

func elementFromNode(node platform.Node) Element {
	bus, path := splitNodeID(node.ID)
	return Element{
		ID:         node.ID,
		Bus:        bus,
		Path:       path,
		Name:       node.Name,
		Role:       node.Role,
		Interfaces: node.Interfaces,
		Bounds:     node.Bounds,
		Depth:      node.Depth,
		Actions:    node.Actions,
		App:        node.App,
		Toolkit:    node.Toolkit,
	}
}

func splitNodeID(id string) (string, string) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func PerformAction(ctx context.Context, elementID string, actionName string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI action cancelled")
	}
	a, ok := platform.Current().Accessibility()
	if !ok {
		return contract.Dependency("ACCESSIBILITY_UNAVAILABLE", "accessibility backend is unavailable", map[string]any{"backend": platform.Current().Name()})
	}
	return a.PerformAction(ctx, elementID, actionName)
}

func SetText(ctx context.Context, elementID string, text string) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("AT-SPI set text cancelled")
	}
	a, ok := platform.Current().Accessibility()
	if !ok {
		return contract.Dependency("ACCESSIBILITY_UNAVAILABLE", "accessibility backend is unavailable", map[string]any{"backend": platform.Current().Name()})
	}
	return a.SetText(ctx, elementID, text)
}

// isFrameRole reports whether an accessibility role name marks a top-level
// window-like element worth correlating against the window list. Children of
// these elements inherit the correlation result rather than re-running the
// per-frame match.
func isFrameRole(role string) bool {
	switch strings.ToLower(role) {
	case "frame", "window", "dialog", "alert":
		return true
	}
	return false
}

// CorrelateWindowID resolves an accessibility frame element to a window id
// using a three-strategy fallback chain.
//
//  1. Bounds: top-level frame extents within ~10px of any window's
//     ClientBounds (preferred) or outer Bounds wins. Tightest fit across
//     all candidates is selected so two windows of similar size don't both
//     match.
//  2. PID: optional resolvePid callback compared against WindowInfo.PID.
//  3. Title: lowercase substring match between frame.Name and window.Title.
//
// Returns the matched window's id and the strategy tag, or ("", "") when no
// candidate matched.
func CorrelateWindowID(el Element, windows []contract.WindowInfo, resolvePid func() uint32) (string, string) {
	if len(windows) == 0 {
		return "", ""
	}
	if !el.Bounds.Empty() {
		const tol = 10
		bestID := ""
		bestDelta := -1
		for _, w := range windows {
			target := w.ClientBounds
			if target.Empty() {
				target = w.Bounds
			}
			if target.Empty() {
				continue
			}
			dx := absInt(el.Bounds.X - target.X)
			dy := absInt(el.Bounds.Y - target.Y)
			dw := absInt(el.Bounds.Width - target.Width)
			dh := absInt(el.Bounds.Height - target.Height)
			if dx > tol || dy > tol || dw > tol || dh > tol {
				continue
			}
			delta := dx + dy + dw + dh
			if bestDelta < 0 || delta < bestDelta {
				bestDelta = delta
				bestID = w.ID
			}
		}
		if bestID != "" {
			return bestID, "bounds"
		}
	}
	if resolvePid != nil {
		if pid := resolvePid(); pid != 0 {
			for _, w := range windows {
				if w.PID != 0 && w.PID == pid {
					return w.ID, "pid"
				}
			}
		}
	}
	if el.Name != "" {
		needle := strings.ToLower(el.Name)
		for _, w := range windows {
			if w.Title == "" {
				continue
			}
			hay := strings.ToLower(w.Title)
			if strings.Contains(hay, needle) || strings.Contains(needle, hay) {
				return w.ID, "title"
			}
		}
	}
	return "", ""
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func ElementID(ref ObjectRef) string {
	return ref.Bus + "|" + ref.Path
}

func ParseElementID(id string) (ObjectRef, error) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ObjectRef{}, contract.Validation("INVALID_ELEMENT_ID", "accessibility element id must be bus|path", map[string]any{"element_id": id})
	}
	return ObjectRef{Bus: parts[0], Path: parts[1]}, nil
}
