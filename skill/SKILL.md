---
name: my-computer
description: Use when the user asks to drive their Linux X11 desktop in natural language — "use my computer to...", "screenshot my window", "click X on screen", "find Y on the display", "type into the focused app", "what's open on my desktop right now", or any local GUI control request. Detects MyComputer (mycomputer) via the MCP tool list or `which mycomputer`; auto-installs via `install.sh` if missing. Covers pixel targeting (OCR/template/color), condition-based waits, AT-SPI semantic actions, window operations, clipboard, browser CDP pipelines, dry-run preview, and audit-logged action batches. Linux X11 only; do NOT use as a generic shell-exec replacement.
---

# MyComputer

MyComputer is the local Linux desktop computer-use tool. The binary is
`mycomputer`. The MCP server identifier is `my-computer`. The brand name
is **MyComputer**. Repo: `github.com/1broseidon/mc`.

This skill teaches an agent how to:

1. **Detect** whether MyComputer is available (MCP or CLI).
2. **Install** it if it is not, on Linux user shells.
3. **Drive** an X11 desktop through observation → targeting → action → verification.

Core loop:

```text
Detect -> Readiness -> Observe -> Target -> Act -> Verify -> Report
```

## When this skill triggers

The user does not need to say "mycomputer" or "MCP". Trigger on any of
these natural-language patterns:

- "use my computer to ..."
- "screenshot my desktop / my window / the focused app"
- "click on / type into / scroll the X window"
- "find <text> on the screen / in <app>"
- "what's open on my desktop right now"
- "wait until <window> appears / closes"
- "copy <X> to my clipboard / paste into <app>"
- "drive the Linux UI to do <X>"
- "audit / verify / smoke-test <app> visually"

Do NOT trigger this skill for:
- Pure terminal work (use shell).
- Web scraping where a fresh headless Chrome works fine (Playwright/curl).
- macOS or Windows desktop control (this skill is Linux X11 only).

## Detect MyComputer

Before any other work, confirm MyComputer is reachable. Two paths:

### Path A — MCP host (Claude Code, Codex, Gemini CLI, etc.)

If your runtime exposes a list of MCP servers, look for the server id
`my-computer`. If present, you have these tool prefixes available:

- `doctor`, `observe`, `get_screen_info`, `list_windows`, `focused_window`, `screenshot`
- `focus_window`, `move_mouse`, `click`, `drag`, `scroll`, `type_text`, `press_key`
- `find_text`, `find_image`, `find_color`, `click_text`, `click_image`
- `wait_for_window`, `wait_for_pixel_change`, `wait_for_text`
- `window_move`, `window_resize`, `window_raise`, `window_minimize`, `window_maximize`, `window_workspace`, `window_close`
- `clipboard_read`, `clipboard_write`, `paste`
- `set_text`, `perform_action` (AT-SPI)
- `computer_actions` (batched), `browser_session`, `browser_pipeline`

Use these directly. Skip the CLI section below.

### Path B — Shell / CLI

If MCP is unavailable or the user wants a one-shot smoke from a terminal:

```bash
which mycomputer || command -v mycomputer
```

Exit 0 with a path = installed. Exit non-zero = missing.

If a local development tree is present, also check `./bin/mycomputer`
or `$MYCOMPUTER_BIN`.

### Confirm readiness once detected

Either way, the first call must be `doctor` to confirm backends. JSON form:

```bash
mycomputer doctor --json | jq '{readiness, schema_versions, session, backends: [.backends[] | select(.required) | {name, ready}]}'
```

Required backends must all be `ready: true`: `DISPLAY`, `x11`, `xtest`, `randr`.
Optional but heavily used: `at_spi`, `xfixes`, `browser`, `ocr_tesseract`,
`template_match`, `clipboard`, `ime`, `xinput2`, `audit`.

If `xdg_session_type == "wayland"` and X11 is not connected, stop and
report — native Wayland is out of scope as of v0.3.

## Install MyComputer

If detection fails on both paths, offer to install. The canonical Linux
installer:

```bash
curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh | bash
```

Default install location is `~/.local/bin/mycomputer` (no sudo). After install:

