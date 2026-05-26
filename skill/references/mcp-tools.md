# MCP Tools

The `my-computer` MCP server is started with:

```bash
mycomputer serve
```

Register it with your MCP host (Claude Code, Codex, Gemini CLI, etc.).
Example `mcpServers` block:

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

This reference groups every tool the server exposes. Start with `doctor`
and `observe` for unfamiliar desktops.

## Discovery and diagnostics

| Tool | Use |
| --- | --- |
| `doctor` | Readiness, blockers, session type, backend availability, `schema_versions`, session-level flags (`respect_user`, `allow_close`, `logical_coords`). |
| `get_screen_info` | Screen bounds plus per-monitor geometry (`index`, `name`, `bounds`, `scale`, `primary`, `refresh_hz`). |
| `list_windows` | Visible top-level windows. Each record carries outer `bounds`, `client_bounds`, and `decoration_insets`. |
| `focused_window` | The currently focused window. |
| `observe` | Combined desktop state: cursor, windows, AT-SPI elements (with `app`, `toolkit`, `window_id` always populated as strings — empty when AT-SPI does not expose them), optional screenshot. |

### Monitors and HiDPI

`get_screen_info` returns the bounding rectangle of all monitors as
`screen.bounds` (v0.1 wire compatible) plus a `monitors[]` array:

```json
{
  "bounds": {"x":0,"y":0,"width":5120,"height":1440},
  "monitors": [
    {"index":0,"name":"DP-1","bounds":{"x":0,"y":0,"width":3440,"height":1440},"scale":1.0,"primary":true,"refresh_hz":165},
    {"index":1,"name":"HDMI-1","bounds":{"x":3440,"y":0,"width":1680,"height":1050},"scale":1.0,"primary":false,"refresh_hz":60}
  ]
}
```

- `index` feeds `point.monitor_index` when `point.space="monitor"`.
- `scale` is the RandR-derived DPI factor (96 DPI = 1.0). MyComputer
  operates in **physical pixels** by default — `scale` is informational
  so agents can translate from app-reported logical coordinates to
  physical XTest coordinates when needed.
- `primary` reports the X primary monitor. Single-monitor systems always
  report `primary:true`.
- `refresh_hz` is the current mode's refresh rate (rounded). Omitted
  when RandR cannot determine it.

The experimental `--logical-coords` global flag (off by default) opts
MyComputer into the translation layer. Production agents should keep
the flag off and stick with physical pixels.

## Screenshots

| Tool | Use |
| --- | --- |
| `screenshot` | Full display, region, zoom crop, cursor overlay, PNG or JPEG. |

Key fields:

- `region`: `{ "x": 0, "y": 0, "width": 800, "height": 600 }`. v0.3 accepts
  the extended `RegionRef` shape with `space: "window"|"window_frame"|"monitor"`
  and `target` or `monitor_index`. See [Region targeting](#region-targeting).
- `max_edge`: downscale the longest edge for model-friendly images.
- `cursor`: overlay pointer when XFixes is available.
- `format`: `png` or `jpeg`.

Every screenshot response includes `coord_map` so screenshot coordinates
translate back to absolute screen coordinates. When `image_size` matches
`capture_bounds`, no translation is needed.

## Pixel targeting (v0.3)

The killer family for apps that don't expose AT-SPI (Gio, Dear ImGui,
Flutter-Linux, raylib, egui, Slint, etc.).

| Tool | Use |
| --- | --- |
| `find_text` | OCR via Tesseract. Auto-inverts dark themes (luminance < 80/255). Multi-word phrase matching up to 6 tokens. Tunable `preprocess`, `psm`, `oem`. |
| `find_image` | Template match. `gocv` when built with `-tags gocv` + OpenCV libs; pure-Go normalized-cross-correlation otherwise. |
| `find_color` | Color blob detection. Two modes: sample a single pixel (`point`) or find contiguous blobs matching a hex color within tolerance. |
| `click_text` | Sugar: `find_text` → click highest-confidence candidate. |
| `click_image` | Sugar: `find_image` → click. |

Common find-result envelope:

```json
{
  "candidates": [
    {"bounds":{"x":1570,"y":769,"width":50,"height":11},"confidence":0.96,"source":"ocr","extra":{"lang":"eng","text":"Familiar","preprocess":"invert"}}
  ],
  "search_region": {"x":1030,"y":201,"width":913,"height":976},
  "coord_space": "screen"
}
```

