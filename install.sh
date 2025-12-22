#!/usr/bin/env bash
set -euo pipefail

REPO="${ATLAS_REPO:-MrTeeett/Atlas}"
VERSION="${ATLAS_VERSION:-}"
METHOD="${ATLAS_METHOD:-auto}" # auto|appimage|deb|rpm|tar
VERIFY_SHA="${ATLAS_VERIFY_SHA:-1}"
SETUP="${ATLAS_SETUP:-1}"
FRESH="${ATLAS_FRESH:-0}"
LISTEN="${ATLAS_LISTEN:-}"
BASE_PATH="${ATLAS_BASE_PATH:-}"
ADMIN_USER="${ATLAS_ADMIN_USER:-admin}"
ADMIN_PASS="${ATLAS_ADMIN_PASS:-}"
PORT_OVERRIDE="${ATLAS_PORT:-}"

usage() {
  cat <<'EOF'
install.sh [options]

Options:
  --repo owner/name       GitHub repo (default: MrTeeett/Atlas)
  --version <tag>         Install a specific tag (default: latest)
  --method auto|appimage|deb|rpm|tar
  --no-setup              Don't create config/users, only install the binary
  --fresh                 Remove existing config/DB and reinitialize
  --listen <addr>         Listen address, e.g. 0.0.0.0:8080
  --port <port>           Convenience for --listen 0.0.0.0:<port>
  --base-path </path>     URL base path, e.g. /abc123 or /
  --admin-user <name>     Admin username (default: admin)
  --admin-pass <pass>     Admin password (if not set, a random one is offered)
  --no-verify             Skip SHA256 verification
  -h, --help              Show help

Examples:
  ./install.sh
  ./install.sh --version v1.2.3
  ./install.sh --method deb
  ./install.sh --fresh
  ./install.sh --port 8080 --base-path /atlas
  ATLAS_REPO=you/atlas ./install.sh
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2;;
    --version) VERSION="${2:-}"; shift 2;;
    --method) METHOD="${2:-}"; shift 2;;
    --no-setup) SETUP="0"; shift 1;;
    --fresh) FRESH="1"; shift 1;;
    --listen) LISTEN="${2:-}"; shift 2;;
    --port) PORT_OVERRIDE="${2:-}"; shift 2;;
    --base-path) BASE_PATH="${2:-}"; shift 2;;
    --admin-user) ADMIN_USER="${2:-}"; shift 2;;
    --admin-pass) ADMIN_PASS="${2:-}"; shift 2;;
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

# When this script is executed via a pipe (curl | bash), stdin isn't a TTY.
# Read prompts from /dev/tty if available so interactive setup still works.
TTY=""
if [[ -e /dev/tty && -r /dev/tty && -w /dev/tty ]]; then
  TTY="/dev/tty"
fi

is_tty() { [[ -n "${TTY}" ]]; }

tty_print() {
  if is_tty; then
    printf "%s" "$*" >"${TTY}"
  else
    printf "%s" "$*"
  fi
}

tty_read_line() {
  local prompt="$1"
  local out=""
  if is_tty; then
    tty_print "${prompt}"
    IFS= read -r out <"${TTY}" || out=""
  else
    IFS= read -r -p "${prompt}" out || out=""
  fi
  printf "%s" "${out}"
}

tty_read_secret() {
  local prompt="$1"
  local out=""
  if is_tty; then
    tty_print "${prompt}"
    IFS= read -r -s out <"${TTY}" || out=""
    tty_print $'\n'
  else
    IFS= read -r -s -p "${prompt}" out || out=""
    printf "\n"
  fi
  printf "%s" "${out}"
}

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
      # Prefer system packages on servers (no FUSE dependency like AppImage).
      if is_root; then
        if [[ "$PM" == "apt" ]]; then echo "deb"; return; fi
        if [[ "$PM" == "dnf" ]]; then echo "rpm"; return; fi
        echo "tar"; return
      fi
      if [[ "$ARCH" == "amd64" ]]; then echo "appimage"; return; fi
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

install_data_dir() {
  if is_root; then
    echo "/var/lib/atlas"
  else
    echo "${HOME}/.local/share/atlas"
  fi
}