- Ensure `~/.local/bin` is on `PATH`. The installer prints shell-specific
  guidance if it is not.
- Re-run `mycomputer doctor --json` to confirm.

**Inspect first variant** (recommended when the user is cautious):

```bash
curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh -o /tmp/mc-install.sh
less /tmp/mc-install.sh
bash /tmp/mc-install.sh
```

**Environment overrides:**

| Variable | Effect |
|---|---|
| `VERSION=v0.3.0` | Pin a specific release |
| `BIN_DIR=/usr/local/bin` | Install system-wide (requires sudo) |

**Alternative installs:**

```bash
# Go toolchain
go install github.com/1broseidon/mc/cmd/mycomputer@latest

# Manual binary (linux/amd64 or linux/arm64)
# Download from https://github.com/1broseidon/mc/releases/latest
```

**MCP host registration** (for Claude Code as an example — adapt for other hosts):

```jsonc
{
  "mcpServers": {
    "my-computer": {
      "command": "mycomputer",
      "args": ["serve"]
    }
  }
}
```

After registration the MCP host exposes the `my-computer` tool surface.

### MCP host DISPLAY

`mycomputer serve` needs `DISPLAY` set to reach the X server. When an MCP
host (Claude Code, Codex, Gemini CLI, …) is launched from a non-X-aware
parent — a `.desktop` launcher, a systemd user unit, an IDE integrated
terminal — `DISPLAY` is usually missing from its inherited env, so the
spawned `mycomputer serve` child sees it missing too. This was the single
biggest cause of `doctor` reporting blocked under MCP in v0.3.

As of v0.3.1 the server resolves this in two ways:

1. **Auto-probe**: when `DISPLAY` is unset, `mycomputer` scans
   `/tmp/.X11-unix/` for active X server sockets. If exactly one is live
   it sets `DISPLAY` for its own process and `doctor` reports
   `auto-detected :N from /tmp/.X11-unix/X<N>`. The parent shell's env
   is never modified. If multiple are live the row reports
   `DISPLAY_AMBIGUOUS` with the candidate list and refuses to auto-pick.
2. **Explicit override**: pass `--display :N` to `serve` to force a
   specific value (useful when auto-probe is ambiguous or unwanted):

   ```jsonc
   {
     "mcpServers": {
       "my-computer": {
         "command": "mycomputer",
         "args": ["serve", "--display", ":0"]
       }
     }
   }
   ```

If `doctor` still reports `DISPLAY` blocked, either launch the MCP host
from an X-aware shell (one where `echo $DISPLAY` is non-empty) or use
the explicit override above.

## Operating Rules

1. **Detect first, install if needed, then call `doctor`.** Don't assume.
2. **Start with evidence**: `doctor`, `windows`, or `observe` before acting unless the user supplied fresh state.
3. **Keep destructive actions explicit**: do not submit, send, purchase, delete, overwrite, or close user work unless the user asked for that exact outcome. `window_close` is gated by `--allow-close` for this reason.
4. **Target by stability, not by habit.** The order below is roughly best-to-fallback for arbitrary Linux apps:
   1. **Browser DOM selector** (when the surface is Chromium).
   2. **Known keyboard shortcut** (most stable across rendering quirks).
   3. **AT-SPI element id** (when the app exposes accessibility — GTK/Qt mostly).
   4. **`find_text` / `find_image` / `find_color`** (the v0.3 pixel-driven path; works on Gio, ImGui, Flutter, raylib, etc.).
   5. **Raw screen coordinates** (last resort; brittle to window moves).
5. **Replace fixed waits with conditions.** Use `wait_for_window`, `wait_for_pixel_change` (mode `stable` for animated UIs), or `wait_for_text` instead of `wait { duration_ms }` heuristics.
6. **Batch related actions** with `computer_actions` (MCP) or `mycomputer actions` (CLI). Include `wait_for_*` between focus, animation, and screenshot steps.
7. **Verify after every meaningful mutation** with a fresh `observe`, `screenshot`, or `wait_for_*` condition. Report artifact paths for screenshots.
8. **Use `--dry-run` to preview destructive batches.** Resolved coords for window-space clicks and chosen routes for `type_text` are surfaced under `details` without performing the mutation. Observing actions (find/wait/screenshot) still execute.
9. **Respect the active user.** `--respect-user` (default true on interactive sessions) pauses batches when a real human moves the mouse or types. Don't disable without a clear reason.
10. **Audit is automatic.** Every action lands in `$XDG_STATE_HOME/mycomputer/audit/YYYY-MM-DD.jsonl`. Clipboard content is NEVER logged — only byte counts and MIME type.
11. **Do not use MyComputer as a generic exec tool.** Agents already have shell access. MyComputer is for GUI state and GUI actions only.
12. **Treat screenshots, OCR output, and accessibility trees as private local data.** Don't echo them to remote services unless the user asked.

