package clipboard

import (
	"context"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// IMEStatus describes the current state of any Input Method Editor. It is
// READ-ONLY — callers must never try to disable an active IME.
type IMEStatus = platform.IMEStatus

// DetectIME probes the active platform for Input Method Editor state. It
// returns a best-effort status; unavailable platform support or probe errors
// surface as active:false. Linux implements this through IBus/Fcitx5 over
// session D-Bus in the x11adapter; platforms without IME probing simply
// return the zero value.
func DetectIME() IMEStatus {
	probe, ok := platform.Current().Keyboard().(platform.InputMethodProbe)
	if !ok {
		return IMEStatus{}
	}
	return probe.DetectIME(context.Background())
}

// ProbeIME returns a BackendStatus describing IME state for doctor.
// Required:false; we never block readiness on the presence/absence of an
// IME. Delegates to the platform keyboard/input backend when available.
func ProbeIME() contract.BackendStatus {
	now := time.Now().UTC()
	probe, ok := platform.Current().Keyboard().(platform.InputMethodProbe)
	if !ok {
		return contract.BackendStatus{
			Name:      "ime",
			Ready:     true,
			Required:  false,
			Message:   "IME probe unavailable on this platform",
			Details:   map[string]any{"active": false, "backend": platform.Current().Name()},
			CheckedAt: now,
		}
	}
	status := probe.ProbeIME(context.Background())
	if status.CheckedAt.IsZero() {
		status.CheckedAt = now
	}
	return status
}
