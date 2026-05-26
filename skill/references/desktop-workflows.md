# Desktop Workflows

Use these patterns when a full Linux desktop matters, especially for native applications and visual QA.

## Native App QA Smoke

Goal: verify a native GTK, Qt, Electron, or Java app behaves correctly through the real UI.

Pattern:

1. `doctor`
2. `windows` or launch/focus through the user's requested path.
3. `observe` for current focus and accessibility tree.
4. Perform the workflow with `computer_actions`.
5. Capture checkpoints after each state transition.
6. Report screenshots, visible state, and any accessibility mismatch.

Good targets:

- settings pages,
- modal dialogs,
- menu actions,
- disabled/enabled button states,
- keyboard focus order,
- file picker behavior,
- warnings and confirmation dialogs.

## IDE And Developer Tool Control

Goal: validate an IDE or desktop developer tool where the UI is the product.

Examples:

- VS Code extension command palette flows.
- New file/editor creation and typing.
- Side-panel tree view rendering.
- Diagnostics, decorations, or status-bar items.
- DBeaver, GitKraken, GNOME Builder, or JetBrains workflows.

Pattern:

1. Focus the IDE by exact window id when available.
2. Use keyboard shortcuts for global IDE actions.
3. Click inside the editor or panel before typing when focus is ambiguous.
4. Verify through screenshot and window title.

Recovery:

- Empty-editor hints may absorb focus. Click the editor body and press `escape`.
- IDEs may have multiple windows with the same class. Prefer exact X11 id from `windows`.

## Accessibility Audit

Goal: compare visible UI against accessible semantics.

Pattern:

1. Capture screenshot.
2. Run `observe` and inspect `accessibility.elements`.
3. Match visible controls to roles, names, bounds, interfaces, and actions.
4. Try `perform_action` on non-destructive controls.
5. Try `set_text` only on known safe EditableText fields.
6. Report missing labels, wrong roles, absent actions, and bounds mismatches.

Useful evidence:

- screenshot path,
- element id,
- role/name/actions,
- expected vs actual behavior.

## Multi-App Workflow

Goal: test workflows crossing native applications.

Examples:

- LibreOffice Writer export to PDF, then open in a PDF viewer.
- DBeaver export CSV, then open in a spreadsheet app.
- File manager drag/drop into another app.
- Image editor export, then browser upload dialog.

Pattern:

1. Use `windows` to identify each app.
2. Complete one app step and screenshot the result.
3. Switch apps by exact id or title.
4. Use screenshots after file dialogs or drag/drop.
5. Report artifact paths and window titles.

Do not automate destructive save/overwrite/send steps unless explicitly requested.

## Visible Browser Versus CDP Browser

Use CDP browser pipelines for fast, selector-based web automation. Use visible desktop browser control when:

- the user wants to see the browser change,
- testing file pickers, downloads, permission prompts, or OS dialogs,
- checking browser extensions or native integrations,
- validating the actual user's profile state.

If visible browser focus is unreliable, use `windows` to capture the current bounds, click inside the window, and verify with a screenshot before typing.
