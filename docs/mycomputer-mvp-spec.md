# MyComputer MVP Spec

Status: MVP implemented in this repository as of 2026-05-26.

Anvil stamp: target `mycomputer`, kind `cli+mcp`, boundary `tool-scope`, callers `agent,script,human-operator`, pattern `verb-surface CLI with MCP-first tool surface`, risk `R1 additive public design`, critique `I5 B5 C5 Err4 O5 S4 V4`.

## Summary

MyComputer is a Go-native CLI and MCP server for X11 desktop computer use on Linux. It gives agents a local, scriptable way to observe and control GUI applications through screenshots, window targeting, mouse and keyboard input, accessibility metadata, and browser-specific pipelines.

The product name is an ode to the old "My Computer" desktop icon. The binary name is `mycomputer`, and the MCP server id is `my-computer`.

## Goals

- Provide reliable X11 desktop control for MCP hosts and agent workflows.
- Use Go and native Linux desktop protocols where practical.
- Make screenshot-to-action coordinate mapping explicit and stable.
- Prefer semantic targeting through AT-SPI when available, with pixel targeting as fallback.
- Reduce agent round trips through validated action batches and browser pipelines.
- Keep the tool local-only, inspectable, and friendly to scripts.
- Avoid arbitrary shell execution.

## Non-Goals

- No generic `exec`, shell, filesystem, or terminal command tool.
- No autonomous planning loop. The MCP host or model remains the planner.
- No Wayland-first automation in MVP. Wayland is detected and reported with actionable diagnostics.
- No remote daemon, cloud service, telemetry, or network listener by default.
- No macOS, Windows, mobile, or cross-platform support in MVP.
- No OCR or template matching in MVP.

## Runtime Target

MVP targets an interactive, unlocked Linux desktop session running X11.

Required runtime conditions:

- `DISPLAY` points to a reachable X11 display.
- `XAUTHORITY` is valid when needed.
- XTest is available for synthetic input.
- XRandR or Xinerama is available for screen geometry.
- EWMH/ICCCM support is available for normal window manager interaction.

Optional runtime capabilities:

- XFixes for cursor overlay in screenshots.
- AT-SPI over D-Bus for accessibility tree discovery and semantic actions.
- Chromium or Chrome with DevTools Protocol for browser pipelines.

Wayland behavior:

- Native Wayland sessions are not controlled in MVP.
- XWayland windows may be partially controllable only when exposed through the X11 display.
- `doctor` must explain the detected session type, supported capabilities, blockers, and next action.

## Boundary Inventory

Evidence collected before this spec:

- Workspace is empty: `find . -maxdepth 3 -type f | sort` returned no files.
- Workspace is not a git repo at inventory time.
- No existing command tree, flags, package exports, config keys, or JSON schemas exist.

Preserve:

- No shipped public contracts exist yet.

Establish:

- CLI command tree.
- MCP tool names and semantics.
- JSON output conventions.
- Error and exit-code contract.
- Config precedence and env prefix.
- Go package layout rule.

## Risk Classification

| Surface | Change | Risk | Compatibility | Artifact | Verification |
| --- | --- | --- | --- | --- | --- |
| CLI binary `mycomputer` | add | R1 | additive; no existing callers | this spec | future `mycomputer --help` smoke |
| CLI commands | add | R1 | additive; no existing callers | this spec | future command help smoke |
| CLI global flags | add | R1 | additive; no existing callers | this spec | future help and JSON smoke |
| Env prefix `MYCOMPUTER_` | add | R1 | additive; no existing callers | this spec | future `config --json` |
| Exit codes | add | R1 | additive; no existing callers | this spec | future bad-input smoke |
| MCP server id `my-computer` | add | R1 | additive; no existing callers | this spec | future MCP initialize/tools/list |
| MCP tools | add | R1 | additive; no existing callers | this spec | future MCP tool schema snapshot |
| JSON schemas | add | R1 | additive; no existing callers | this spec | future schema examples and tests |
| Repo layout | add | R0/R1 | internal layout; public only as contributor contract | this spec | future `go test ./...` and import-cycle check |

No R3, R4, or R5 changes are in scope because no shipped surface exists.

## CLI Surface

The CLI is a shallow verb-surface tool. The rich action surface lives in MCP. The CLI exists for setup, diagnostics, scripting, and debugging.

Root command:

```text
mycomputer [command] [flags]
```

Top-level commands:

