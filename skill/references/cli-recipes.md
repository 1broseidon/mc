# CLI Recipes

Use the `mycomputer` CLI when MCP tools are unavailable, when debugging
the local installation, or when a repeatable shell-level smoke is useful.

## Install and detect

```bash
# Detect (CLI):
which mycomputer
mycomputer doctor --json | jq '.readiness.status'

# Install (Linux user bin, default ~/.local/bin):
curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh | sh

# Pin a version or change the install dir:
VERSION=v0.3.0 sh install.sh
BIN_DIR=/usr/local/bin sudo sh install.sh

# Go install:
go install github.com/1broseidon/mc/cmd/mycomputer@latest
```

After install, verify backends:

```bash
mycomputer doctor --json | jq '{
  schema_versions,
  session: .session,
  required:    [.backends[] | select(.required) | {name, ready}],
  optional:    [.backends[] | select(.required==false) | {name, ready, message}]
}'
```

## Readiness and inventory

```bash
mycomputer doctor --json
mycomputer config --json
mycomputer windows --json
mycomputer observe --json
mycomputer get-screen-info --json
```

Useful one-liners:

```bash
mycomputer doctor --json | jq '.readiness.status'
mycomputer windows --json | jq '.windows[] | {id,class,title,bounds,client_bounds,focused}'
mycomputer observe --json | jq '{screen:.screen.bounds, windows:(.windows|length), cursor:.cursor, a11y:.accessibility.status}'

# Per-monitor layout (index drives point.space="monitor"):
mycomputer get-screen-info --json | jq '.monitors[] | {index,name,bounds,scale,primary,refresh_hz}'

# Confirm a primary monitor is reported (always true on healthy setups):
mycomputer get-screen-info --json | jq '.monitors | length >= 1 and (map(.primary) | any)'
```

## Screenshots

Full screen, region, zoom, cursor overlay:

```bash
mycomputer capture --out /tmp/mc-full.png --json
mycomputer capture --region 100,100,900,600 --out /tmp/mc-region.png --json
mycomputer capture --zoom 600,400,300 --out /tmp/mc-zoom.png --json
mycomputer capture --cursor --format jpeg --out /tmp/mc-shot.jpg --json
```

Every screenshot response includes:

- `capture_bounds`: physical X11 region.
- `image_size`: output image size after downscale.
- `coord_map`: `capture_x,capture_y,capture_width,capture_height,image_width,image_height`.

When `image_size == capture_bounds`, no translation is needed; otherwise
map screenshot coordinates back through `coord_map` before clicking.

## Region targeting (window-space, monitor-space)

Every region-accepting tool (`capture`, `find-*`, `wait-for-pixel-change`,
`wait-for-text`) supports the extended region shape via action batch:

```jsonc
// v0.1/v0.2 — bare bounds, screen-space (still works):
{"region": {"x": 1030, "y": 201, "width": 913, "height": 976}}

// v0.3 — window-space (survives window moves):
{"region": {"x": 0, "y": 0, "width": 200, "height": 200,
            "space": "window", "target": {"class": "Code"}}}

// v0.3 — monitor-space:
{"region": {"x": 0, "y": 0, "width": 800, "height": 600,
            "space": "monitor", "monitor_index": 1}}
```

CLI `--region` accepts only screen-space `x,y,w,h`. For window/monitor
regions, use action batches.

## Pixel targeting

`find-text`, `find-image`, `find-color` for apps without AT-SPI:

```bash
# OCR text in a region:
mycomputer find-text --region 0,0,800,200 --json 'Submit'

# Multi-word phrase (up to 6 tokens):
mycomputer find-text --region 0,0,1920,1080 --json 'Save As'

# Regex query:
mycomputer find-text --regex --region 0,0,800,200 --json 'Loaded\\s+skill'

# Template match (pure-Go single-scale by default):
mycomputer find-image --region 0,0,1920,1080 --template /tmp/btn.png --threshold 0.9 --json

# Color blob (Gio/ImGui dot-style indicators):
mycomputer find-color --region 1030,201,40,400 --tolerance 30 --json "#a78bfa"

# Single-pixel sample:
mycomputer find-color --point 1057,329 --json
```