Candidates are sorted by confidence desc, then top-to-bottom, left-to-right.

### find_text parameters

| Field | Default | Notes |
| --- | --- | --- |
| `query` | — | Literal substring OR regex (with `regex:true`). Multi-word phrases supported. |
| `region` | focused window or full screen | Accepts plain `Bounds` or extended `RegionRef`. |
| `lang` | `"eng"` | Tesseract language code. |
| `preprocess` | `"auto"` | `auto` \| `invert` \| `binarize` \| `none`. Auto-inverts dark regions. |
| `psm` | tesseract default | Page segmentation mode (0–13). Use `6` for uniform block, `11` for sparse layouts (calculator keypads, icon grids). |
| `oem` | tesseract default | OCR engine mode (0–3). |
| `case_sensitive` | `false` | |
| `regex` | `false` | |
| `min_confidence` | `0` | Filter candidates below this score. |
| `strict` | `false` | When true, multiple candidates above `min_confidence` → `TARGET_AMBIGUOUS`. |

### Common gotchas

- Dark UI + standard PSM: try `psm: 11` (sparse text).
- Multi-word query returns 0 candidates but single-word matches: query tokens may not appear consecutively in OCR output. Re-check region or use shorter phrase.
- Anti-aliased font + low contrast: use `preprocess: "binarize"` (Otsu).
- Gio / ImGui apps: `find_color` often beats `find_text` for distinctive UI elements (orange "=" button, status dots).

## Wait conditions (v0.3)

Replace fixed `wait { duration_ms }` heuristics.

| Tool | Use |
| --- | --- |
| `wait_for_window` | Block until a window matching `{id\|class\|title\|title_regex\|pid}` appears (default) or disappears (`present:false`). |
| `wait_for_pixel_change` | Block until a region's pixels change (`mode:"any"`) or stop changing (`mode:"stable"` with `stable_ms`). dhash-based. |
| `wait_for_text` | Block until OCR finds (or with `present:false` stops finding) text in a region. Slower than the others because OCR runs per poll — default `poll_ms: 250`. |

All wait results include `polls`, `elapsed_ms`, and `last_state` (for
debugging from the response alone). Timeout = precondition error (exit
5), code `WAIT_TIMEOUT`.

## Window operations (v0.3, EWMH-backed)

| Tool | Backend | Notes |
| --- | --- | --- |
| `window_move` | `_NET_MOVERESIZE_WINDOW` | Tiling WMs may refuse → `WINDOW_GEOMETRY_REFUSED` warning, ok:true. |
| `window_resize` | `_NET_MOVERESIZE_WINDOW` | Same tiling caveat. |
| `window_raise` | `_NET_ACTIVE_WINDOW` + restack fallback | |
| `window_minimize` | `_NET_WM_STATE` | |
| `window_maximize` | `_NET_WM_STATE` | `axis: "both"\|"horz"\|"vert"`. |
| `window_workspace` | `_NET_WM_DESKTOP` | Zero-based index. |
| `window_close` | `_NET_CLOSE_WINDOW` | **Gated** by `--allow-close` CLI flag OR `allow_close:true` on the batch. Returns `PRECONDITION_CLOSE_NOT_ALLOWED` otherwise. |

## Desktop input

| Tool | Backend | Notes |
| --- | --- | --- |
| `focus_window` | X11/EWMH | Target by id, class, title (substring), or PID. |
| `move_mouse` | XTest | Accepts every coord space. |
| `click` | XTest | `button` (`left`|`right`|`middle`), `count`. |
| `drag` | XTest | `from` → `to` with optional `duration_ms`. |
| `scroll` | XTest | `direction: "up"\|"down"\|"left"\|"right"`. |
| `type_text` | XTest or clipboard paste | `via: "auto"|"xtest"|"paste"`. Auto chooses paste for len>64, non-ASCII, control chars (except `\t`/`\n`), or when an IME is active. |
| `press_key` | XTest | Single chars (a-z, 0-9, common punct) plus chords (`ctrl+n`, `alt+tab`, `F1`, `Return`, `Escape`, etc.). Unreachable layout chars → `INPUT_LAYOUT_UNREACHABLE`. |

## AT-SPI semantic actions

| Tool | Use |
| --- | --- |
| `set_text` | Set the text on an AT-SPI `EditableText` element. Requires `element_id`. |
| `perform_action` | Invoke an AT-SPI action like `click`, `press`, `default.activate` on an element. |