| Command | Purpose | Output |
| --- | --- | --- |
| `serve` | Run the stdio MCP server. | MCP JSON-RPC over stdio |
| `doctor` | Report readiness and blockers. | text or JSON |
| `config` | Show effective config and detected backends. | text or JSON |
| `observe` | Return combined desktop state for debugging. | text, JSON, or minimal |
| `capture` | Capture full screen, window, region, or zoom crop. | image file plus summary |
| `windows` | List windows and focus metadata. | text, JSON, or minimal |
| `focus` | Focus a target window. | quiet success or JSON summary |
| `actions` | Execute a validated action batch from JSON. | text or JSON |
| `browse` | Run a browser pipeline through CDP. | text or JSON |
| `completion` | Generate shell completion. | shell script |
| `version` | Print version/build metadata. | text or JSON |

There is intentionally no root `run` command.

### Command-Local Inputs

Commands that accept structured plans use the same input flag:

| Flag | Commands | Meaning |
| --- | --- | --- |
| `--input-file path` | `actions`, `browse` | Read a JSON request document from `path`; `-` means stdin. |
| `--timeout duration` | `actions`, `browse` | Cancel the operation after a caller-provided duration. |

`capture` accepts target flags for full screen, window, region, and zoom crop. The exact flag names are finalized during implementation, but they must be consistent with the MCP `screenshot` input schema.

### Global Flags

| Flag | Meaning |
| --- | --- |
| `--json` | Emit machine-readable JSON for data commands. |
| `--minimal` | Emit bounded tab-separated output where supported. |
| `--max-chars int` | Truncate long textual fields to N characters; `0` disables truncation. |
| `--no-color` | Disable ANSI output even on a TTY. |
| `--config path` | Load config from an explicit file. |
| `-q, --quiet` | Suppress non-essential diagnostics. |
| `-v, --verbose` | Emit extra diagnostics to stderr. |
| `-h, --help` | Show help. |

All diagnostics, warnings, and progress go to stderr. Data goes to stdout. `NO_COLOR=1` must be honored.

### Config

Config precedence:

```text
flags > environment variables > config file > defaults
```

Environment prefix:

```text
MYCOMPUTER_
```

Config file search order:

```text
./mycomputer.yaml
$XDG_CONFIG_HOME/mycomputer/config.yaml
~/.config/mycomputer/config.yaml
/etc/mycomputer/config.yaml
```

The `config --json` command must report effective values, source of each value, and available backends.

### Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Generic or unclassified error. |
| `2` | User input or validation error. |
| `3` | Target not found. |
| `4` | Desktop, browser, or external dependency unavailable. |
| `5` | Precondition or state error, such as unsupported session type or locked desktop. |
| `6` | Cancelled by signal, context cancellation, or timeout. |

### CLI Error Shape

With `--json`, errors are written to stderr as one JSON object:

```json
{"error":{"code":"DISPLAY_UNAVAILABLE","message":"cannot connect to X11 display","details":{"display":":0"}}}
```

Without `--json`, errors must be actionable text on stderr.

## MCP Surface

The MCP server runs over stdio:

```bash
mycomputer serve
```

Tool names are stable public contracts once implemented. The MVP tool surface is grouped by behavior, but exposed as flat MCP tools for agent discoverability.

### Diagnostics And Discovery

| Tool | Read-only | Purpose |
| --- | --- | --- |
| `doctor` | yes | Return readiness, detected session type, available backends, blockers, and recommended next action. |
| `get_screen_info` | yes | Return screen dimensions, monitor bounds, coordinate space, and scaling assumptions. |
| `list_windows` | yes | Return visible top-level windows with ids, titles, classes, PID when available, focus state, and bounds. |
| `focused_window` | yes | Return the currently focused window metadata. |
| `observe` | mostly | Return screenshot metadata, focused window, window list, cursor position, and optional AT-SPI tree. |

`observe` may capture the screen and may reveal local desktop contents. It must be annotated as read-only from the application's perspective but privacy-sensitive.

### Screen Capture

| Tool | Mutating | Purpose |
| --- | --- | --- |
| `screenshot` | no, except optional focus/crop preparation | Capture full screen, window, region, or zoom crop. |

The `screenshot` tool supports:

- Full display capture.
- Region capture.
- Window capture by id or selector.
- Zoom crop by region or point plus radius.
- PNG output by default.
- Optional JPEG output for smaller model payloads.
- Optional cursor overlay when XFixes is available.

Every screenshot response includes a coordinate map.

```json
{
  "image_path": "/tmp/mycomputer-shot.png",
  "mime_type": "image/png",
  "capture_bounds": {"x": 0, "y": 0, "width": 1920, "height": 1080},
  "image_size": {"width": 1568, "height": 882},
  "coord_map": "0,0,1920,1080,1568,882"
}
```

### Desktop Actions

