#!/usr/bin/env sh
set -eu

# Platform-boundary import guard: X11/XInput/D-Bus implementation imports must
# stay inside the X11 adapter or the legacy leaf packages it wraps.
patterns='github.com/1broseidon/mc/internal/x11|github.com/1broseidon/mc/internal/yield|github.com/jezek/xgb|github.com/godbus/dbus'
violations=$(find . -name '*.go' \
  ! -path './internal/platform/x11adapter/*' \
  ! -path './internal/x11/*' \
  ! -path './internal/yield/*' \
  ! -path './vendor/*' \
  -print | xargs grep -nE "^[[:space:]]*\"($patterns)" || true)

if [ -n "$violations" ]; then
  echo "platform-boundary import violations:" >&2
  echo "$violations" >&2
  exit 1
fi
