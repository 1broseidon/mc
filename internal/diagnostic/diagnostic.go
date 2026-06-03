package diagnostic

import (
	"context"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/audit"
	"github.com/1broseidon/mc/internal/browser"
	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
	imageutil "github.com/1broseidon/mc/internal/image"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/window"
)

// ewmhAtomToCapability maps _NET_SUPPORTED atom names onto the short
// capability tokens advertised on the x11 backend row in doctor output.
// Probed once per doctor call by reading _NET_SUPPORTED on the X11
// root window. Missing atoms are silently omitted so agents can branch
// on a single capability list without sniffing per-WM quirks.
var ewmhAtomToCapability = []struct {
	atom       string
	capability string
}{
	{"_NET_MOVERESIZE_WINDOW", "ewmh.move"},
	{"_NET_MOVERESIZE_WINDOW", "ewmh.resize"},
	{"_NET_WM_DESKTOP", "ewmh.workspace"},
	{"_NET_WM_STATE", "ewmh.state"},
	{"_NET_CLOSE_WINDOW", "ewmh.close"},
	{"_NET_FRAME_EXTENTS", "ewmh.frame_extents"},
	{"_NET_ACTIVE_WINDOW", "ewmh.active_window"},
	{"_NET_RESTACK_WINDOW", "ewmh.restack"},
}

// Doctor builds the readiness report. The tools parameter is the
// authoritative list of MCP tool names sourced from mcpserver.Catalog()
// at call time — diagnostic cannot import mcpserver (cycle), so callers
// inject the catalog to keep AvailableTools drift-free.
func Doctor(version contract.VersionInfo, cfg config.Effective, tools []string) contract.DoctorReport {
	version.Go = runtime.Version()
	now := time.Now().UTC()
	autoDisplay := maybeAutoDetectDisplay()
	backends := platform.Current().Probe(context.Background())
	annotateDisplayAutoDetect(backends, autoDisplay)
	annotateEWMHCapabilities(backends)
	backends = append(backends, probeAccessibility(now))
	backends = append(backends, browser.Probe(cfg.BrowserBin, cfg.BrowserEndpoint))
	tess := imageutil.ProbeOCRTesseract()
	tess.CheckedAt = now
	backends = append(backends, tess)
	tmpl := imageutil.ProbeTemplateMatch()
	tmpl.CheckedAt = now
	backends = append(backends, tmpl)
	// task-5: clipboard + IME rows. Both required:false.
	clip := clipboard.Probe()
	clip.CheckedAt = now
	backends = append(backends, clip)
	ime := clipboard.ProbeIME()
	ime.CheckedAt = now
	backends = append(backends, ime)

	// task-6: xinput2 row. Reports whether the xinput binary that
	// drives MC's yield watcher is available, plus whether a brief
	// 200ms sample actually saw any real user events (sees_user_events).
	backends = append(backends, probeActivity(now))

	// task-6: audit row. Reports the audit directory, whether it's
	// writable, retention setting, and today's byte count.
	backends = append(backends, probeAudit(now))

	session := contract.SessionInfo{
		Display:        os.Getenv("DISPLAY"),
		XAuthority:     os.Getenv("XAUTHORITY"),
		XDGSessionType: os.Getenv("XDG_SESSION_TYPE"),
		WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
		Desktop:        os.Getenv("XDG_CURRENT_DESKTOP"),
		// RespectUser surfaces the resolved --respect-user setting.
		// Yield watcher is wired by task-6.
		RespectUser:   cfg.RespectUser,
		AllowClose:    cfg.AllowClose,
		LogicalCoords: cfg.LogicalCoords,
	}

	var blockers []string
	var warnings []string
	for _, backend := range backends {
		if backend.Required && !backend.Ready {
			blockers = append(blockers, backend.Name+": "+backend.Message)
		}
		if !backend.Required && !backend.Ready {
			warnings = append(warnings, backend.Name+": "+backend.Message)
		}
	}
	if session.XDGSessionType == "wayland" && session.Display == "" {
		blockers = append(blockers, "native Wayland sessions are not controlled by the MVP; start an X11 session or expose XWayland through DISPLAY")
	}
	status := "ready"
	next := "MCP server can be started with mycomputer serve"
	if len(blockers) > 0 {
		status = "blocked"
		next = "resolve readiness blockers before invoking mutating desktop tools"
		// task-28: when DISPLAY is unset and auto-probe could not pick
		// a singular live X server, surface a remediation hint that
		// covers the MCP-host env-inheritance footgun (Codex, Claude
		// Code, etc. spawned from a non-X-aware shell).
		if session.Display == "" {
			next = displayRemediationHint(autoDisplay)
		}
	}
	return contract.DoctorReport{
		Product:        "MyComputer",
		Version:        version,
		Session:        session,
		Readiness:      contract.Readiness{Status: status, Blockers: blockers, Warnings: warnings, NextAction: next},
		Backends:       backends,
		SchemaVersions: contract.SupportedSchemaVersions(),
		AvailableTools: tools,
	}
}

// maybeAutoDetectDisplay asks the active platform to repair a missing
// display environment when it supports that concept (X11). Non-DISPLAY
// platforms return an empty result.
func maybeAutoDetectDisplay() platform.DisplayAutoDetectResult {
	if detector, ok := platform.Current().(platform.DisplayAutoDetector); ok {
		return detector.MaybeAutoDetectDisplay()
	}
	return platform.DisplayAutoDetectResult{}
}

