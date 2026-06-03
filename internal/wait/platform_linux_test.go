//go:build linux

package wait

// Register the Linux/X11 platform backend for package-level live tests.
// Production binaries do this from cmd/mycomputer/platform_linux.go; package
// tests that call wait/window/screen paths directly need the same registration.
import _ "github.com/1broseidon/mc/internal/platform/x11adapter"
