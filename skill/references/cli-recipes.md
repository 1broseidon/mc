# CLI Recipes

Use the CLI when MCP tools are unavailable, when debugging the local installation, or when a repeatable shell-level smoke is useful.

## Readiness And Inventory

```bash
mycomputer doctor --json
mycomputer config --json
mycomputer windows --json
mycomputer observe --json
mycomputer get-screen-info --json
```

Useful checks:

```bash
mycomputer doctor --json | jq '.readiness.status'
mycomputer windows --json | jq '.windows[] | {id,class,title,bounds,focused}'
mycomputer observe --json | jq '{screen:.screen.bounds, windows:(.windows|length), cursor:.cursor, a11y:.accessibility.status}'

# Per-monitor layout (index drives point.space="monitor"):
mycomputer get-screen-info --json | jq '.monitors[] | {index,name,bounds,scale,primary,refresh_hz}'

# Confirm a primary monitor is reported (always true on healthy setups):
mycomputer get-screen-info --json | jq '.monitors | length >= 1 and (map(.primary) | any)'
```

## Screenshots

Full screen:

```bash
mycomputer capture --out /tmp/mycomputer-full.png --json
```

Region:

```bash
mycomputer capture --region 100,100,900,600 --out /tmp/mycomputer-region.png --json
```

Zoom crop:

```bash
mycomputer capture --zoom 600,400,300 --out /tmp/mycomputer-zoom.png --json
```

Cursor overlay and JPEG:

```bash
mycomputer capture --cursor --format jpeg --out /tmp/mycomputer-shot.jpg --json
```

Every screenshot response includes:

- `capture_bounds`: physical X11 region.
- `image_size`: output image size after downscale.
- `coord_map`: `capture_x,capture_y,capture_width,capture_height,image_width,image_height`.

## Action Batch

Open a new VS Code editor and type text:

```bash
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "actions": [
    {"type": "focus_window", "target": {"class": "Code"}},
    {"type": "press_key", "key": "ctrl+n"},
    {"type": "click", "point": {"x": 1200, "y": 290, "space": "screen"}},
    {"type": "type_text", "text": "typed through MyComputer\n"},
    {"type": "screenshot", "screenshot": {"out": "/tmp/mycomputer-vscode.png", "max_edge": 1200, "cursor": true}}
  ]
}
JSON
```

Click from screenshot coordinates:

```json
{
  "actions": [
    {
      "type": "click",
      "point": {
        "x": 520,
        "y": 310,
        "space": "screenshot",
        "coord_map": "0,0,1920,1080,1568,882"
      }
    }
  ]
}
```

Use `wait` between focus, animation, modal, and screenshot steps:

```json
{"type": "wait", "duration_ms": 500}
```

## AT-SPI Semantic Actions

Every AT-SPI element in `observe` carries `app`, `toolkit`, and `window_id` string fields (empty string when AT-SPI does not expose the value — never `?` or null), so you can filter to a single app without bus-name spelunking:

```bash
# All clickable elements belonging to VS Code, grouped by role:
mycomputer observe --json \
  | jq -r '.accessibility.elements[]
            | select(.app == "Visual Studio Code")
            | select((.actions // []) | length > 0)
            | [.role, .name, ((.actions // []) | join(","))] | @tsv' \
  | sort -u

# Sanity check: no elements should ever report "?" as their app
mycomputer observe --json | jq '[.accessibility.elements[] | select(.app == "?")] | length'
# => 0

# Toolkit roll-up (handy for picking the right input strategy):
mycomputer observe --json \
  | jq -r '[.accessibility.elements[].toolkit] | unique'
```

Find candidate accessible elements regardless of app:

```bash
mycomputer observe --json \
  | jq -r '.accessibility.elements[] | select((.actions // []) | length > 0) | [.id,.role,.name,((.actions // [])|join(","))] | @tsv'
```

Invoke an action:

```bash
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "actions": [
    {"type": "perform_action", "element_id": ":1.123|/org/app/a11y/button", "action": "click"}
  ]
}
JSON
```

Set text on an EditableText element:

```json
{
  "actions": [
    {"type": "set_text", "element_id": ":1.123|/org/app/a11y/text", "text": "new value"}
  ]
}
```

## Dark-Theme Apps (OCR Preprocessing)

Tesseract trained data assumes printed-page contrast (dark text on light paper). White-on-dark UIs — gnome-calculator's display, gnome-terminal, dark-mode Electron, IDE side panels — feed Tesseract an inverted signal and it returns zero candidates without help.

`find-text` defaults to `--preprocess=auto`: it samples the requested region's mean luminance and, when the region is below `darkThemeLuminanceThreshold` (= 80/255 ≈ 0.314), inverts the pixels before invoking Tesseract. Light regions pass through unchanged, so this is bit-for-bit identical for anything that already looks like a printed page.

Each candidate's `extra.preprocess` reports what fired (`"none"`, `"invert"`, or `"binarize"`) so callers can see when the auto-invert helped.

```bash
# Read the calculator result display (white "40" on #1f1f1f).
mycomputer find-text --region 0,150,400,100 --json '40' \
  | jq '.candidates[0] | {text:.extra.text, conf:.confidence, preprocess:.extra.preprocess}'
# => { "text": "40", "conf": 0.95, "preprocess": "invert" }
```

When you already know a UI is dark, skip the luminance probe by forcing `--preprocess=invert`. It's faster on big regions and removes one decision branch from your reasoning:

```bash
mycomputer find-text --preprocess invert --region 0,150,400,100 '40'
```

For low-contrast or noisy backgrounds (anti-aliased gradients, gradient toolbars), try Otsu binarization:

```bash
mycomputer find-text --preprocess binarize --region 0,0,800,200 'Save'
```

PSM / OEM tuning is also wired through. Single-line displays often read better with `--psm 7` (treat the image as a single text line), and you can pin the LSTM engine with `--oem 1`:

```bash
mycomputer find-text --psm 7 --oem 1 --region 0,150,400,100 '40' --json
```

The same fields are exposed on the MCP `find_text` tool and the `find_text` / `click_text` action types — defaults are the same (`preprocess: auto`, PSM/OEM omitted unless non-zero).

The doctor row for `ocr_tesseract` now reports tesseract `version` and the `supported_psm` / `supported_oem` ranges so agents can validate ahead of time:

```bash
mycomputer doctor --json | jq '.backends[] | select(.name=="ocr_tesseract") | .details'
```

## Browser Pipeline

Use CDP for browser-only tasks:

```bash
cat <<'JSON' | mycomputer browse --input-file - --json
{
  "headless": true,
  "steps": [
    {"action": "navigate", "url": "https://duckduckgo.com/?q=linux+accessibility"},
    {"action": "wait", "duration_ms": 2000},
    {"action": "screenshot", "path": "/tmp/mycomputer-search.png", "full_page": false},
    {"action": "get_title"},
    {"action": "get_dom_text"}
  ]
}
JSON
```

Use browser pipelines for selectors and DOM text. Use desktop screenshots when the task needs the user's actual visible browser window.

## Common Recovery

- If `focus_window` succeeds but visual state is wrong, click inside the target window and verify with a screenshot.
- If typed text does not appear, click the intended editor/input area, press `escape` to dismiss hints, and retry.
- If an app exposes no useful AT-SPI nodes, fall back to screenshot coordinates with clear verification.
- If `doctor` reports Wayland without X11, stop; native Wayland control is outside the MVP.