func probeAccessibility(now time.Time) contract.BackendStatus {
	if _, ok := platform.Current().Accessibility(); !ok {
		return contract.BackendStatus{Name: "at_spi", Ready: false, Required: false, Message: "accessibility backend unavailable on this platform", CheckedAt: now}
	}
	return contract.BackendStatus{Name: "at_spi", Ready: true, Required: false, Message: "available", Capabilities: []string{"tree", "action", "set_text"}, CheckedAt: now}
}

func probeActivity(now time.Time) contract.BackendStatus {
	watcher, ok := platform.Current().Activity()
	if !ok {
		return contract.BackendStatus{Name: "xinput2", Ready: false, Required: false, Message: "activity watcher unavailable on this platform", CheckedAt: now}
	}
	available, detail := watcher.Available()
	if !available {
		return contract.BackendStatus{Name: "xinput2", Ready: false, Required: false, Message: "activity watcher not available (required for --respect-user)", Details: map[string]any{"detail": detail}, CheckedAt: now}
	}
	count, err := watcher.Sample(context.Background(), 200*time.Millisecond)
	details := map[string]any{
		"path":               detail,
		"sees_user_events":   count > 0,
		"sampled_events":     count,
		"sample_duration_ms": 200,
	}
	msg := "available"
	if err != nil {
		msg = "available (sample failed: " + err.Error() + ")"
		details["sample_error"] = err.Error()
	}
	return contract.BackendStatus{Name: "xinput2", Ready: true, Required: false, Message: msg, Details: details, Capabilities: []string{"raw_motion", "raw_button_press", "raw_key_press"}, CheckedAt: now}
}

// probeAudit returns a BackendStatus describing the audit-log writer.
// Ready when the audit directory is writable. Surfaces the resolved
// dir, retention, and today's byte count for operability checks.
func probeAudit(now time.Time) contract.BackendStatus {
	w := audit.New()
	res := w.Probe(now)
	if !res.Writable {
		return contract.BackendStatus{
			Name:      "audit",
			Ready:     false,
			Required:  false,
			Message:   "audit directory not writable: " + res.Error,
			Details:   map[string]any{"dir": res.Dir, "retention_days": res.RetentionDays},
			CheckedAt: now,
		}
	}
	return contract.BackendStatus{
		Name:     "audit",
		Ready:    true,
		Required: false,
		Message:  "writable",
		Details: map[string]any{
			"dir":            res.Dir,
			"retention_days": res.RetentionDays,
			"today_bytes":    res.TodayBytes,
		},
		CheckedAt: now,
	}
}

// annotateDisplayAutoDetect rewrites the DISPLAY backend row to reflect
// the auto-probe outcome when DISPLAY was previously unset. Three cases:
//
//   - singular live socket: row becomes ready=true with the auto-detected
//     value and a message identifying the source socket;
//   - multiple live sockets: row stays not-ready but the message lists
//     the ambiguous candidates so the user can pick explicitly;
//   - no live sockets: row keeps its default "not set" message.
//
// When DISPLAY was already set the row is untouched.
func annotateDisplayAutoDetect(backends []contract.BackendStatus, res platform.DisplayAutoDetectResult) {
	if res.Display == "" && len(res.Ambiguous) == 0 {
		return
	}
	for i := range backends {
		if backends[i].Name != "DISPLAY" {
			continue
		}
		if res.Display != "" {
			backends[i].Ready = true
			backends[i].Message = "auto-detected " + res.Display + " from " + res.Source
			if backends[i].Details == nil {
				backends[i].Details = map[string]any{}
			}
			backends[i].Details["value"] = res.Display
			backends[i].Details["auto_detected"] = true
			backends[i].Details["source"] = res.Source
			return
		}
		// Ambiguous: leave Ready=false but explain the candidates.
		backends[i].Message = "DISPLAY_AMBIGUOUS: multiple live X servers (" + strings.Join(res.Ambiguous, ", ") + "); set DISPLAY explicitly or pass 'mycomputer serve --display <value>'"
		if backends[i].Details == nil {
			backends[i].Details = map[string]any{}
		}
		backends[i].Details["candidates"] = res.Ambiguous
		return
	}
}

// displayRemediationHint returns the next-action string for the
// DISPLAY-unset blocked state, tailored to what the auto-probe found.
func displayRemediationHint(res platform.DisplayAutoDetectResult) string {
	switch {
	case len(res.Ambiguous) > 0:
		return "DISPLAY is unset and /tmp/.X11-unix/ has multiple live sockets (" + strings.Join(res.Ambiguous, ", ") + "); pick one via 'mycomputer serve --display <value>' or export DISPLAY before launching the MCP host"
	default:
		return "DISPLAY is unset and no live X server was found in /tmp/.X11-unix/; launch the MCP host from an X session OR set DISPLAY explicitly via 'mycomputer serve --display :0'"
	}
}

// annotateEWMHCapabilities mutates the x11 backend row in place to
// advertise the EWMH atoms the running WM supports. Best-effort: if
// DISPLAY is unset, _NET_SUPPORTED is missing, or the probe otherwise
// fails the x11 row keeps a nil Capabilities slice (still valid per
// BackendStatus.Capabilities omitempty rules).
func annotateEWMHCapabilities(backends []contract.BackendStatus) {
	idx := -1
	for i, b := range backends {
		if b.Name == "x11" {
			idx = i
			break
		}
	}
	if idx < 0 || !backends[idx].Ready {
		return
	}
	supported, err := window.SupportedAtoms(context.Background())
	if err != nil || len(supported) == 0 {
		return
	}
	seen := map[string]bool{}
	caps := []string{}
	for _, pair := range ewmhAtomToCapability {
		if !supported[pair.atom] {
			continue
		}
		if seen[pair.capability] {
			continue
		}
		seen[pair.capability] = true
		caps = append(caps, pair.capability)
	}
	backends[idx].Capabilities = caps
}
