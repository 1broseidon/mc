package diagnostic

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/1broseidon/mc/internal/a11y"
	"github.com/1broseidon/mc/internal/audit"
	"github.com/1broseidon/mc/internal/browser"
	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
	imageutil "github.com/1broseidon/mc/internal/image"
	"github.com/1broseidon/mc/internal/window"
	"github.com/1broseidon/mc/internal/x11"
	"github.com/1broseidon/mc/internal/yield"
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
	backends := x11.Probe()
	annotateEWMHCapabilities(backends)
	backends = append(backends, a11y.Probe())
	backends = append(backends, browser.Probe(cfg.BrowserBin, cfg.BrowserEndpoint))
	now := time.Now().UTC()
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
	backends = append(backends, probeXInput2(now))

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

// probeXInput2 returns a BackendStatus describing the xinput2 yield
// listener. ready=true when the xinput binary is found on PATH; we
// additionally start a brief 200ms watcher and report how many real
// (non-MC-synthetic) raw events arrived in that window. A zero count
// is not a failure — the user was simply idle during the sample.
func probeXInput2(now time.Time) contract.BackendStatus {
	ok, path := yield.Available()
	if !ok {
		return contract.BackendStatus{
			Name:      "xinput2",
			Ready:     false,
			Required:  false,
			Message:   "xinput binary not found on PATH (required for --respect-user)",
			CheckedAt: now,
		}
	}
	count, err := yield.SampleUserEvents(context.Background(), 200*time.Millisecond)
	details := map[string]any{
		"path":               path,
		"sees_user_events":   count > 0,
		"sampled_events":     count,
		"sample_duration_ms": 200,
	}
	msg := "available"
	if err != nil {
		msg = "available (sample failed: " + err.Error() + ")"
		details["sample_error"] = err.Error()
	}
	return contract.BackendStatus{
		Name:         "xinput2",
		Ready:        true,
		Required:     false,
		Message:      msg,
		Details:      details,
		Capabilities: []string{"raw_motion", "raw_button_press", "raw_key_press"},
		CheckedAt:    now,
	}
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
