// Package platform defines the OS-abstraction seam for MyComputer.
//
// Everything above this package — the cmd/ CLI handlers, internal/mcpserver,
// internal/pipeline, and the service packages (input, screen, window,
// clipboard, a11y, yield) — speaks in terms of contract types and the
// capability interfaces declared here. Everything below this package is an
// adapter: a build-tagged implementation that turns these interfaces into
// real OS calls (X11/XTest/AT-SPI on Linux, Quartz/AppKit/AX on macOS).
//
// # Layering rules
//
//   - platform depends ONLY on internal/contract plus the standard library
//     (context, image). It must never import a service package or an
//     adapter package — that would re-create the import tangle this seam
//     exists to break.
//   - Service packages depend on platform + contract. They keep their
//     existing exported function signatures; their bodies delegate the
//     OS-specific third of the work to platform.Current().
//   - Adapter packages depend on platform + contract + the OS libraries.
//     Each adapter registers itself via SetProvider from an init function
//     guarded by a build tag (init_linux.go, init_darwin.go).
//
// # Currency
//
// contract types (Bounds, Point, WindowInfo, MonitorInfo, BackendStatus,
// …) are the lingua franca across the seam. The neutral types declared in
// this package (Button, Key, Selection, CursorImage, Node, NativeID) exist
// only where the contract layer is not yet OS-neutral — they replace values
// that currently leak X11 semantics (keysyms, selection atoms, XIDs).
//
// # Capability negotiation
//
// Not every OS exposes every capability. Optional capabilities are surfaced
// through comma-ok accessors on Provider (Accessibility, Activity) rather
// than nil checks scattered through callers. Per-OS feature differences
// (X11 PRIMARY selection, EWMH workspace control) are reported through
// Provider.Probe and the Capabilities accessors so the service layer can
// emit a canonical *_UNSUPPORTED error instead of guessing.
package platform