install_config_path() {
  if is_root; then
    echo "/etc/atlas/atlas.json"
  else
    echo "$(install_data_dir)/atlas.json"
  fi
}

service_available() {
  command -v systemctl >/dev/null 2>&1 || return 1
  [[ -f /etc/systemd/system/atlas.service || -f /lib/systemd/system/atlas.service ]] || return 1
  return 0
}

start_service() {
  if ! is_root; then
    return 0
  fi
  if ! service_available; then
    return 0
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl restart atlas >/dev/null 2>&1 || systemctl start atlas >/dev/null 2>&1 || true
}

rand_hex() {
  local nbytes="${1:-12}"
  od -An -N "${nbytes}" -tx1 </dev/urandom | tr -d ' \n'
}

rand_u32() {
  od -An -N 4 -tu4 </dev/urandom | tr -d ' \n'
}

rand_port() {
  local min="${1:-20000}"
  local max="${2:-60000}"
  local span=$((max - min + 1))
  local n
  n="$(rand_u32)"
  echo $((min + (n % span)))
}

rand_password() {
  LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 24 2>/dev/null || true
  echo
}

prompt_yes_no() {
  local question="$1"
  local default="${2:-Y}" # Y or N
  local reply=""

  if ! is_tty; then
    [[ "${default}" == "Y" ]] && return 0 || return 1
  fi

  while true; do
    reply="$(tty_read_line "${question} ")" || reply=""
    reply="$(echo "${reply}" | tr '[:upper:]' '[:lower:]' | xargs)"
    if [[ -z "${reply}" ]]; then
      reply="$(echo "${default}" | tr '[:upper:]' '[:lower:]')"
    fi
    case "${reply}" in
      y|yes) return 0 ;;
      n|no) return 1 ;;
      *) echo "Please answer y/n." ;;
    esac
  done
}

