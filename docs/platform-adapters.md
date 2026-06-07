# Platform adapters

MyComputer's desktop functionality is split into a portable core and a
per-OS **adapter** behind a single seam: `internal/platform`. The Linux
backend lives in `internal/platform/x11adapter`. This document is the
checklist for adding a new backend (next: macOS / `macadapter`).

## How the seam works

- `internal/platform` declares OS-neutral capability interfaces and neutral
  types. It imports only `internal/contract` + stdlib.
- An adapter package implements those interfaces and **self-registers** via
  `platform.SetProvider` from a build-tagged `init()`.
- `cmd/mycomputer` blank-imports the adapter through a build-tagged file so
  the binary wires the right backend per OS.
- `platform.Current()` returns the active provider. It **never returns nil**:
  with no adapter registered it returns `unsupported{}`, whose mutating ops
  fail with `PLATFORM_UNSUPPORTED` and whose enumerations return empty.

Today on macOS the build succeeds but resolves to `unsupported{}` — there is
no darwin registration yet. That is step 1 below.

## Provider surface

`platform.Provider` (see `internal/platform/interfaces.go`):

| Method | Required | Notes |
| --- | --- | --- |
| `Name() string` | yes | backend id for diagnostics, e.g. `"darwin"` |
| `Labels() BackendLabels` | yes | user-facing labels in action results / observe |
| `Pointer() Pointer` | yes | mouse move/button/scroll |
| `Keyboard() Keyboard` | yes | type text + key combos |
| `Screen() ScreenGrabber` | yes | capture, monitors, cursor |
| `Windows() WindowManager` | yes | list + control top-level windows |
| `Clipboard() Clipboard` | yes | read/write selections |
| `Accessibility() (Accessibility, bool)` | optional | return `(nil,false)` if absent |
| `Activity() (UserActivityWatcher, bool)` | optional | yield / respect-user |
| `Probe(ctx) []contract.BackendStatus` | yes | doctor rows for this OS |

### Optional capabilities (type-asserted, not on Provider)

The portable layer discovers these via type assertion on the capability
value, so a backend opts in just by implementing the method:

- `ClipboardDaemon` — only platforms whose clipboard dies with the process
  (X11). macOS `NSPasteboard` persists, so **do not implement** it.
- `WindowWorkspaceReader` — `WorkspaceOf` for the workspace honored-check.
  macOS Spaces differ; leave unimplemented unless you can map it.
- `DisplayAutoDetector` — `MaybeAutoDetectDisplay` for repairing a missing
  display env (X11 `/tmp/.X11-unix`). macOS has no DISPLAY concept; skip.
- `InputMethodProbe` — `DetectIME` / `ProbeIME` on the keyboard capability.

## Neutral types you implement against

In `internal/platform/types.go`: `Button`, `PressAction`, `Key`,
`Selection`, `NativeID`, `MaximizeAxis`, `CursorImage`, `Node`,
`ActivityEvent`, `IMEStatus`, `DisplayAutoDetectResult`, and
`BackendLabels{Platform, Screen, Capture, Window, Input, Clipboard, Accessibility}`.

Policy stays in the portable service layer (coordinate resolution, type-text
routing, drag interpolation, target matching, geometry-divergence heuristic,
clipboard save/restore, a11y tree walk policy). The adapter implements only
the OS primitives. Match the contract error codes the X11 adapter emits
where the concept is identical; introduce `AX_*` / TCC-specific codes only
where macOS genuinely differs.

## Step-by-step: adding `macadapter`

1. **Registration skeleton** (unblocks everything):
   - `internal/platform/macadapter/provider.go` — `Provider` with `Name()`
     `"darwin"`, `Labels()`, and capability accessors returning stubs that
     fail with a clear code (reuse the `PLATFORM_CAP_NOT_IMPLEMENTED` shape).
   - `internal/platform/macadapter/register_darwin.go` — `//go:build darwin`,
     `func init() { platform.SetProvider(New()) }`.
   - `cmd/mycomputer/platform_darwin.go` — `//go:build darwin`, blank import
     of the adapter (mirror `platform_linux.go`).
   - After this, `doctor` on macOS reports `"darwin"`, not `"unsupported"`.

2. **Screen first** (highest confidence, unlocks capture + OCR + find_*):
   CoreGraphics — `CGGetActiveDisplayList`, `CGDisplayBounds`,
   `CGDisplayCreateImage`. Return `SCREEN_RECORDING_PERMISSION_REQUIRED` as a
   `contract.Dependency` when TCC denies capture.

3. **Input** — Quartz Event Services (`CGEventCreateMouseEvent`,
   `CGEventPost`, `CGWarpMouseCursorPosition`, keyboard/unicode events).
   Return an Accessibility-permission error when not trusted.

4. **Clipboard** — `NSPasteboard`. `Selections()` returns `{clipboard}`
   only; do **not** implement `ClipboardDaemon`. The portable layer already
   degrades `both` → clipboard-only.

5. **Windows** — `CGWindowListCopyWindowInfo` for list; control via the
   Accessibility API (`AXUIElement`). Keep an internal `NativeID.ID → AXUIElement`
   map (CGWindowID fits `NativeID.Raw` for listing). Best-effort; emit the
   existing `WINDOW_GEOMETRY_*` warnings.

6. **Optional** — `Accessibility` (AX API) and `Activity` (`CGEventTap`)
   last; return `(nil,false)` until implemented.

7. **Doctor rows** — `Probe` returns macOS-specific rows
   (`screen_recording`, `accessibility`, `coregraphics`, `pasteboard`, …)
   instead of DISPLAY/xtest/randr.

### cgo decision

Use **cgo behind `//go:build darwin` only**. Linux stays cgo-free; macOS
gets native Quartz/AX/AppKit without `purego` contortions. The boundary
guard below does not restrict cgo — only Linux-specific Go imports.

## Guardrails

- `make platform-boundary` — fails if `internal/x11`, `internal/yield`,
  `jezek/xgb`, or `godbus/dbus` are imported outside `internal/platform/x11adapter`
  and the legacy leaf packages. Keep new portable code clean; keep darwin
  code free of these entirely.
- Package-level live tests that call platform-backed APIs directly need a
  registration shim (see `internal/screen/platform_linux_test.go`); add the
  darwin equivalent when writing live mac tests.

## Verification (run on the target OS)

```sh
gofmt -l .
make platform-boundary
go test ./...
GOOS=darwin GOARCH=arm64 go build ./...   # from Linux: compile-only check
```

On macOS, also run `go test ./...` natively and exercise `doctor`,
`screenshot`, `click`, and `clipboard-*` against a real session.
