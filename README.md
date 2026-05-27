# mc

MyComputer is a local CLI and MCP server for X11 desktop computer use on
Linux. It gives coding agents (Claude Code, Codex, and other MCP hosts) a
scriptable way to observe and control GUI applications through screenshots,
window targeting, mouse and keyboard input, AT-SPI accessibility metadata,
and browser-driven CDP pipelines.

Use it when you need:

- A local computer-use tool for Linux that does not depend on cloud APIs,
  remote VMs, or screen-recording services
- An MCP server an agent can drive with natural prompts like "look at my
  screen and click the Submit button" — no per-action wiring required
- A CLI for scripting deterministic GUI flows (`observe` -> `find-text`
  -> `actions`) with structured JSON output and a stable error envelope

## Install

One-line install (downloads the latest release into `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh | bash
```

Inspect first, then run:

```sh
curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh -o install.sh
less install.sh
bash install.sh
```

Environment overrides:

```sh
VERSION=v0.3.0 bash install.sh                     # pin a specific tag
BIN_DIR=/usr/local/bin sudo bash install.sh        # system-wide install
```

Or with Go:

```sh
go install github.com/1broseidon/mc/cmd/mycomputer@latest
```

Or grab a binary from [releases](https://github.com/1broseidon/mc/releases).

The project ships as the `mycomputer` binary, not `mc`. The short repo name
is for the URL; the long binary name avoids the established `mc` in
Midnight Commander and MinIO Client. The brand in prose is "MyComputer".
The MCP server identifier (what an agent says) is `my-computer`.

Requires Go 1.26+ and a running X11 session (`echo $XDG_SESSION_TYPE`
should report `x11`). For OCR, install `tesseract` on the host. Native
Wayland is out of scope; `mycomputer doctor` reports blockers.

## Quick start

Confirm readiness, then observe and act. All data commands accept `--json`.

```sh
$ mycomputer doctor --json | jq '.readiness'
{
  "status": "ready",
  "next_action": "MCP server can be started with mycomputer serve"
}

$ mycomputer get-screen-info --json | jq '.monitors[0] | {name, bounds, scale, primary}'
{
  "name": "DP-4",
  "bounds": { "x": 0, "y": 0, "width": 3440, "height": 1440 },
  "scale": 1.1377083333333333,
  "primary": true
}

$ mycomputer windows --json | jq '.windows[0] | {id, title, class, bounds}'
{
  "id": "0x4200003",
  "title": "plank",
  "class": "Plank",
  "bounds": { "x": 0, "y": 1326, "width": 3440, "height": 114 }
}

$ mycomputer find-text --json --region 0,0,200,50 'no-such-label-here'
{"error":{"code":"TARGET_NOT_FOUND","message":"find_text returned no candidates","details":{"query":"no-such-label-here","region":{"x":0,"y":0,"width":200,"height":50}}}}
```

Every command emits the same `{ "error": { "code", "message", "details" } }`
envelope on failure and a stable exit code (2 validation, 3 not_found,
4 dependency_unavailable, 5 precondition, 6 cancelled). See
[`conventions.yaml`](conventions.yaml) for the full wire contract.

## Quick start: MCP

Run the stdio MCP server:

```sh
mycomputer serve
```

Register it with an MCP host (Claude Code, Codex, Claude Desktop, etc.) by
pointing the host at the built binary:

```json
{
  "mcpServers": {
    "my-computer": {
      "command": "/absolute/path/to/mycomputer",
      "args": ["serve"]
    }
  }
}
```

Tools exposed by the server mirror the CLI verbs and share the same
JSON envelope. Read-only tools (`doctor`, `list_windows`, `observe`,
`screenshot`, `find_text`, ...) are registered before mutating tools
(`click`, `type_text`, `paste`, `computer_actions`, ...).

## Commands at a glance

| Command | What it does |
|---|---|
| `doctor` | Probe X11, XTest, RandR, AT-SPI, OCR, clipboard, IME, audit, browser — report readiness and blockers |
| `observe` | One call: screen info, focused window, window list, and optional AT-SPI tree |
| `capture` | Full-screen, region, or zoom-crop screenshot to PNG/JPEG with downscale and cursor overlay |
| `windows` | List windows with bounds, decoration insets, class, PID, focus state |
| `get-screen-info` | Logical/physical bounds and per-monitor RandR geometry |
| `focus` | Focus a window by id, title, class, or PID |
| `find-text` | OCR a region (or the screen) and return candidate matches with bounds and confidence |
| `find-image` | Locate a template image by template matching |
| `find-color` | Sample a pixel or find color blobs within tolerance |
| `type-text` | Type literal text into the focused target (auto / xtest / paste) |
| `paste` | Send the paste shortcut to the focused window |
| `clipboard-read` / `clipboard-write` | Read or set X11 selections with a detached owner daemon |
| `wait-for-window` / `wait-for-text` / `wait-for-pixel-change` | Block until a target appears, text is read, or pixels settle |
| `window-{raise,move,resize,maximize,minimize,workspace,close}` | Window verbs; `window-close` is gated by `--allow-close` |
| `browse` | Run a browser pipeline through Chrome DevTools Protocol |
| `actions` | Execute a validated JSON action batch (supports `--dry-run`) |
| `audit` | Inspect and replay the MyComputer audit log |
| `serve` | Run the stdio MCP server |
| `config` | Show effective config and available backends as text or JSON |
| `conventions` | Inspect and regenerate the `conventions.yaml` surface contract |
| `doctor`, `version`, `help`, `completion` | The usual operability verbs |

All data commands accept `--json`. Bounded output is available via
`--minimal` (tab-separated) and `--max-chars N` for agents that need to
cap payload size.

## How it works

MyComputer is a layered backend stack with one verb surface on top:

1. **X11 + XTest** — synthetic input (mouse, keyboard, scroll) and
   pixel-accurate screenshots via `MIT-SHM` when available.
2. **RandR + XFixes** — multi-monitor geometry, scale factors, cursor
   overlay.
3. **AT-SPI** — accessibility tree on GTK/Qt apps for structured
   `find_text` and `perform_action` shortcuts that avoid OCR when the app
   exposes a label.
4. **Tesseract OCR** — fallback text search when AT-SPI is silent
   (Electron apps, native canvases, browsers).
5. **Template matching** — `find-image` with a tunable threshold for
   exact pixel matches.
6. **Chrome DevTools Protocol** — `browse` runs a scripted pipeline
   inside a headless or attached Chrome session when the target is the
   browser.
7. **X11 selections + IME** — clipboard read/write via a detached owner
   daemon so the selection survives past command exit.
8. **Audit log** — every mutating call writes a structured record; with
   `--audit-screenshots` it captures before/after PNGs, and with
   `--audit-full-payloads` it persists a sealed manifest so `audit replay`
   can reconstruct full inputs.

`mycomputer doctor` reports the status of every backend and tells the
caller what is missing. Coordinates are physical pixels by default;
`--logical-coords` is experimental HiDPI translation off the primary
monitor's RandR scale.

## AI agents

The MCP server is the primary surface for agents. Once registered as
`my-computer`, an agent can drive the desktop with high-level prompts:

- "Take a screenshot and tell me what's on screen" -> `screenshot` +
  `observe`
- "Click the Submit button" -> `find_text` + `click` (or the higher-level
  `click_text`)
- "Open the URL in Firefox and fill the login form" -> `focus_window` +
  `type_text` + `press_key`
- "Wait until the build output shows 'PASS'" -> `wait_for_text`

Read-only tools (`doctor`, `screenshot`, `observe`, `list_windows`,
`find_text`, `find_image`, `find_color`, `get_screen_info`,
`focused_window`, `clipboard_read`, `wait_for_*`) are registered before
mutating tools so an agent that introspects the catalog sees safe
verbs first. Mutating tools carry the same JSON error envelope as the
CLI; `computer_actions` request envelopes must declare
`schema_version: "0.2"`.

Safety:

- `--respect-user` (default on) yields the desktop when real input is
  detected — agent batches pause mid-flight.
- `--allow-close` gates `window_close`; without it the tool returns
  `PRECONDITION_CLOSE_NOT_ALLOWED`.
- `--dry-run` resolves and validates a mutating batch without touching
  the desktop; the result envelope carries `dry_run: true`.

The full operator-facing skill guide, including the
observe -> target -> act -> verify loop and worked recipes, lives in
[`skill/SKILL.md`](skill/SKILL.md).

### Install the skill

If your agent runtime supports skill packages, install MyComputer's
skill directly from this repo:

```sh
npx skills add 1broseidon/mc
```

The skill description triggers on natural-language patterns like
"use my computer to...", "screenshot my window", "click X on screen",
or "find Y on the display" — no per-prompt wiring required. Once
installed, the agent also knows how to detect MyComputer (via the
MCP tool list or `which mycomputer`) and how to install it via
`install.sh` if it's missing.

The skill source ships under [`skill/`](skill/) in this repo:
[`SKILL.md`](skill/SKILL.md) plus three reference docs covering
[CLI recipes](skill/references/cli-recipes.md),
[MCP tools](skill/references/mcp-tools.md), and
[desktop workflows](skill/references/desktop-workflows.md).

## Documentation

| Document | Purpose |
| --- | --- |
| [`docs/mycomputer-mvp-spec.md`](docs/mycomputer-mvp-spec.md) | Full MVP specification: verbs, schemas, readiness rules |
| [`conventions.yaml`](conventions.yaml) | Wire contract: error codes, envelopes, flag conventions |
| [`anvil.md`](anvil.md) | Locked public surface for the v0.3.x release line |
| [`docs/CHANGELOG.md`](docs/CHANGELOG.md) | Keep-a-Changelog ledger; wire-affecting changes land here |
| [`skill/SKILL.md`](skill/SKILL.md) | Agent-facing operating guide |
| [`skill/references/`](skill/references/) | CLI recipes and worked examples |

## License

[MIT](./LICENSE)