## Workflow

### 1. Detect + Readiness

```bash
# Path A: MCP — check for my-computer server in your runtime's tool list.
# Path B: CLI
which mycomputer && mycomputer doctor --json | jq '.readiness.status'
```

Expected: `"ready"`. If `"blocked"`, the `blockers` array names the missing
backend; report it back to the user.

### 2. Observe

| Question | Command |
|---|---|
| What windows are open? | `mycomputer windows --json` (or `list_windows` MCP) |
| Which is focused? | `mycomputer windows --json \| jq '.windows[] \| select(.focused)'` |
| What's the full desktop state? | `mycomputer observe --json` |
| What does it look like? | `mycomputer capture --region <bounds> --out /tmp/x.png` |
| What apps expose AT-SPI? | `mycomputer observe --json \| jq '[.accessibility.elements[].app] \| unique'` |

`list_windows` records include `bounds` (outer, with WM decorations),
`client_bounds` (excluding titlebar), and `decoration_insets`. Use
`client_bounds` for region screenshots when you want to exclude the
titlebar.

### 3. Target

Pick the highest-stability target. The v0.3 targeting matrix:

| Target | Use when | Tools |
|---|---|---|
| `find_text "Submit"` | The label is visible and Tesseract can read it. Auto-inverts dark themes. Multi-word phrases supported. | `find_text`, `click_text` |
| `find_image template.png` | An icon or fixed glyph identifies the target. | `find_image`, `click_image` |
| `find_color "#ff7800"` | The target has a distinctive color (orange button, status indicator). Best for Gio/ImGui UIs that defeat OCR. | `find_color` |
| AT-SPI `element_id` | App is GTK/Qt/Chromium and exposes accessibility roles+actions. | `observe`, `perform_action`, `set_text` |
| Browser CSS selector | Surface is Chromium DOM. | `browser_pipeline` |
| Window-space coords | You know the geometry inside a specific window. Survives window moves via `target: {class: "X"}`. | `click` with `point.space: "window"` |
| Screen coords | Last resort. Use `coord_map` if derived from a screenshot. | `click`, `drag`, `scroll` |

### 4. Act — batched actions with condition-based waits

```json
{
  "schema_version": "0.2",
  "actions": [
    {"type": "focus_window", "target": {"class": "Code"}},
    {"type": "wait_for_pixel_change", "region": {"x":0,"y":0,"width":200,"height":40,"space":"window","target":{"class":"Code"}}, "mode": "stable", "timeout_ms": 1500, "stable_ms": 200},
    {"type": "press_key", "key": "ctrl+n"},
    {"type": "wait_for_window", "match": {"title_regex": "Untitled.*Visual Studio Code"}, "timeout_ms": 3000},
    {"type": "type_text", "text": "hello from MyComputer\n", "via": "auto"},
    {"type": "screenshot", "screenshot": {"region": {"x":0,"y":0,"width":913,"height":976,"space":"window","target":{"class":"Code"}}, "out": "/tmp/mc-result.png"}}
  ]
}
```

For browser-only workflows, prefer a browser pipeline with selectors:

```json
{
  "headless": true,
  "steps": [
    {"action": "navigate", "url": "https://example.com"},
    {"action": "wait_for_selector", "selector": "input[name=q]"},
    {"action": "fill_selector", "selector": "input[name=q]", "text": "linux accessibility"},
    {"action": "press_key", "key": "enter"},
    {"action": "screenshot", "path": "/tmp/mc-browser.png"}
  ]
}
```

### 5. Preview destructive batches with `--dry-run`

