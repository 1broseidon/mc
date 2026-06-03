//go:build linux

package x11adapter

import "github.com/1broseidon/mc/internal/platform"

// init installs the X11 provider as the process-wide platform backend on
// Linux. Activated by a blank import (see cmd/mycomputer/platform_linux.go).
// Tests that need a different backend override this via platform.SetProvider.
func init() {
	platform.SetProvider(New())
}