| Tool | Mutating | Purpose |
| --- | --- | --- |
| `focus_window` | yes | Focus a window by id, title, class, PID, or app hint. |
| `move_mouse` | yes | Move pointer to physical coordinates or mapped screenshot coordinates. |
| `click` | yes | Click by coordinate or AT-SPI element id; supports button and count. |
| `drag` | yes | Drag from one coordinate to another with optional duration. |
| `scroll` | yes | Scroll vertically or horizontally at a coordinate or element. |
| `type_text` | yes | Type literal text into the focused target or target window. |
| `press_key` | yes | Press a key or chord, such as `enter`, `ctrl+l`, or `alt+tab`. |
| `set_text` | yes | Set text through AT-SPI when available; fallback behavior must be explicit. |
| `perform_action` | yes | Invoke an AT-SPI action such as press, activate, or toggle. |
| `computer_actions` | yes | Execute a validated ordered batch of focus, input, wait, screenshot, and observe actions. |

Action tools must validate:

- Coordinate bounds.
- Coordinate map consistency.
- Required target existence.
- Supported button/key names.
- Session readiness.

Action tools must not silently fall back between semantic and pixel targeting. If fallback is used, the response must say which backend executed the action.

### Browser Tools

| Tool | Mutating | Purpose |
| --- | --- | --- |
| `browser_session` | yes | Report CDP readiness, or start a Chromium-family browser through CDP when `launch: true`. |
| `browser_pipeline` | yes | Execute ordered browser operations in one call. |

Supported browser pipeline steps:

- `navigate`
- `wait_for_load`
- `wait_for_selector`
- `click_selector`
- `fill_selector`
- `press_key`
- `scroll`
- `screenshot`
- `get_url`
- `get_title`
- `get_dom_text`

The browser surface is not an exec tool. It may launch a detected browser binary or use an explicit CDP endpoint. It must not accept arbitrary shell commands.

## Coordinate Contract

MyComputer uses physical X11 coordinates internally.

Screenshot-derived coordinates require a `coord_map`:

```text
capture_x,capture_y,capture_width,capture_height,image_width,image_height
```

Pointer tools accept either:

- Physical coordinates: `{"x": 100, "y": 200, "space": "screen"}`.
- Screenshot coordinates: `{"x": 82, "y": 163, "space": "screenshot", "coord_map": "0,0,1920,1080,1568,882"}`.

Off-screen coordinates are validation errors unless a future explicit unsafe flag is added.

## JSON Conventions

Data-emitting CLI commands support `--json`.

Rules:

- Stable top-level keys.
- No ANSI in JSON.
- Scalars return one JSON object.
- Lists may return JSON arrays when bounded.
- Streams use NDJSON.
- Long text fields obey `--max-chars`.
- Minimal output is tab-separated with stable columns documented per command.

MCP tool results use structured JSON content first. Image paths and base64 payload support are implementation details to decide during implementation; the contract must remain explicit per tool.

## Safety Contract

MyComputer can observe private desktop contents and mutate local application state. The server is local-only by default, but local access is still powerful.

MVP safety rules:

- No arbitrary exec or shell command tool.
- No telemetry.
- No network listener by default.
- No interactive prompts on the default CLI path.
- MCP tools use annotations to distinguish read-only, privacy-sensitive, and mutating actions.
- Mutating tools identify the target backend used in responses.
- Actions validate bounds and target identity before execution.
- Browser pipeline must reject arbitrary command strings.
- Screenshots and action logs stay local unless the MCP host sends them elsewhere.

Recommended MCP host behavior:

- Ask the user before actions that may submit, purchase, delete, send, overwrite, or change external state.
- Treat screenshot and accessibility contents as untrusted input.

## Implementation Architecture

Language:

- Go.

Primary libraries and protocols:

- MCP: official Go MCP SDK.
- X11 protocol: `github.com/jezek/xgb`.
- Input: XTest extension.
- Screen geometry: XRandR, with Xinerama fallback.
- Window management: EWMH and ICCCM.
- Screenshot: X11 image capture first; MIT-SHM optimization can follow after correctness.
- Cursor overlay: XFixes when available.
- Accessibility: AT-SPI over D-Bus.
- Browser: Chrome DevTools Protocol through Go Rod or a direct CDP client.

MVP should not shell out to `xdotool`, `scrot`, or `wmctrl` as the primary implementation path. They can be referenced in diagnostics as user comparison tools, but not required for the core path.

## Repository Layout

Initial layout rule: feature-first under `internal/`, with narrow boundary packages and no bucket packages.

```text
cmd/mycomputer/main.go
cmd/mycomputer/root.go
cmd/mycomputer/serve.go
cmd/mycomputer/doctor.go
cmd/mycomputer/config.go
cmd/mycomputer/observe.go
cmd/mycomputer/capture.go
cmd/mycomputer/windows.go
cmd/mycomputer/focus.go
cmd/mycomputer/actions.go
cmd/mycomputer/browse.go
internal/a11y/
internal/browser/
internal/config/
internal/contract/
internal/diagnostic/
internal/image/
internal/input/
internal/mcpserver/
internal/pipeline/
internal/safety/
internal/screen/
internal/window/
internal/x11/
docs/
```

