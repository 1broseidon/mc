//go:build linux

package main

// Blank import installs the X11 platform backend on Linux via the
// adapter's init (platform.SetProvider). Build-tagged so non-Linux builds
// select their own adapter without pulling in X11 dependencies.
import _ "github.com/1broseidon/mc/internal/platform/x11adapter"
