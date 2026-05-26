# Changelog

All notable changes to MyComputer (`mycomputer`) are documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Wire-shape mutations (anything that changes the JSON payload of a CLI
command, an MCP tool, the audit record, or the contract envelope) must
either ship a `schema_version` bump or land here as a ledger entry.
See `schema_governance` in `conventions.yaml` and the matching section
in `anvil.md` for the rule.

## [Unreleased]

## [0.3.0] — 2026-05-26

The v0.3 polish release. Locks the public surface (CLI, MCP tools,
exit codes, error envelope, schema versions, doctor backends) as the
v0.3.x contract via `anvil.md`. Adds OCR robustness for dark themes,
multi-word phrase matching, dry-run completeness, window-space
screenshot regions, AT-SPI ↔ X11 correlation, doctor `session.logical_coords`,
reproducible build metadata, and a top-level README.

### Added

- `anvil.md` at the repo root — locks every public surface of
  `mycomputer` as the v0.3.x contract. (task-20)
- `docs/CHANGELOG.md` (this file) — Keep-a-Changelog ledger for
  wire-affecting changes. (task-20)
- `conventions.yaml` `schema_governance` section — the rule that
  wire changes during a release cycle require either a
  `schema_version` point-bump or an `anvil.md`/CHANGELOG ledger
  entry. (task-20)
- `find_text` multi-word phrase matching — queries like
  `"deep work"` now match adjacent OCR tokens up to a 6-token cap.
  Single-word queries unchanged. (task-15)
- `find_text` dark-mode preprocessing — auto-invert when luminance
  `< 80/255`, plus tunable `preprocess`, `psm`, `oem` knobs. Default
  behavior is bit-for-bit identical for callers that don't opt in.
  (task-8)
- Dry-run completeness — `--dry-run` previews resolved window-space
  coordinates and the `type_text` typing route without mutating the
  desktop. (task-9)
- AT-SPI ↔ X11 `window_id` correlation — `observe` emits a
  `window_id` field on accessibility elements when an AT-SPI app
  exposes a matching X11 window. Gio/ImGui apps unaffected. (task-11)
- `screenshot.region` accepts window-space and monitor-space target
  shapes (`point.space: "window"` / `"monitor"`). Existing screen-space
  calls unchanged; `coord_map` continues to reflect absolute screen
  bounds. (task-12)
- `doctor.session.logical_coords` — new boolean field populated from
  `cfg.LogicalCoords`. Returns `false` (not `null`) when disabled.
  (task-13)
- `press_key` now accepts printable single characters (`a`–`z`,
  `A`–`Z`, `0`–`9`, common punctuation) in addition to chords and
  function keys. Unreachable layout chars return
  `INPUT_LAYOUT_UNREACHABLE`. (task-13)
- `mycomputer conventions emit [--check]` — regenerates
  `conventions.yaml` from the live binary surface; `--check` exits 2
  on drift so CI can gate against surface drift. (task-16)
- Reproducible build metadata — `Makefile` injects `main.version`,
  `main.commit`, `main.built` via ldflags, honors
  `SOURCE_DATE_EPOCH` for deterministic builds. `make release`
  strips symbols/DWARF. (task-17)
- Top-level `README.md` — quick-start, install, one-page tour of the
  CLI + MCP surface. (task-18)
- Audit hook verification for `window_close` — confirmed wired
  through the existing `writeAudit` path. (task-21)
- `mcpserver.Catalog()` becomes the single source of truth for the
  registered tool list. `diagnostic.AvailableTools` is now populated
  via `Doctor(tools []string)` injection at call time so adding a
  new MCP tool surfaces in `doctor.available_tools` without a manual
  sync step. (task-22)
- `Makefile` `lint` target — hard-fail gates on `go vet`, `gofmt`,
  `staticcheck`, `deadcode`, and `golangci-lint`. Lint findings are
  now zero across the chain. (task-23, task-24)