normalize_base_path() {
  local p="$1"
  p="$(echo "${p}" | xargs)"
  if [[ -z "${p}" ]]; then
    echo "/"
    return 0
  fi
  if [[ "${p}" != /* ]]; then
    p="/${p}"
  fi
  p="${p%/}"
  [[ -z "${p}" ]] && p="/"
  echo "${p}"
}

prompt_port() {
  local suggested="$1"
  local port="${suggested}"
  if ! is_tty; then
    echo "${port}"
    return 0
  fi
  if ! prompt_yes_no "Set port manually (instead of random ${suggested})? [y/N]" "N"; then
    echo "${port}"
    return 0
  fi
  while true; do
    port="$(tty_read_line "Enter port (1-65535): ")" || port=""
    port="$(echo "${port}" | tr -d ' ' )"
    if [[ "${port}" =~ ^[0-9]+$ ]] && (( port >= 1 && port <= 65535 )); then
      echo "${port}"
      return 0
    fi
    echo "Bad port: ${port}"
  done
}

prompt_base_path() {
  local suggested="$1"
  local p="${suggested}"
  if ! is_tty; then
    echo "${p}"
    return 0
  fi
  if ! prompt_yes_no "Set base path manually (instead of random ${suggested})? [y/N]" "N"; then
    echo "${p}"
    return 0
  fi
  while true; do
    p="$(tty_read_line "Enter base path (e.g. /abc123, or / for root): ")" || p=""
    p="$(normalize_base_path "${p}")"
    if [[ "${p}" == "/" ]] || [[ "${p}" =~ ^/[A-Za-z0-9._-]+$ ]]; then
      echo "${p}"
      return 0
    fi
    echo "Bad base path: ${p}"
  done
}

prompt_password() {
  local suggested="$1"
  local p="${suggested}"
  if ! is_tty; then
    echo "${p}"
    return 0
  fi
  if ! prompt_yes_no "Set admin password manually (instead of random)? [y/N]" "N"; then
    echo "${p}"
    return 0
  fi
  while true; do
    p="$(tty_read_secret "Enter admin password (min 8 chars): ")" || p=""
    if (( ${#p} >= 8 )); then
      echo "${p}"
      return 0
    fi
    echo "Password too short."
  done
}

write_config() {
  local cfg="$1"
  local listen="$2"
  local base_path="$3"
  local data_dir="$4"
  mkdir -p "$(dirname "$cfg")" "$data_dir"
  base_path="$(normalize_base_path "$base_path")"
  cat >"$cfg" <<EOF
{
  "listen": "${listen}",
  "root": "/",
  "base_path": "${base_path}",
  "cookie_secure": false,
  "enable_exec": true,
  "enable_firewall": true,
  "enable_admin_actions": true,
  "service_name": "atlas.service",
  "daemonize": true,
  "log_level": "info",
  "log_file": "${data_dir}/atlas.log",
  "log_stdout": false,
  "fs_sudo": true,
  "fs_users": ["*"],
  "master_key_file": "${data_dir}/atlas.master.key",
  "user_db_path": "${data_dir}/atlas.users.db",
  "firewall_db_path": "${data_dir}/atlas.firewall.db"
}
EOF
  chmod 0600 "$cfg" 2>/dev/null || true
}

print_login() {
  local cfg="$1"
  local user="$2"
  local pass="$3"
  local listen base_path tls_cert tls_key scheme url port host
  listen="$(sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$cfg" | head -n 1)"
  base_path="$(sed -n 's/.*"base_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$cfg" | head -n 1)"
  tls_cert="$(sed -n 's/.*"tls_cert_file"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$cfg" | head -n 1)"
  tls_key="$(sed -n 's/.*"tls_key_file"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$cfg" | head -n 1)"

  scheme="http"
  if [[ -n "${tls_cert}" && -n "${tls_key}" ]]; then scheme="https"; fi

  port=""
  if [[ "${listen}" == *:* ]]; then port="${listen##*:}"; fi
  host="${listen%:*}"
  if [[ "${host}" == "${listen}" ]]; then host=""; fi

  base_path="$(normalize_base_path "${base_path:-/}")"
  base_path="${base_path%/}"
  if [[ "${base_path}" == "/" ]]; then base_path=""; fi

  url="${scheme}://localhost"
  if [[ -n "${port}" ]]; then url="${url}:${port}"; fi
  url="${url}${base_path}/login"

  remote_url=""
  if [[ "${host}" == "0.0.0.0" ]]; then
    ip="$(guess_primary_ip || true)"
    if [[ -z "${ip}" ]]; then ip="<server-ip>"; fi
    remote_url="${scheme}://${ip}"
    if [[ -n "${port}" ]]; then remote_url="${remote_url}:${port}"; fi
    remote_url="${remote_url}${base_path}/login"
  elif [[ -n "${host}" && "${host}" != "127.0.0.1" && "${host}" != "localhost" ]]; then
    remote_url="${scheme}://${host}"
    if [[ -n "${port}" ]]; then remote_url="${remote_url}:${port}"; fi
    remote_url="${remote_url}${base_path}/login"
  fi

  cat <<EOF

Login:
  url:  ${url}
EOF
  if [[ -n "${remote_url}" ]]; then
    cat <<EOF
  remote_url: ${remote_url}
EOF
  fi
  cat <<EOF
  user: ${user}
  pass: ${pass}

EOF
}

guess_primary_ip() {
  if command -v hostname >/dev/null 2>&1; then
    # hostname -I may print multiple; pick the first.
    ip="$(hostname -I 2>/dev/null | awk '{print $1}' | tr -d ' \n' || true)"
    if [[ -n "${ip}" ]]; then
      echo "${ip}"
      return 0
    fi
  fi
  if command -v ip >/dev/null 2>&1; then
    ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' | tr -d ' \n' || true)"
    if [[ -n "${ip}" ]]; then
      echo "${ip}"
      return 0
    fi
  fi
  return 1
}

case "$METH" in
  appimage)
    BIN_DIR="$(install_bin_dir)"
    mkdir -p "$BIN_DIR"
    APPDIR="$(is_root && echo /usr/local/lib/atlas || echo "${HOME}/.local/share/atlas")"
    mkdir -p "$APPDIR"
    install -m 0755 "${TMPDIR}/${ASSET}" "${APPDIR}/${ASSET}"
    cat > "${BIN_DIR}/atlas" <<EOF
#!/usr/bin/env sh
set -eu
APPIMAGE="${APPDIR}/${ASSET}"

# Avoid requiring FUSE on servers: use extract-and-run if supported.
if "\$APPIMAGE" --appimage-extract-and-run --help >/dev/null 2>&1; then
  exec "\$APPIMAGE" --appimage-extract-and-run "\$@"
fi
exec "\$APPIMAGE" "\$@"
EOF
    chmod 0755 "${BIN_DIR}/atlas"
    ATLAS_BIN="${BIN_DIR}/atlas"
    echo "Installed: ${BIN_DIR}/atlas (AppImage wrapper)"
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
    ATLAS_BIN="$(command -v atlas || true)"
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
    ATLAS_BIN="$(command -v atlas || true)"
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
      ATLAS_BIN="/usr/local/bin/atlas"
      echo "Installed: /usr/local/bin/atlas"
    else
      BIN_DIR="$(install_bin_dir)"
      mkdir -p "$BIN_DIR"
      install -m 0755 "${TMPDIR}/atlas" "${BIN_DIR}/atlas"
      ATLAS_BIN="${BIN_DIR}/atlas"
      echo "Installed: ${BIN_DIR}/atlas"
    fi
    ;;
esac

if [[ "${SETUP}" != "1" ]]; then
  exit 0
fi

CFG="$(install_config_path)"
DATA_DIR="$(install_data_dir)"

if [[ "${FRESH}" == "1" ]]; then
  rm -f "${CFG}" \
        "${DATA_DIR}/atlas.master.key" \
        "${DATA_DIR}/atlas.users.db" \
        "${DATA_DIR}/atlas.firewall.db"
fi

# Decide whether to configure now. For package installs, a default config may already exist.
do_setup=0
if [[ "${FRESH}" == "1" ]] || [[ ! -f "${CFG}" ]]; then
  do_setup=1
else
  # If it looks like a default config (8080 + "/" base_path), offer to customize it.
  cur_listen="$(sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${CFG}" | head -n 1)"
  cur_base="$(sed -n 's/.*"base_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${CFG}" | head -n 1)"
  if [[ "${cur_listen}" == "127.0.0.1:8080" && "${cur_base}" == "/" ]]; then
    if prompt_yes_no "Configure Atlas now (port/base_path/admin password)? [Y/n]" "Y"; then
      do_setup=1
    fi
  else
    if prompt_yes_no "Update admin credentials now? [y/N]" "N"; then
      do_setup=1
    fi
  fi
fi

if [[ "${do_setup}" -ne 1 ]]; then
  exit 0
fi

if [[ -z "${LISTEN}" ]]; then
  if [[ -n "${PORT_OVERRIDE}" ]]; then
    PORT="$(echo "${PORT_OVERRIDE}" | tr -d ' ')"
  else
    PORT="$(prompt_port "$(rand_port 20000 60000)")"
  fi
  # Bind to all interfaces so you can access the UI remotely.
  # Recommended: put Atlas behind TLS (reverse proxy) and restrict access by firewall.
  LISTEN="0.0.0.0:${PORT}"
fi
if [[ -z "${BASE_PATH}" ]]; then
  BASE_PATH="$(prompt_base_path "/$(rand_hex 12)")"
fi
if [[ -z "${ADMIN_PASS}" ]]; then
  ADMIN_PASS="$(prompt_password "$(rand_password)")"
fi

write_config "${CFG}" "${LISTEN}" "${BASE_PATH}" "${DATA_DIR}"

# Create/update the admin user with full privileges.
if [[ -z "${ATLAS_BIN:-}" ]]; then
  ATLAS_BIN="$(command -v atlas || true)"
fi
if [[ -z "${ATLAS_BIN:-}" ]]; then
  echo "atlas binary not found in PATH after install; try re-login or use an absolute path." >&2
  exit 1
fi

"${ATLAS_BIN}" -config "${CFG}" user add \
  -user "${ADMIN_USER}" \
  -pass "${ADMIN_PASS}" \
  -role admin \
  -exec=true \
  -procs=true \
  -fw=true \
  -fs-sudo=true \
  -fs-users='*' >/dev/null

echo "Configured: ${CFG}"
print_login "${CFG}" "${ADMIN_USER}" "${ADMIN_PASS}"

start_service
if service_available; then
  echo "Service: started (atlas)"
fi
