#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: remote_install.sh [--ref <ref>] [--dir <install_dir>] [--fresh]

Downloads Atlas source from GitHub into a temp dir and runs deploy/install.sh.

Options:
  --ref <ref>   Git ref to download (default: main). Can be a branch, tag, or commit SHA.
  --dir <path>  Passed through to deploy/install.sh
  --fresh       Passed through to deploy/install.sh
  -h, --help    Show help
EOF
}

REF="main"
PASS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --ref)
      REF="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      PASS+=("$1")
      shift 1
      ;;
  esac
done

# If executed from a checked-out repo (not via pipe), run the local installer.
SCRIPT_SRC="${BASH_SOURCE[0]:-}"
if [[ -n "${SCRIPT_SRC}" && -f "${SCRIPT_SRC}" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${SCRIPT_SRC}")" && pwd)"
  if [[ -f "${SCRIPT_DIR}/install.sh" && -f "${SCRIPT_DIR}/../go.mod" ]]; then
    exec "${SCRIPT_DIR}/install.sh" "${PASS[@]}"
  fi
fi

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "Missing dependency: $1" >&2; exit 1; }
}

need tar
need go
need mktemp

fetch() {
  local url="$1"
  local out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
    return 0
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
    return 0
  fi
  echo "Missing dependency: curl or wget" >&2
  exit 1
}

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

archive="${tmp}/atlas.tgz"
url="https://codeload.github.com/MrTeeett/Atlas/tar.gz/${REF}"
fetch "$url" "$archive"

tar -xzf "$archive" -C "$tmp"
root="$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "${root}" || ! -f "${root}/deploy/install.sh" ]]; then
  echo "Failed to locate deploy/install.sh in downloaded archive (ref=${REF})." >&2
  exit 1
fi

exec "${root}/deploy/install.sh" "${PASS[@]}"