Rules:

- `main.go` only calls command execution and maps errors to exit codes.
- `internal/x11` owns raw X11 connection and extension setup.
- `internal/screen`, `internal/window`, and `internal/input` depend on `internal/x11`, not the reverse.
- `internal/diagnostic` composes readiness evidence from feature packages for `doctor`.
- `internal/mcpserver` adapts internal services to MCP tools.
- `internal/cli` may be introduced only if command plumbing becomes shared across multiple binaries or many command files.
- No `pkg/` in MVP unless a public Go library is intentionally shipped.
- No `util`, `common`, `helpers`, `shared`, or `misc` package.

## MVP Acceptance Criteria

- `mycomputer doctor --json` reports ready/not-ready with explicit blockers.
- `mycomputer serve` starts a stdio MCP server and exposes the MVP tools.
- Full-screen screenshot returns a valid PNG and coordinate map.
- Region or zoom capture returns a full-resolution crop and coordinate map.
- Click, drag, scroll, type, and keypress work in a normal X11 desktop app.
- Window listing and focusing work on at least one common X11 desktop environment.
- `observe` returns screenshot metadata, focused window, windows, cursor, and optional AT-SPI data.
- `computer_actions` can focus a window, click, type, wait, and return a final screenshot in one tool call.
- `browser_pipeline` can connect to or start Chromium, navigate to a page, fill a field, click, and return final URL/title/state.
- Wayland sessions fail gracefully with diagnostics rather than partial misleading behavior.
- No command or MCP tool can run arbitrary shell commands.

## Verification Matrix

Planned implementation checks:

| Check | Command / Evidence | Expected Result | Notes |
| --- | --- | --- | --- |
| Root help | `mycomputer --help` | pass | Lists commands and global flags. |
| Version JSON | `mycomputer version --json | jq type` | pass | Emits one JSON object. |
| Config JSON | `mycomputer config --json | jq type` | pass | Includes effective values and available backends. |
| Doctor JSON | `mycomputer doctor --json | jq '.readiness.status'` | pass | Clear ready/not-ready state. |
| No color | `NO_COLOR=1 mycomputer --help | cat` | pass | No ANSI escapes. |
| Bad input exit | `mycomputer capture --region bad; echo $?` | `2` | Validation error. |
| Wayland diagnostics | Wayland-only session `mycomputer doctor --json` | exit `0` with readiness blockers | Doctor reports state even when actions are unsupported. |
| Unsupported action exit | Unsupported session `mycomputer actions --input-file fixture.json; echo $?` | `5` | Action precondition error. |
| Screenshot smoke | `mycomputer capture --out /tmp/shot.png --json` | pass | Valid image, coord map present. |
| MCP tools list | MCP initialize then `tools/list` | pass | Stable MVP tool names present. |
| MCP action batch | `computer_actions` fixture | pass | Focus, click, type, wait, screenshot. |
| Browser pipeline | `mycomputer browse --input-file fixture.json --json` | pass | Navigates and returns URL/title. |
| Tests | `go test ./...` | pass | Includes package tests. |
| Import cycles | `go list ./...` | pass | No import cycles. |

Current local evidence from the MVP implementation:

- `go test ./...` passes.
- `go list ./...` passes with no import cycles.
- `mycomputer --help` lists the MVP command surface and has no root `run`.
- `mycomputer doctor --json` reports the current X11 session as ready.
- `mycomputer capture` produced valid PNG, JPEG, region, zoom, and cursor-overlay screenshots with `coord_map`.
- `mycomputer observe --json` returned screen, windows, cursor, and AT-SPI elements.
- `mycomputer actions --input-file - --json` executed pointer movement, waits, screenshots, and AT-SPI `perform_action`.
- `mycomputer browse --input-file - --json` launched Chromium headless through CDP, navigated a data URL, filled a field, clicked a button, and returned final URL/title.
- MCP in-memory tests verify the stable MVP tool list and key input-schema names such as `max_edge`, `element_id`, and `browser_session.launch`.
- Invalid structured input returns JSON errors on stderr with the documented exit code `2`.

## Deferred From MVP

- Wayland-native control.
- OCR and template matching.
- Remote daemon or HTTP transport.
- Public Go library API.
- Multi-agent isolated desktops.
- Recording/replay format.
- Built-in policy engine for per-app approval.
- Hand-written shell completion beyond what the CLI framework can generate.

## Slop Test Preview

- Universal: no invented SLAs, quotas, telemetry, or rate limits.
- CLI: no root `run`, exit codes documented, config precedence documented, stdout/stderr split documented.
- Agentic: JSON required for data commands, output bounds documented, config introspection required, no silent backend fallback.
- Structural: layout rule documented, no bucket packages, no public `pkg/` until intentionally shipped.
