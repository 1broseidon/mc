---
name: my-computer
description: Use when Codex needs to operate, test, or verify a real Linux X11 desktop through MyComputer/linux-computer-use, including screenshots, window focus, mouse/keyboard input, AT-SPI semantic actions, native app QA workflows, VS Code or IDE control, and browser CDP pipelines. Use for full-desktop computer-use tasks where visual verification and GUI state matter; do not use as a generic shell exec replacement.
---

# MyComputer

MyComputer is a local Linux desktop computer-use tool. Use it to observe and control an interactive X11 desktop through the `mycomputer` CLI or the `my-computer` MCP server.

Core loop:

```text
Readiness -> Observe -> Target -> Act -> Verify -> Report
```

## Operating Rules

1. Start with evidence: run `doctor`, `windows`, or `observe` before acting unless the user has already supplied fresh state.
2. Keep destructive actions explicit: do not submit, send, purchase, delete, overwrite, or close user work unless the user asked for that exact outcome.
3. Prefer semantic targeting when possible: AT-SPI element ids and browser selectors are usually more stable than raw pixels.
4. Use screenshot coordinates only with a `coord_map`; map screenshot points back to screen coordinates before clicking.
5. Batch related actions with `computer_actions` or `mycomputer actions` to reduce focus drift and round trips.
6. Verify after every meaningful mutation with `observe` or `screenshot`; report artifact paths for screenshots.
7. Do not use MyComputer as an exec tool. Agents already have shell access; use MyComputer for GUI state and GUI actions.
8. Treat screenshots and accessibility trees as private local desktop data.

## Workflow

### 1. Check Readiness

Use `mycomputer doctor --json` or the MCP `doctor` tool. Confirm:

- X11 is connected.
- XTest is available before pointer or keyboard actions.
- AT-SPI is available before semantic `set_text` or `perform_action`.
- Browser support exists before CDP pipelines.

If the session is native Wayland without an X11 display, stop and report the blocker.

### 2. Observe The Desktop

Use:

- `windows` / `list_windows` for top-level window ids, titles, classes, PIDs, and bounds.
- `observe` for screen bounds, focused window, cursor, windows, and AT-SPI elements.
- `capture` / `screenshot` for visual context and coordinate maps.

For visual tasks, capture before acting and after acting.

### 3. Choose The Targeting Strategy

Use the highest-stability target that fits the app:

| Target | Use When | Tooling |
| --- | --- | --- |
| Browser selector | The task is in a Chromium page and DOM selectors are available. | `browser_pipeline` or `mycomputer browse` |
| AT-SPI element id | Native app exposes accessible roles/actions/text. | `observe`, `perform_action`, `set_text` |
| Window target | You need to focus a known app/window. | `focus_window`, `windows` |
| Screen coordinate | Pixel action is unavoidable or app lacks semantics. | `screenshot`, `coord_map`, `click`, `drag`, `scroll` |

If a window focus action reports success but the screenshot shows another window on top, treat that as a focus/raise mismatch and recover by clicking the intended window or using a more semantic path.

### 4. Act In Small Batches

Prefer an ordered action batch:

```json
{
  "actions": [
    {"type": "focus_window", "target": {"class": "Code"}},
    {"type": "press_key", "key": "ctrl+n"},
    {"type": "type_text", "text": "hello from MyComputer\n"},
    {"type": "screenshot", "screenshot": {"out": "/tmp/mycomputer-result.png", "max_edge": 1200}}
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
    {"action": "screenshot", "path": "/tmp/mycomputer-browser.png"}
  ]
}
```

### 5. Verify And Report

After acting, verify with a fresh screenshot or observation. In the final response, include:

- What was controlled.
- Which backend did the work: X11/EWMH, XTest, AT-SPI, or CDP.
- Screenshot paths or relevant observed state.
- Any mismatch between reported action success and visible state.

## References

Load only what is needed:

- [CLI Recipes](references/cli-recipes.md): repeatable command-line examples and JSON request shapes.
- [MCP Tools](references/mcp-tools.md): tool groups, arguments, and selection guidance for MCP hosts.
- [Desktop Workflows](references/desktop-workflows.md): realistic native-app, QA, IDE, accessibility, and multi-app workflows.

## Smoke Script

Use `scripts/smoke.sh` for a quick local CLI check when a `mycomputer` binary is installed or available at `./bin/mycomputer`.

```bash
skill/scripts/smoke.sh
```

Set `MYCOMPUTER_BIN=/path/to/mycomputer` to test a specific binary.

## Quality Gate

Before handoff, check:

- Intent: the GUI task matches MyComputer rather than shell execution.
- Boundary: no arbitrary exec or unrequested destructive desktop action.
- Contracts: command/tool names and JSON fields match the installed binary.
- Errors: failures are reported with the MyComputer error code/message.
- Operability: readiness, timeout, screenshot, and artifact paths are covered.
- Verification: at least one post-action observation or screenshot supports the result.
