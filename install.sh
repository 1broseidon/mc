#!/usr/bin/env bash
# install.sh — fetch a released mycomputer binary into a user bin directory.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/1broseidon/mc/main/install.sh | sh
#
# Environment overrides:
#   VERSION   Release tag to install (e.g. v0.3.0). Defaults to the latest GitHub release.
#   BIN_DIR   Destination directory. Defaults to "$HOME/.local/bin".

set -euo pipefail

REPO="1broseidon/mc"
BINARY="mycomputer"

log() {
  printf '%s\n' "$*" >&2
}

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

# --- preflight ---------------------------------------------------------------

os="$(uname -s)"
if [ "$os" != "Linux" ]; then
  die "unsupported OS: $os (mycomputer is Linux-only)"
fi

raw_arch="$(uname -m)"
case "$raw_arch" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "unsupported architecture: $raw_arch (supported: x86_64, aarch64)" ;;
esac

require_cmd curl
require_cmd tar
require_cmd grep
require_cmd sed
require_cmd mkdir
require_cmd chmod
require_cmd mv
require_cmd uname

if command -v sha256sum >/dev/null 2>&1; then
  sha_check() { sha256sum -c -; }
elif command -v shasum >/dev/null 2>&1; then
  sha_check() { shasum -a 256 -c -; }
else
  die "required command not found: sha256sum (or shasum)"
fi

# --- resolve version ---------------------------------------------------------

version="${VERSION:-}"
if [ -z "$version" ]; then
  log "resolving latest release from github.com/${REPO}..."
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  version="$(curl -fsSL "$api_url" \
    | grep '"tag_name"' \
    | head -n1 \
    | sed -E 's/.*"(v[^"]+)".*/\1/')"
  if [ -z "$version" ]; then
    die "could not determine latest release tag from $api_url"
  fi
fi

case "$version" in
  v*) ;;
  *)  die "VERSION must start with 'v' (got: $version)" ;;
esac

version_no_v="${version#v}"

# --- resolve bin dir ---------------------------------------------------------

bin_dir="${BIN_DIR:-$HOME/.local/bin}"
mkdir -p "$bin_dir"

# --- download + verify -------------------------------------------------------

asset="${BINARY}_${version_no_v}_linux_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${version}"
asset_url="${base_url}/${asset}"
sums_url="${base_url}/checksums.txt"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

log "downloading ${asset_url}"
if ! curl -fsSL "$asset_url" -o "${tmpdir}/${asset}"; then
  die "failed to download asset: $asset_url"
fi

log "downloading ${sums_url}"
if ! curl -fsSL "$sums_url" -o "${tmpdir}/checksums.txt"; then
  die "failed to download checksums.txt: $sums_url"
fi

log "verifying checksum"
(
  cd "$tmpdir"
  if ! grep "  ${asset}\$" checksums.txt | sha_check; then
    die "checksum mismatch for ${asset}"
  fi
) || exit 1

# --- extract + install -------------------------------------------------------

log "extracting"
tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"

if [ ! -f "${tmpdir}/${BINARY}" ]; then
  die "expected ${BINARY} not found in archive"
fi

dest="${bin_dir}/${BINARY}"
mv "${tmpdir}/${BINARY}" "$dest"
chmod +x "$dest"

# --- PATH guidance -----------------------------------------------------------

case ":$PATH:" in
  *:"$bin_dir":*)
    on_path=1
    ;;
  *)
    on_path=0
    ;;
esac

if [ "$on_path" -eq 0 ]; then
  shell_name="$(basename "${SHELL:-}")"
  log ""
  log "warning: ${bin_dir} is not on your PATH."
  case "$shell_name" in
    bash)
      log "  echo 'export PATH=\"${bin_dir}:\$PATH\"' >> ~/.bashrc && source ~/.bashrc"
      ;;
    zsh)
      log "  echo 'export PATH=\"${bin_dir}:\$PATH\"' >> ~/.zshrc && source ~/.zshrc"
      ;;
    fish)
      log "  fish_add_path ${bin_dir}"
      ;;
    *)
      log "  add ${bin_dir} to your shell's PATH"
      ;;
  esac
  log ""
fi

# --- confirm -----------------------------------------------------------------

if ! "$dest" version >/dev/null 2>&1; then
  log "warning: '${dest} version' did not exit cleanly; binary is installed but may be unreadable"
fi

printf 'installed %s %s to %s\n' "$BINARY" "$version" "$dest"
