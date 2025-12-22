#!/usr/bin/env bash
set -euo pipefail

REPO="${ATLAS_REPO:-MrTeeett/atlas}"
VERSION="${ATLAS_VERSION:-}"
METHOD="${ATLAS_METHOD:-auto}" # auto|appimage|deb|rpm|tar
VERIFY_SHA="${ATLAS_VERIFY_SHA:-1}"

usage() {
  cat <<'EOF'
install.sh [options]

Options:
  --repo owner/name       GitHub repo (default: MrTeeett/atlas)
  --version vX.Y.Z        Install a specific tag (default: latest)
  --method auto|appimage|deb|rpm|tar
  --no-verify             Skip SHA256 verification
  -h, --help              Show help

Examples:
  ./install.sh
  ./install.sh --version v1.2.3
  ./install.sh --method deb
  ATLAS_REPO=you/atlas ./install.sh
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2;;
    --version) VERSION="${2:-}"; shift 2;;
    --method) METHOD="${2:-}"; shift 2;;
    --no-verify) VERIFY_SHA="0"; shift 1;;
    -h|--help) usage; exit 0;;
    *) echo "unknown arg: $1" >&2; usage; exit 2;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1; }

download() {
  local url="$1" out="$2"
  if need curl; then
    curl -fsSL -o "$out" "$url"
  elif need wget; then
    wget -qO "$out" "$url"
  else
    echo "need curl or wget" >&2
    exit 1
  fi
}

arch_detect() {
  local m
  m="$(uname -m)"
  case "$m" in
    x86_64) echo "amd64";;
    aarch64|arm64) echo "arm64";;
    *) echo "unsupported arch: $m" >&2; exit 1;;
  esac
}

pm_detect() {
  if need apt-get; then echo "apt"; return; fi
  if need dnf; then echo "dnf"; return; fi
  if need pacman; then echo "pacman"; return; fi
  echo "none"
}

is_root() { [[ "$(id -u)" -eq 0 ]]; }

TAG=""
if [[ -n "$VERSION" ]]; then
  TAG="$VERSION"
else
  api="https://api.github.com/repos/${REPO}/releases/latest"
  tmp="$(mktemp)"
  download "$api" "$tmp"
  TAG="$(sed -n 's/^[[:space:]]*\"tag_name\":[[:space:]]*\"\\([^\"]\\+\\)\".*/\\1/p' "$tmp" | head -n1)"
  rm -f "$tmp"
fi

if [[ -z "$TAG" ]]; then
  echo "failed to resolve release tag" >&2
  exit 1
fi

ARCH="$(arch_detect)"
PM="$(pm_detect)"

appimage_asset() { echo "Atlas-${TAG}-x86_64.AppImage"; }
deb_asset() { echo "atlas_${TAG}_linux_${ARCH}.deb"; }
rpm_asset() { echo "atlas_${TAG}_linux_${ARCH}.rpm"; }
tar_asset() { echo "atlas_${TAG}_linux_${ARCH}.tar.gz"; }

pick_method() {
  local m="${METHOD}"
  case "$m" in
    auto)
      if [[ "$ARCH" == "amd64" ]]; then
        echo "appimage"
        return
      fi
      if is_root; then
        if [[ "$PM" == "apt" ]]; then echo "deb"; return; fi
        if [[ "$PM" == "dnf" ]]; then echo "rpm"; return; fi
      fi
      echo "tar"
      ;;
    appimage|deb|rpm|tar) echo "$m";;
    *) echo "bad --method: $m" >&2; exit 2;;
  esac
}

METH="$(pick_method)"

ASSET=""
case "$METH" in
  appimage)
    if [[ "$ARCH" != "amd64" ]]; then
      echo "AppImage is only published for x86_64; use --method tar (or deb/rpm on arm64)" >&2
      exit 1
    fi
    ASSET="$(appimage_asset)"
    ;;
  deb) ASSET="$(deb_asset)";;
  rpm) ASSET="$(rpm_asset)";;
  tar) ASSET="$(tar_asset)";;
