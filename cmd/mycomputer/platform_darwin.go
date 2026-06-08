//go:build darwin

package main

// Blank import installs the macOS platform backend on darwin via the
// adapter's init (platform.SetProvider). Build-tagged so non-darwin builds
// select their own adapter without pulling in macOS frameworks.
import _ "github.com/1broseidon/mc/internal/platform/macadapter"
