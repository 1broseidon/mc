//go:build darwin

package macadapter

import "github.com/1broseidon/mc/internal/platform"

// init installs the macOS provider as the process-wide platform backend on
// darwin. Activated by a blank import (see cmd/mycomputer/platform_darwin.go).
// Tests that need a different backend override this via platform.SetProvider.
func init() {
	platform.SetProvider(New())
}
