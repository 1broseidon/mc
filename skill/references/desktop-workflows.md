# Desktop Workflows

Real-app patterns where a full Linux desktop matters. Each section names
the apps the pattern was designed for, so an agent can recognize which
recipe fits the user's request.

## Drive a Gio / Dear ImGui / Flutter-Linux / egui app

Goal: control a desktop app that exposes NO AT-SPI tree (immediate-mode
renderers by design — Gio, ImGui, Flutter-Linux, raylib, Slint, egui).

Examples on a typical Linux dev machine:

- Familiar (Go Gio chat client)
- gst-launch GUI front-ends
- Dear ImGui developer tools (e.g., RenderDoc, Tracy)
- Flutter-Linux apps (Reactor, etc.)

Pattern:

1. `windows` → find the app by class.
2. `focus_window` by id (most stable on tiling WMs).
3. `find_text` / `find_color` to locate UI elements — these apps defeat
   AT-SPI by definition, so OCR + color blob are your only targeting
   options.
4. Use **`click_text`** or **`click_image`** sugar to compose
   find + click in one batch step.
5. Verify with `wait_for_pixel_change(mode:"stable")` to detect UI
   settling.
6. Screenshot the result region for the report.

Killer combo when OCR fails (dark theme, anti-aliased font, sparse
layout): use `find_color` for distinctive UI elements (orange "=" button
in gnome-calculator, colored project dots in Familiar's left rail,
status indicators in dev tools).

Recovery — WM bounds vs rendered surface: immediate-mode toolkits often
don't react to `ConfigureNotify`, so a `window-maximize` / `window-resize`
"succeeds" while the rendered canvas stays at the previous size. The
WM-reported `client_bounds` then contains exposed desktop instead of
app content, and coordinate-based clicks at those bounds miss.
MyComputer detects this with `WINDOW_GEOMETRY_DIVERGED`:

- After any `window-move` / `window-resize` / `window-maximize`, check
  `details.warning.code` (or `result.warning.code` on the verb shape)
  for `WINDOW_GEOMETRY_DIVERGED`. The `details.rendered_bounds_estimate`
  field carries an approximate inner rectangle where rendered content
  stops.
- When the warning fires, abandon WM-coordinate targeting and switch
  to `find_color` / `find_text` against a fresh screenshot of
  `client_bounds`. Those find results return absolute screen
  coordinates that match the rendered surface, not the WM frame.
- `mycomputer windows --detect-rendered --json` adds an opt-in
  `rendered_bounds_estimate` field per window so the agent can spot the
  divergence without first running a window verb. The extra XGetImage
  per window is off by default — only enable when sampling is needed.

```jsonc
{
  "schema_version": "0.2",
  "actions": [
    {"type": "focus_window", "target": {"class": "gnome-calculator"}},
    {"type": "find_color", "color": "#ff7800",
     "region": {"x":0,"y":400,"width":410,"height":100},
     "tolerance": 30, "as": "equals_button"},
    {"type": "click", "target_slot": "equals_button"},
    {"type": "wait_for_pixel_change",
     "region": {"x":0,"y":150,"width":400,"height":100},
     "mode": "stable", "stable_ms": 300, "timeout_ms": 1500}
  ]
}
```

#### `find_text` → `find_color` recovery

Codex's v0.3 dogfood found that even with auto-invert preprocessing,
OCR can miss button glyphs in immediate-mode UIs. v0.3.1 auto-retries
with `psm=11` (sparse text) on tight regions (< 100,000 px²) — but
when both passes strike out, switch targeting modes entirely.

Pattern:

1. `find_text` for the label first — cheap when it works, and the
   auto-retry already covers the easy recovery wins.
2. On zero candidates (or low confidence), `find_color` on a hex that
   uniquely identifies the surrounding control.
3. `click` the located region.

```jsonc
// Goal: press gnome-calculator's "=" button. Try the OCR'd label first;
// fall back to the orange button fill if OCR misses.
{
  "schema_version": "0.2",
  "actions": [
    {"type": "find_text", "query": "=",
     "region": {"x":0,"y":400,"width":410,"height":100},
     "min_confidence": 0.6, "as": "equals_text"},
    // Always provide the find_color slot too — the click step uses
    // whichever slot resolved (equals_text on OCR hit, equals_button
    // when OCR misses and the smart-PSM retry still came back empty).
    {"type": "find_color", "color": "#ff7800",
     "region": {"x":0,"y":400,"width":410,"height":100},
     "tolerance": 30, "as": "equals_button"},
    {"type": "click", "target_slot": "equals_button"}
  ]
}
```

