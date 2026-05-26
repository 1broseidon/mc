#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--help" ]]; then
  cat <<'EOF'
Usage: smoke.sh [--no-browser]

Runs a non-destructive MyComputer CLI smoke test:
  - doctor
  - windows
  - observe
  - screenshot capture
  - optional headless browser pipeline

Set MYCOMPUTER_BIN=/path/to/mycomputer to test a specific binary.
EOF
  exit 0
fi

run_browser=1
if [[ "${1:-}" == "--no-browser" ]]; then
  run_browser=0
fi

bin="${MYCOMPUTER_BIN:-mycomputer}"
if ! command -v "$bin" >/dev/null 2>&1; then
  if [[ -x ./bin/mycomputer ]]; then
    bin="./bin/mycomputer"
  else
    echo "mycomputer binary not found; set MYCOMPUTER_BIN" >&2
    exit 127
  fi
fi

tmp="${TMPDIR:-/tmp}/mycomputer-skill-smoke"
mkdir -p "$tmp"

echo "== doctor =="
"$bin" doctor --json

echo "== windows =="
"$bin" windows --json >/tmp/mycomputer-skill-windows.json
python3 - <<'PY'
import json
with open("/tmp/mycomputer-skill-windows.json", "r", encoding="utf-8") as f:
    data = json.load(f)
print({"windows": len(data.get("windows", []))})
PY

echo "== observe =="
"$bin" observe --json >/tmp/mycomputer-skill-observe.json
python3 - <<'PY'
import json
with open("/tmp/mycomputer-skill-observe.json", "r", encoding="utf-8") as f:
    data = json.load(f)
print({
    "screen": data.get("screen", {}).get("bounds"),
    "windows": len(data.get("windows", [])),
    "a11y": (data.get("accessibility") or {}).get("status"),
})
PY

echo "== capture =="
"$bin" capture --out "$tmp/desktop.png" --max-edge 900 --json
test -s "$tmp/desktop.png"

if [[ "$run_browser" == "1" ]]; then
  echo "== browser =="
  cat >"$tmp/browser.json" <<'JSON'
{
  "headless": true,
  "steps": [
    {"action": "navigate", "url": "data:text/html,<title>mycomputer smoke</title><h1>ok</h1>"},
    {"action": "wait_for_selector", "selector": "h1"},
    {"action": "get_title"}
  ]
}
JSON
  "$bin" browse --input-file "$tmp/browser.json" --json
fi

echo "smoke ok"
