# MCP Tools

Use the MCP server when the host has `my-computer` installed through:

```bash
mycomputer serve
```

## Discovery And Diagnostics

| Tool | Use |
| --- | --- |
| `doctor` | Readiness, blockers, session type, backend availability. |
| `get_screen_info` | Screen bounds and full per-monitor geometry (index, name, bounds, scale, primary, refresh_hz). |
| `list_windows` | Visible top-level windows. |
| `focused_window` | Current focus state. |
| `observe` | Combined desktop state, cursor, windows, accessibility, optional screenshot. AT-SPI elements always carry `app`, `toolkit`, and `window_id` string fields (empty when AT-SPI does not expose the value — never `?` or null). |

Start with `doctor` and `observe` for unfamiliar desktops.

### Monitors And HiDPI

`get_screen_info` returns the bounding rectangle of all monitors as `screen.bounds` (v0.1 wire compatible) plus a `monitors[]` array:

```json
{
  "bounds": {"x":0,"y":0,"width":5120,"height":1440},
  "backend": "x11",
  "monitors": [
    {"index":0,"name":"DP-1","bounds":{"x":0,"y":0,"width":3440,"height":1440},"scale":1.0,"primary":true,"refresh_hz":165},
    {"index":1,"name":"HDMI-1","bounds":{"x":3440,"y":0,"width":1680,"height":1050},"scale":1.0,"primary":false,"refresh_hz":60}
  ]
}
```

- `index` feeds `point.target.monitor_index` when `point.space="monitor"`.
- `scale` is the RandR-derived DPI factor (96 DPI = 1.0). MyComputer operates in **physical pixels** by default — `scale` is informational so agents can translate from app-reported logical coordinates to physical XTest coordinates when needed.
- `primary` reports the X primary monitor. Single-monitor systems always report `primary:true`.
- `refresh_hz` is the current mode's refresh rate (rounded). Omitted when RandR cannot determine it.

The experimental `--logical-coords` global flag (off by default) opts MyComputer into the translation layer: screenshot output dimensions are divided by the primary monitor's scale, and input coordinates from clients are multiplied by that scale before XTest. Production agents should keep the flag off and stick with physical pixels — turning it on couples the entire process to a single monitor's scale.

## Screenshots

Use `screenshot` for:

- full display capture,
- physical region capture,
- zoom crop,
- cursor overlay,
- PNG or JPEG output.

Important fields:

- `region`: `{ "x": 0, "y": 0, "width": 800, "height": 600 }`
- `max_edge`: downscale longest edge for model-friendly images.
- `cursor`: overlay pointer when XFixes supports it.
- `format`: `png` or `jpeg`.

Always preserve and use `coord_map` when converting screenshot coordinates back to screen coordinates.

## Desktop Actions

| Tool | Backend | Notes |
| --- | --- | --- |
| `focus_window` | X11/EWMH | Target by id, title, class, or PID. Verify focus visually. |
| `move_mouse` | XTest | Physical or screenshot-mapped coordinates. |
| `click` | XTest | Validate target bounds first. |
| `drag` | XTest | Include duration for human-like movement when needed. |
| `scroll` | XTest | Direction: `up`, `down`, `left`, `right`. |
| `type_text` | XTest | Types into the currently focused target. |
| `press_key` | XTest | Supports chords such as `ctrl+n`, `ctrl+l`, `alt+tab`. |
| `set_text` | AT-SPI | Requires `element_id` exposing EditableText. |
| `perform_action` | AT-SPI | Invoke actions such as `click`, `press`, `default.activate`. |
| `computer_actions` | mixed | Execute ordered batches. |

Use `computer_actions` for multi-step desktop workflows. Include `wait` and final `screenshot` steps in the batch.

## Browser Tools

| Tool | Use |
| --- | --- |
| `browser_session` | Report browser readiness; launch a browser when `launch: true`. |
| `browser_pipeline` | Navigate, wait, fill selectors, click selectors, press keys, scroll, screenshot, and extract DOM text. |

Prefer browser tools when the task is truly inside Chromium and selectors are known. Prefer desktop tools when validating the actual visible user session, native dialogs, downloads, file pickers, or cross-app behavior.

## Error Handling

MyComputer uses stable JSON errors:

```json
{"error":{"code":"POINT_OUT_OF_BOUNDS","message":"screen coordinate is outside the screen"}}
```

Treat these categories as actionable:

- validation: fix input shape, coordinate map, key name, selector, or target.
- not found: refresh `windows` or `observe`; app state changed.
- dependency: backend missing or unavailable.
- precondition: session or element does not support the requested action.
- cancelled: timeout or caller cancellation.
