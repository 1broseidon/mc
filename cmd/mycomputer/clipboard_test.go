package main

import (
	"errors"
	"runtime"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

// TestSpawnClipboardOwnerDaemonHandshakeTimeout verifies the
// silent-success regression: when the spawned daemon fails to take
// selection ownership within the deadline, spawnClipboardOwnerDaemon
// must return a CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT Dependency error
// rather than a fake-success WriteResult populated from the request
// args.
//
// We force failure by spawning with an unreachable DISPLAY so the
// child cannot open its X11 connection and never assumes ownership.
// The parent's foreground Write also fails first under the same env,
// but we still get a contract.AppError back (never a bare success);
// either CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT or a sibling Dependency
// code is acceptable — the contract is "no silent success".
func TestSpawnClipboardOwnerDaemonHandshakeTimeout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("clipboard owner daemon is linux-only")
	}
	t.Setenv("DISPLAY", ":9999") // unlikely to be a real display

	_, err := spawnClipboardOwnerDaemon("clipboard", "text/plain", "ignored")
	if err == nil {
		t.Fatalf("expected spawn to return an error when DISPLAY is unreachable, got nil")
	}
	var app *contract.AppError
	if !errors.As(err, &app) {
		t.Fatalf("expected *contract.AppError, got %T: %v", err, err)
	}
	switch app.Code {
	case "CLIPBOARD_DAEMON_HANDSHAKE_TIMEOUT", "CLIPBOARD_DAEMON_SPAWN_FAILED":
		// acceptable: both prove the silent-success regression is gone.
	default:
		t.Fatalf("unexpected error code %q (message: %s)", app.Code, app.Message)
	}
}