Element ids come from `observe.accessibility.elements[].id` in the shape
`<bus>|<path>` (e.g. `:1.43|/org/a11y/atspi/accessible/12`). When an
element belongs to a window, `window_id` cross-references the X11 id
from `list_windows`.

## Clipboard (v0.3)

| Tool | Use |
| --- | --- |
| `clipboard_read` | Read the current `CLIPBOARD` or `PRIMARY` selection as `text/plain` or `text/uri-list`. |
| `clipboard_write` | Write to the selection. The MCP server owns it for the server's lifetime. |
| `paste` | Send `ctrl+v` (or `shift+insert` with `method:"insert"`) to the focused window. Only `selection:"clipboard"` is valid; X11 `ctrl+v` always targets CLIPBOARD. |

Clipboard **content** is never logged to the audit trail — only byte
count and MIME type. Standalone CLI `mycomputer clipboard-write` spawns
a daemon to hold the selection past process exit.

## Batched actions

| Tool | Use |
| --- | --- |
| `computer_actions` | Execute an ordered list of actions in one call. Schema-versioned. Supports `dry_run`, `respect_user`, `allow_close`, `resume_from`, `quiet_ms`, `yield_timeout_ms`. |

Use `computer_actions` for any multi-step workflow. Include `wait_for_*`
between focus, animation, and screenshot steps. The result envelope
returns each per-action outcome plus `batch_id`, which the user can
later replay via `mycomputer audit replay <batch_id>` (dry-run by
default; full payload manifest requires `--audit-full-payloads` to have
been set on the original run).

## Browser

| Tool | Use |
| --- | --- |
| `browser_session` | Report browser readiness; launch a browser when `launch:true`. |
| `browser_pipeline` | Navigate, wait for selectors, fill/click selectors, press keys, scroll, screenshot, extract DOM text. |

Prefer browser tools when the surface is genuinely inside a Chromium
DOM. Prefer desktop tools (`find_text`/`window_*`/etc.) when validating
the actual visible user session, native dialogs, downloads, file
pickers, or cross-app behavior.

## Region targeting

Every region-accepting tool (`screenshot`, `find_*`, `wait_for_*`)
accepts both shapes:

```json
// v0.1/v0.2: bare bounds, screen-space (still works)
{"region": {"x": 1030, "y": 201, "width": 913, "height": 976}}

// v0.3: extended RegionRef with coord space + target
{"region": {"x": 0, "y": 0, "width": 200, "height": 200,
            "space": "window", "target": {"class": "fam-ui"}}}
```

Spaces: `"screen"` (default), `"window"` (client_bounds origin),
`"window_frame"` (outer bounds), `"monitor"` (offset by `monitor_index`).
Resolution failures return precondition errors with the candidate list
attached.

## Point targeting

Same shape for click/move/drag points:

```json
{"point": {"x": 50, "y": 50, "space": "window", "target": {"id": "0x4200003"}}}
```

`point.space: "screenshot"` requires `coord_map` from a prior screenshot
response.

## Schema versioning

Every `computer_actions` request must carry `schema_version: "0.2"`
(current). Missing field → `VALIDATION_SCHEMA_VERSION_REQUIRED` (exit 2).
Unrecognized version → `VALIDATION_SCHEMA_VERSION_UNSUPPORTED`. Explicit
`"0.1"` is accepted for backward compatibility with the v0.1 wire shapes
that did not include the field.

## Error handling

MyComputer uses a stable JSON error envelope:

```json
{"error": {"code": "STABLE_CODE", "message": "human-readable", "details": {...}}}
```

Error categories map to exit codes:

| Code class | Exit | Treat as |
| --- | --- | --- |
| `VALIDATION_*` | 2 | Fix input shape, coord map, key name, selector, or target. |
| `*_NOT_FOUND`, `WINDOW_NOT_FOUND` | 3 / 5 | Refresh `windows` or `observe`; app state changed. |
| `DEPENDENCY_*` | 4 | Backend missing or unavailable (e.g., Tesseract not installed). |
| `PRECONDITION_*`, `WAIT_TIMEOUT`, `TARGET_NOT_FOUND` | 5 | Session or element does not support the requested action; retry or escalate. |
| `CANCELLED`, `YIELDED_TO_USER` | 6 | Timeout, ctx cancel, or user reclaimed the desktop via `--respect-user`. |