Other ready-made color targets for this pattern:

- gnome-calculator `=` button — `#ff7800` (orange accent).
- Familiar project dots — `#a78bfa` (purple), `#34d399` (green).
- VS Code activity-bar badge — `#0078d4` (focus blue).
- GNOME Terminal cursor — terminal foreground at known cell coordinates.

A returned OCR candidate carrying `extra.psm_retried=true` /
`extra.psm_used=11` is a strong signal that the region is borderline
for OCR; make `find_color` the default for this UI going forward
rather than relying on the retry to keep saving the call.

## Native GTK/Qt app QA

Goal: verify a native GTK, Qt, Electron, or Java Swing app behaves
correctly through the real UI.

Examples: GIMP, Inkscape, gnome-control-center, KDE System Settings,
DBeaver, JetBrains IDEs, KeePassXC, Obsidian (Electron), OBS Studio,
Audacity.

Pattern:

1. `doctor` — confirm `at_spi` ready (most GTK/Qt apps expose it).
2. `windows` or launch the app through the user's preferred path
   (terminal, dock, .desktop file).
3. `observe` and filter to the target app via `app` field:
   ```bash
   mycomputer observe --json \
     | jq '.accessibility.elements[] | select(.app == "GIMP")'
   ```
4. Use `perform_action` on AT-SPI ids for stable targeting, falling
   back to `find_text` for menus / icons that don't expose AT-SPI roles.
5. Verify with `wait_for_text` (e.g., wait until the status bar reports
   "Saved") or `wait_for_window` (modal opened/closed).
6. Screenshot the result for the report.

Good targets for this pattern:

- Settings pages.
- Modal dialogs (open/close detected via `wait_for_window`).
- Menu actions (`perform_action`).
- Disabled/enabled button states (check `actions` array).
- Keyboard focus order.
- File picker behavior.
- Warnings and confirmation dialogs.

## IDE / developer tool control

Goal: validate an IDE or desktop developer tool where the UI is the
product.

Examples: VS Code (extension command palette flows, side-panel tree
view, diagnostics decorations), JetBrains IDEs (IntelliJ, GoLand,
PyCharm), GNOME Builder, KDevelop, Qt Creator, Emacs/Vim in GUI mode.

Pattern:

1. Focus the IDE by exact window id (multiple windows often share a
   class).
2. Use keyboard shortcuts for global actions (`ctrl+shift+p` palette,
   `ctrl+n` new file, etc.). Most stable across releases.
3. Click inside the editor body before typing if focus is ambiguous.
4. Verify via screenshot of the affected region + window title.

Recovery:

- Empty-editor hints (the "Untitled-1" placeholder) may absorb focus.
  Click the editor body and press `escape`.
- IDEs may have multiple windows with the same class. Prefer exact X11
  id from `windows`.
- VS Code's command palette is a Chromium DOM — sometimes
  `browser_pipeline` against the renderer port is more stable than
  AT-SPI. Check `--remote-debugging-port` flags.

## UI assessment / visual review

Goal: produce a written design review of a running desktop app.