esac

BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Repo:    $REPO"
echo "Tag:     $TAG"
echo "Arch:    $ARCH"
echo "Method:  $METH"
echo "Asset:   $ASSET"

download "${BASE_URL}/${ASSET}" "${TMPDIR}/${ASSET}"

if [[ "$VERIFY_SHA" == "1" ]] && need sha256sum; then
  download "${BASE_URL}/SHA256SUMS.txt" "${TMPDIR}/SHA256SUMS.txt" || true
  if [[ -s "${TMPDIR}/SHA256SUMS.txt" ]]; then
    line="$(grep -F "  ${ASSET}" "${TMPDIR}/SHA256SUMS.txt" || true)"
    if [[ -n "${line}" ]]; then
      (cd "$TMPDIR" && printf '%s\n' "${line}" | sha256sum -c -) >/dev/null 2>&1 || {
        echo "SHA256 verification failed for ${ASSET}" >&2
        exit 1
      }
      echo "SHA256:  OK"
    fi
  fi
fi

install_bin_dir() {
  if is_root; then
    echo "/usr/local/bin"
  else
    echo "${HOME}/.local/bin"
  fi
}

case "$METH" in
  appimage)
    BIN_DIR="$(install_bin_dir)"
    mkdir -p "$BIN_DIR"
    install -m 0755 "${TMPDIR}/${ASSET}" "${BIN_DIR}/atlas"
    echo "Installed: ${BIN_DIR}/atlas"
    ;;
  deb)
    if ! is_root; then
      echo "deb install requires root (run with sudo) or use --method appimage/tar" >&2
      exit 1
    fi
    if ! need apt-get; then
      echo "apt-get not found; use --method tar/appimage" >&2
      exit 1
    fi
    apt-get update -y >/dev/null
    apt-get install -y "${TMPDIR}/${ASSET}"
    echo "Installed: atlas (.deb)"
    ;;
  rpm)
    if ! is_root; then
      echo "rpm install requires root (run with sudo) or use --method appimage/tar" >&2
      exit 1
    fi
    if need dnf; then
      dnf install -y "${TMPDIR}/${ASSET}"
    elif need rpm; then
      rpm -Uvh --replacepkgs "${TMPDIR}/${ASSET}"
    else
      echo "dnf/rpm not found; use --method tar/appimage" >&2
      exit 1
    fi
    echo "Installed: atlas (.rpm)"
    ;;
  tar)
    tar -xzf "${TMPDIR}/${ASSET}" -C "${TMPDIR}"
    if [[ ! -f "${TMPDIR}/atlas" ]]; then
      echo "unexpected tarball layout (missing atlas)" >&2
      exit 1
    fi
    if is_root; then
      install -m 0755 "${TMPDIR}/atlas" /usr/local/bin/atlas
      mkdir -p /etc/atlas /var/lib/atlas
      if [[ -f "${TMPDIR}/atlas.json" ]] && [[ ! -f /etc/atlas/atlas.json ]]; then
        install -m 0640 "${TMPDIR}/atlas.json" /etc/atlas/atlas.json
      fi
      if [[ -f "${TMPDIR}/atlas.service" ]]; then
        if [[ -d /etc/systemd/system ]]; then
          install -m 0644 "${TMPDIR}/atlas.service" /etc/systemd/system/atlas.service
          systemctl daemon-reload >/dev/null 2>&1 || true
          echo "Service: systemctl enable --now atlas"
        fi
      fi
      echo "Installed: /usr/local/bin/atlas"
    else
      BIN_DIR="$(install_bin_dir)"
      mkdir -p "$BIN_DIR"
      install -m 0755 "${TMPDIR}/atlas" "${BIN_DIR}/atlas"
      echo "Installed: ${BIN_DIR}/atlas"
    fi
    ;;
esac