- `.github/workflows/release.yml` + `.goreleaser.yaml` — release
  pipeline triggered on `v*` tag push. Builds linux/amd64 +
  linux/arm64, publishes the GitHub release with checksums.
  (task-26)
- `install.sh` — curl-pipe-sh installer for Linux user bin.
  Verifies sha256 checksums, defaults `BIN_DIR=~/.local/bin`,
  detects user shell for PATH guidance, shellcheck-clean.
  Environment overrides: `VERSION=v0.3.0`, `BIN_DIR=/usr/local/bin`.
  (task-27)
- `LICENSE` — MIT, 2026. (task-25)
- Top-level `README.md` in the cymbal/ketch style; quick-start
  commands verified against the live binary. (task-18, task-25)

### Changed

- `--respect-user` help text — removed the stale
  "(declaration only in v0.2; implementation lands in task-6)"
  suffix. The flag has been functional since v0.2 shipped. (task-13)
- Audit record `dry_run` field — consistent shape (omit when false,
  present as `true` when the action was dry-run). Previously some
  records emitted `dry_run: null`. (task-13)
- `--verbose` now produces observably more output on `doctor` and
  `version`; remains a no-op on commands that don't opt in by
  reading `rootOpts.Verbose`. Documented in `conventions.yaml`
  notes. (task-13)
- Public release prep: repo at `github.com/1broseidon/mc`, MIT
  license, module path renamed from `mycomputer` to
  `github.com/1broseidon/mc`. The binary (`mycomputer`), CLI verbs,
  MCP server_id (`my-computer`), wire envelopes, and exit codes are
  unchanged — the rename is repo/module only. (task-25)

### Documentation

- The Point.Target shape change from v0.2's cycle — `string` in the
  task-1 declaration → `WindowTarget` struct in the task-4
  implementation — is recorded here as **pre-release schema
  fluidity**. The interim shape never shipped to any external
  caller, so no `schema_version` bump was issued at the time. From
  v0.3.0 forward, any equivalent wire-shape mutation must carry
  either a point-version bump or an entry in this changelog.

### Schema versions

- `schema_version` remains `"0.2"`. Supported list: `["0.2", "0.1"]`.
- No new schema versions introduced in v0.3.0. The Point.Target
  reshape recorded above is a historical note, not a v0.3 change.

## [0.2.0] — 2026-05-26

Foundations release. Establishes the v0.2 wire contract:
`computer_actions` request envelope with `schema_version`, pixel
targeting (`find_text`, `find_image`, `find_color`), wait conditions
(`wait_for_window`, `wait_for_pixel_change`, `wait_for_text`),
window-local coordinates, window operations gated by `--allow-close`,
and the `respect_user` cooperative-yield model.

### Added

- `computer_actions` batched action envelope with required
  `schema_version` field. (task-1)
- `find_text`, `find_image`, `find_color` pixel-targeting primitives
  via OCR (Tesseract) and template match (gocv when CGo+OpenCV
  present, pure-Go fallback otherwise). (task-2)
- `wait_for_window`, `wait_for_pixel_change`, `wait_for_text` wait
  conditions with `present:false` (wait for absence) support.
  (task-3)
- Window-local point space (`point.space: "window"`) and window
  operations (`window_move`, `window_resize`, `window_maximize`,
  `window_minimize`, `window_raise`, `window_workspace`,
  `window_close` gated by `--allow-close`). (task-4)
- Cooperative-yield (`--respect-user`) implementation. Pauses
  action batches when real human input is detected on the active
  X11 session. (task-6)

### Notes

- During the v0.2 cycle, `Point.Target` was reshaped from the
  task-1 `string` declaration to the task-4 `WindowTarget` struct
  before any external caller depended on the interim shape. This
  is recorded under v0.3.0 → Documentation as pre-release schema
  fluidity. From v0.3.0 forward the governance rule applies; no
  retroactive `0.2.1` is issued.

## [0.1.0] — earlier

Initial wire shapes. Documented for `schema_version` accept-list
continuity (`"0.1"` payloads remain wire-compatible when the field
is present).