Click-by-find sugar (composes find + click on the highest-confidence
candidate):

```bash
# Inside an action batch:
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "schema_version": "0.2",
  "actions": [
    {"type": "click_text", "query": "Submit",
     "region": {"x":0,"y":0,"width":800,"height":600},
     "min_confidence": 0.6}
  ]
}
JSON
```

## Dark-theme apps (OCR preprocessing)

Tesseract trained data assumes printed-page contrast (dark text on light
paper). White-on-dark UIs — gnome-calculator's display, gnome-terminal,
dark-mode Electron, IDE side panels — feed Tesseract an inverted signal
and it returns zero candidates without help.

`find-text` defaults to `--preprocess=auto`: it samples the requested
region's mean luminance and inverts when below
`darkThemeLuminanceThreshold` (≈ 80/255). Light regions pass through
unchanged, so this is bit-for-bit identical for anything that already
looks like a printed page. Each candidate's `extra.preprocess` reports
what fired (`"none"`, `"invert"`, or `"binarize"`).

```bash
# Auto-invert:
mycomputer find-text --region 0,150,400,100 --json '40' \
  | jq '.candidates[0] | {text:.extra.text, conf:.confidence, preprocess:.extra.preprocess}'

# Force invert (skip the luminance probe; faster on big regions):
mycomputer find-text --preprocess invert --region 0,150,400,100 '40'

# Otsu binarization for low-contrast or gradient backgrounds:
mycomputer find-text --preprocess binarize --region 0,0,800,200 'Save'

# Sparse-text layouts (calculator keypad, icon grid):
mycomputer find-text --psm 11 --region 0,300,400,250 '7'
```

Probe Tesseract version + supported modes:

```bash
mycomputer doctor --json | jq '.backends[] | select(.name=="ocr_tesseract") | .details'
```

## Wait conditions

Replace fixed-duration waits with conditions:

```bash
# Wait for a window to appear (default present:true):
mycomputer wait-for-window --class gnome-calculator --timeout-ms 5000 --json

# Wait for it to disappear:
mycomputer wait-for-window --class gnome-calculator --present=false --timeout-ms 3000 --json

# Wait for a region to settle (UI animation finished):
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "schema_version": "0.2",
  "actions": [
    {"type": "wait_for_pixel_change",
     "region": {"x":0,"y":150,"width":400,"height":100},
     "mode": "stable", "stable_ms": 250, "timeout_ms": 1500}
  ]
}
JSON

# Wait for text to appear (OCR per poll — set poll_ms ≥ 250 for cost):
mycomputer wait-for-text --region 0,0,1020,200 --timeout-ms 5000 --poll-ms 300 --json 'Loaded skill'
```

Result envelopes include `polls`, `elapsed_ms`, and `last_state` so a
single response tells you why a wait timed out.

## Window operations (v0.3 EWMH verbs)

```bash
# By window id:
mycomputer window-move --id 0xa400001 --x 100 --y 50 --json
mycomputer window-resize --id 0xa400001 --width 1200 --height 900 --json
mycomputer window-raise --class Code --json
mycomputer window-minimize --class Code --json
mycomputer window-maximize --class Code --axis both --json
mycomputer window-workspace --class Code --index 1 --json

# window-close is gated by --allow-close (off by default):
mycomputer --allow-close window-close --class my-throwaway-window --json
```

Tiling WMs (i3/bspwm) may refuse `window-move` and `window-resize`. The
result still reports `ok:true` with a `WINDOW_GEOMETRY_REFUSED` warning
in `details` — agents should fall back to keyboard-based WM commands.

## Clipboard