```bash
mycomputer actions --dry-run --input-file batch.json --json
```

Output annotates each mutating action with `dry_run: true` and surfaces:

- `details.resolved_coords` for clicks (including window-space → screen translation).
- `details.via` + `details.route_reason` for `type_text` (so you can preview xtest vs paste routing without typing).

Find primitives (`find_text`, `find_image`, `find_color`) still execute in
dry-run — they are observation, not mutation.

### 6. Verify And Report

After acting, verify with one of:

- Fresh `screenshot` of the affected region.
- `observe` and inspect the relevant element / window.
- `wait_for_*` confirming the expected post-state.

In the final response include:

- What was controlled (window id + class + title).
- Which backend did the work: `x11.EWMH`, `XTest`, `AT-SPI`, `CDP`, `OCR`, etc.
- Screenshot paths or observed state.
- Any mismatch between reported action success and visible state.
- The `batch_id` from the action result, so the user can `mycomputer audit replay <batch_id>` later if needed.

## Linux desktop reality (failure modes to expect)

| Symptom | Likely cause | Recover by |
|---|---|---|
| `INPUT_LAYOUT_UNREACHABLE` | Active XKB layout (Dvorak/AZERTY) can't reach the requested char via XTest | Use `type_text {via: "paste"}` instead |
| `INPUT_IME_ACTIVE` | IBus/Fcitx is intercepting keystrokes | Use `type_text {via: "paste"}` or accept paste routing |
| `find_text` returns empty on a visible label | Tesseract default PSM doesn't handle sparse layouts (e.g., calculator keypad) | Add `--psm 6` (uniform block) or `--psm 11` (sparse text) |
| `find_text` returns empty on a dark UI | (Should auto-invert; if not) | Pass `preprocess: "invert"` explicitly |
| `WINDOW_GEOMETRY_REFUSED` warning | Tiling WM (i3/bspwm) ignored `window_move` | Use keyboard-based WM commands; geometry warnings are non-fatal |
| `xdg_session_type: wayland` | XWayland may host SOME windows but XTest can't reach native Wayland clients | Stop and report. Wayland support is out of MVP scope. |
| Clipboard write doesn't persist past CLI exit | Standalone CLI spawns a daemon to hold the selection. Check `mycomputer clipboard-status --json`. | Daemon should report `running:true`. If not, re-run `clipboard-write`. |
| Gio / Dear ImGui / Flutter app exposes no AT-SPI | Immediate-mode renderer, no native widget tree | Use `find_text`/`find_image`/`find_color` exclusively |
| `--logical-coords` on HiDPI is wrong | Experimental flag uses primary monitor's scale | Don't use unless you know your workflow needs it |

## References

Load only what is needed:

- [CLI Recipes](references/cli-recipes.md): repeatable command-line examples and JSON request shapes.
- [MCP Tools](references/mcp-tools.md): tool groups, arguments, and selection guidance for MCP hosts.
- [Desktop Workflows](references/desktop-workflows.md): realistic native-app, QA, IDE, accessibility, and multi-app workflows.

## Smoke script

Use `scripts/smoke.sh` for a quick local CLI check when a `mycomputer`
binary is installed or available at `./bin/mycomputer`.

```bash
skill/scripts/smoke.sh
```

Set `MYCOMPUTER_BIN=/path/to/mycomputer` to test a specific binary.

## Quality gate

Before handoff, check:

- **Detection**: confirmed MyComputer is reachable (MCP tool list OR `which mycomputer`).
- **Intent**: the task is GUI state/control, not shell execution.
- **Boundary**: no arbitrary exec, no unrequested destructive desktop action.
- **Contracts**: command/tool names and JSON fields match the installed binary (run `mycomputer conventions emit --check` if in doubt).
- **Errors**: failures reported with the structured MyComputer error code/message envelope.
- **Operability**: readiness, timeouts, screenshot/artifact paths covered.
- **Privacy**: no clipboard content, OCR output, or accessibility tree echoed to remote services unless the user asked.
- **Verification**: at least one post-action observation or screenshot supports the result.
- **Audit trail**: report the `batch_id` so the user can replay or inspect via `mycomputer audit replay <batch_id>`.