Pattern (this is the workflow used to review Familiar's UI):

1. `windows` to find the app + grab `client_bounds` (excludes WM
   decorations).
2. `focus_window` by id.
3. `wait_for_pixel_change(mode:"stable")` to make sure the UI has
   settled (any in-flight animation completed).
4. `screenshot` the `client_bounds` region — gives a decoration-free
   capture for review.
5. Optionally drive the UI through known states (focus composer, open
   modal, etc.) and screenshot each.
6. Write up: layout, hierarchy, typography, color, affordances,
   accessibility (AT-SPI coverage check via `observe`), friction
   points, mitigations.

Useful one-liners:

```bash
# Is this app exposing AT-SPI? (Gio/ImGui apps: no)
WIN=$(mycomputer windows --json | jq -r '.windows[] | select(.class=="fam-ui") | .id')
mycomputer observe --json | jq --arg id "$WIN" \
  '[.accessibility.elements[] | select(.window_id == $id)] | length'
```

## Accessibility audit

Goal: compare visible UI against accessible semantics. Catch missing
labels, wrong roles, absent actions, bounds mismatches.

Pattern:

1. Capture screenshot of the app.
2. Run `observe` and inspect `accessibility.elements` filtered to the
   app.
3. For each visible control in the screenshot:
   - Look up the matching AT-SPI element by bounds containment.
   - Compare role + name + actions vs what a screen reader would
     announce.
4. Try `perform_action` on non-destructive controls to verify they
   actually do what their role claims.
5. Try `set_text` only on known safe EditableText fields.
6. Report missing labels, wrong roles, absent actions, bounds
   mismatches.

Useful evidence per finding:

- Screenshot path with the control circled (manual annotation OK).
- Element id (`<bus>|<path>`).
- Role / name / actions array.
- Expected vs actual behavior (e.g., "button" role but no
  `default.activate` action).

## Multi-app workflow

Goal: test a workflow that crosses native applications.

Examples:

- LibreOffice Writer export to PDF, then open in evince or okular.
- DBeaver export CSV, then open in LibreOffice Calc.
- File manager (Nautilus, Dolphin) drag/drop into another app.
- Image editor export, then browser upload dialog.
- `ffmpeg` headless conversion, then play in `mpv` for visual smoke.

Pattern:

1. `windows` → identify each app, record their ids.
2. Complete one app step (use `focus_window` by id; don't trust class
   alone when multiple windows match).
3. `wait_for_window` to detect when a file picker, save dialog, or
   subsequent app window appears.
4. `screenshot` after each transition for the report.
5. Switch apps by exact id.

Do NOT automate destructive save/overwrite/send steps unless the user
explicitly asked.

## Visible browser vs CDP browser

Use **CDP browser pipelines** (`browser_pipeline`) for fast,
selector-based web automation when:

- The work is purely DOM (form fill, navigation, get_dom_text).
- The user doesn't need to see the work happen.
- A fresh headless context is acceptable.

Use **visible desktop browser control** (focus_window + click + type) when:

- The user wants to see the browser change in their session.
- Testing file pickers, downloads, permission prompts, or OS dialogs.
- Checking browser extensions (1Password, ad blockers) or native
  integrations.
- Validating the actual user's profile state (logged-in cookies, MFA
  tokens already on device).

If visible browser focus is unreliable, use `windows` to capture the
current bounds, click inside the window, and verify with a screenshot
before typing.

## Reproduce a flaky / visual bug

Goal: capture a UI defect that only manifests on a real desktop (not in
headless test runs).

Pattern:

1. Launch the app + drive to the suspect state.
2. Set up an action batch that triggers the bug repeatedly (e.g., open
   the modal N times), screenshotting each iteration.
3. Compare screenshots manually or with `diff` / image-diff tooling.
4. Capture the audit log lines for the batch — they record exact
   timings and target resolutions.
5. Replay the batch via `mycomputer audit replay <batch_id>` for
   reproducibility (requires `--audit-full-payloads` to have been set).

This is genuinely impossible without a real desktop session — the
killer use case for MyComputer over headless tooling.

## Tutorial / GIF / docs capture

Goal: produce a sequence of screenshots illustrating a workflow.

Pattern:

1. Pre-arrange the windows you need (`window_move`, `window_resize`).
2. Build an action batch that walks the workflow.
3. After each meaningful state change, screenshot with descriptive
   `out` paths (`step-01-open.png`, `step-02-typed.png`, …).
4. Use `wait_for_pixel_change(mode:"stable")` between steps so the
   screenshots catch settled state, not mid-animation.
5. Optionally chain the PNGs into a GIF with `convert` or `ffmpeg`
   outside MyComputer.

## Safe target check list

Before any destructive action (window_close, type into a real app,
paste over user content, click a "Submit" button), confirm:

- The user explicitly asked for the action.
- `--respect-user` is on (default true on interactive sessions).
- `--dry-run` previewed the resolved coordinates and routing.
- The target window is what you think it is (verify via `windows`
  + `focused_window`, not just class match).
- The audit log captures the action with `batch_id` reported back to
  the user.

When in doubt, screenshot first, dry-run second, ask third, and
execute last.