```bash
# Read current clipboard:
mycomputer clipboard-read --json

# Write (spawns a detached owner daemon so the selection persists
# past the CLI exit):
mycomputer clipboard-write --content "hello from MyComputer" --json

# Inspect daemon status (PID file lives at $XDG_RUNTIME_DIR/mycomputer/):
mycomputer clipboard-status --json

# Paste (ctrl+v) into the currently focused window:
mycomputer paste --json

# Alternative: paste with shift+insert
mycomputer paste --method insert --json
```

Clipboard content is private. It never appears in the audit log — only
`bytes` count and `mime` type.

## type_text routing

`type_text` chooses how to put text into the focused app based on the
content. The defaults are auto:

```bash
# Auto-pick (short ASCII → xtest; long/non-ASCII/control chars → paste):
mycomputer type-text 'hello'                 # xtest
mycomputer type-text "$(< /etc/os-release)"  # paste

# Force a route:
mycomputer type-text --via xtest 'hello'
mycomputer type-text --via paste '京都 🎉'
```

When an IME is active (IBus/Fcitx5), `via:xtest` returns
`INPUT_IME_ACTIVE` — switch to `via:paste`. When the active XKB layout
can't reach a character, `INPUT_LAYOUT_UNREACHABLE` is returned.

## Action batch (modern v0.3 form)

Open VS Code, create a new file, type, and screenshot — all with
condition-based waits and window-space coordinates:

```bash
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "schema_version": "0.2",
  "actions": [
    {"type": "focus_window", "target": {"class": "Code"}},
    {"type": "wait_for_pixel_change",
     "region": {"x":0,"y":0,"width":300,"height":40,"space":"window","target":{"class":"Code"}},
     "mode": "stable", "stable_ms": 200, "timeout_ms": 1500},
    {"type": "press_key", "key": "ctrl+n"},
    {"type": "wait_for_window",
     "match": {"title_regex": "Untitled.*Visual Studio Code"},
     "timeout_ms": 3000},
    {"type": "type_text", "text": "hello from MyComputer\n", "via": "auto"},
    {"type": "screenshot",
     "screenshot": {"region": {"x":0,"y":0,"width":1200,"height":900,"space":"window","target":{"class":"Code"}},
                    "out": "/tmp/mc-vscode.png"}}
  ]
}
JSON
```

Click from a screenshot's coordinates (legacy path):

```jsonc
{
  "schema_version": "0.2",
  "actions": [
    {"type": "click",
     "point": {"x": 520, "y": 310, "space": "screenshot",
               "coord_map": "0,0,1920,1080,1568,882"}}
  ]
}
```

## AT-SPI semantic actions

Every AT-SPI element in `observe` carries `app`, `toolkit`, and
`window_id` string fields (empty when AT-SPI does not expose the value
— never `?` or null), so you can filter to a single app without
bus-name spelunking:

```bash
# All clickable elements belonging to VS Code, grouped by role:
mycomputer observe --json \
  | jq -r '.accessibility.elements[]
            | select(.app == "Visual Studio Code")
            | select((.actions // []) | length > 0)
            | [.role, .name, ((.actions // []) | join(","))] | @tsv' \
  | sort -u

# Sanity check: no elements should ever report "?" as their app:
mycomputer observe --json | jq '[.accessibility.elements[] | select(.app == "?")] | length'
# => 0

# Cross-reference: which X11 window owns this AT-SPI element?
mycomputer observe --json \
  | jq -r '.accessibility.elements[] | select(.window_id != "") | [.app, .window_id, .role, .name] | @tsv' \
  | head -10
```

Invoke an action:

```bash
cat <<'JSON' | mycomputer actions --input-file - --json
{
  "schema_version": "0.2",
  "actions": [
    {"type": "perform_action", "element_id": ":1.123|/org/app/a11y/button", "action": "click"}
  ]
}
JSON
```

Set text on an EditableText element:

```jsonc
{
  "schema_version": "0.2",
  "actions": [
    {"type": "set_text", "element_id": ":1.123|/org/app/a11y/text", "text": "new value"}
  ]
}
```

## Audit log

Every executed action lands in
`$XDG_STATE_HOME/mycomputer/audit/YYYY-MM-DD.jsonl`. Clipboard content
is never logged.

```bash
# Tail recent activity:
mycomputer audit tail --lines 20 --json

# Search:
mycomputer audit grep --query 'window_close' --json

# Replay a batch in dry-run (type-only by default; full payloads
# require --audit-full-payloads to have been set on the original run):
mycomputer audit replay <batch_id> --dry-run --json
```

Full payload manifest (privacy-preserving, opt-in):

```bash
# Run actions with full payloads saved next to the JSONL ledger:
mycomputer --audit-full-payloads actions --input-file batch.json --json

# Clipboard content is STILL redacted in the manifest. Verify:
mycomputer --audit-full-payloads clipboard-write --content "SECRET" --json
grep -rF SECRET ~/.local/state/mycomputer/audit/   # 0 matches expected
```

## Conventions drift check

`conventions.yaml` ships with the binary surface (commands, MCP tools,
doctor backends, flags). The `emit` subcommand regenerates it from the
live binary and `--check` reports drift:

```bash
# Verify no surface drift:
mycomputer conventions emit --check

# Regenerate the file in place:
mycomputer conventions emit --out conventions.yaml

# Print to stdout for inspection:
mycomputer conventions emit
```

A CI step running `make conventions-check` gates against future drift.

## Dry-run preview

```bash
# Preview a batch without mutating anything:
mycomputer actions --dry-run --input-file batch.json --json
```

Output annotates mutating actions with `dry_run: true` and surfaces:

- `details.resolved_coords` for clicks (including window-space → screen).
- `details.via` and `details.route_reason` for `type_text` (xtest vs paste decision).

Observing actions (`find_*`, `wait_for_*`, `screenshot`, `clipboard_read`)
still execute against the real desktop — they aren't mutations.

## Browser pipeline (CDP)

Use CDP for browser-only tasks:

```bash
cat <<'JSON' | mycomputer browse --input-file - --json
{
  "headless": true,
  "steps": [
    {"action": "navigate", "url": "https://duckduckgo.com/?q=linux+accessibility"},
    {"action": "wait_for_selector", "selector": "form[role=search]"},
    {"action": "screenshot", "path": "/tmp/mc-search.png", "full_page": false},
    {"action": "get_title"},
    {"action": "get_dom_text"}
  ]
}
JSON
```

Use browser pipelines for selectors and DOM text. Use desktop screenshots
when the task needs the user's actual visible browser window.

## Common recovery

- `focus_window` reports success but the screenshot shows another window
  on top → focus/raise mismatch. Recover by clicking inside the target
  window or using `window_raise` explicitly.
- `type_text` produces no characters → click the intended input area,
  press `escape` to dismiss hints, retry. If non-ASCII content,
  `via:auto` should have routed through paste — verify with
  `--dry-run`.
- `find_text` returns zero candidates → try
  `--preprocess invert`, then `--psm 6` (uniform block) or `--psm 11`
  (sparse). Confirm Tesseract sees something with
  `tesseract /tmp/region.png - --psm 6`.
- An app exposes no useful AT-SPI nodes → it's Gio/ImGui/Flutter/raylib.
  Use `find_text` / `find_image` / `find_color` exclusively.
- `doctor` reports `xdg_session_type: wayland` and `x11` blocker → stop;
  native Wayland control is out of MVP scope.
- A `wait_for_*` returns `WAIT_TIMEOUT` → inspect
  `details.last_state` in the response to see what was actually
  observed at timeout.
- `WINDOW_GEOMETRY_REFUSED` warning → tiling WM. Either accept the
  WM-driven layout or use keyboard chords to invoke WM verbs.
