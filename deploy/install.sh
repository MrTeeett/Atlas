#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: deploy/install.sh [--dir <install_dir>] [--fresh]

Builds Atlas, installs it into a directory, creates/updates an admin user with a random password,
and prints the login URL + credentials.

Options:
  --dir <path>   Install directory (default: /opt/atlas if writable, else ~/.local/share/atlas)
  --fresh        Remove existing atlas.json / atlas.master.key / atlas.users.db / atlas.firewall.db in --dir first
  -h, --help     Show help
EOF
}

INSTALL_DIR=""
FRESH=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)
      INSTALL_DIR="${2:-}"
      shift 2
      ;;
    --fresh)
      FRESH=1
      shift 1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown arg: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ ! -f "${REPO_ROOT}/go.mod" ]]; then
  echo "Error: go.mod not found at ${REPO_ROOT}; run from the repo checkout." >&2
  exit 1
fi

is_tty=0
if [[ -t 0 && -t 1 ]]; then is_tty=1; fi

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
  # 24 chars base62.
  # Ignore SIGPIPE noise from upstream when head closes the pipe early.
  LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 24 2>/dev/null || true
  echo
}

prompt_yes_no() {
  local question="$1"
  local default="${2:-Y}" # Y or N
  local reply=""

  if [[ "${is_tty}" -ne 1 ]]; then
    [[ "${default}" == "Y" ]] && return 0 || return 1
  fi

  while true; do
    read -r -p "${question} " reply || reply=""
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

prompt_port() {
  local suggested="$1"
  local port="${suggested}"
  if prompt_yes_no "Use random port ${suggested}? [Y/n]" "Y"; then
    echo "${port}"
    return 0
  fi
  if [[ "${is_tty}" -ne 1 ]]; then
    echo "${port}"
    return 0
  fi
  while true; do
    read -r -p "Enter port (1-65535): " port || port=""
    port="$(echo "${port}" | tr -d ' ' )"
    if [[ "${port}" =~ ^[0-9]+$ ]] && (( port >= 1 && port <= 65535 )); then
      echo "${port}"
      return 0
    fi
    echo "Bad port: ${port}"
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

prompt_base_path() {
  local suggested="$1"
  local p="${suggested}"
  if prompt_yes_no "Use random base path ${suggested}? [Y/n]" "Y"; then
    echo "${p}"
    return 0
  fi
  if [[ "${is_tty}" -ne 1 ]]; then
    echo "${p}"
    return 0
  fi
  while true; do
    read -r -p "Enter base path (e.g. /abc123, or / for root): " p || p=""
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
  if prompt_yes_no "Use random admin password? [Y/n]" "Y"; then
    echo "${p}"
    return 0
  fi
  if [[ "${is_tty}" -ne 1 ]]; then
    echo "${p}"
    return 0
  fi
  while true; do
    read -r -s -p "Enter admin password (min 8 chars): " p || p=""
    echo
    if (( ${#p} >= 8 )); then
      echo "${p}"
      return 0
    fi
    echo "Password too short."
  done
}

if [[ -z "${INSTALL_DIR}" ]]; then
  if mkdir -p /opt/atlas 2>/dev/null; then
    INSTALL_DIR="/opt/atlas"
  else
    INSTALL_DIR="${HOME}/.local/share/atlas"
  fi
fi

BIN_PATH="${INSTALL_DIR}/atlas"
CFG_PATH="${INSTALL_DIR}/atlas.json"

mkdir -p "${INSTALL_DIR}"
INSTALL_DIR="$(cd "${INSTALL_DIR}" && pwd)"
BIN_PATH="${INSTALL_DIR}/atlas"
CFG_PATH="${INSTALL_DIR}/atlas.json"

if [[ "${FRESH}" -eq 1 ]]; then
  rm -f "${CFG_PATH}" \
        "${INSTALL_DIR}/atlas.master.key" \
        "${INSTALL_DIR}/atlas.users.db" \
        "${INSTALL_DIR}/atlas.firewall.db"
fi

mkdir -p "${REPO_ROOT}/.gocache"
(
  cd "${REPO_ROOT}"
  # Build without CGO to avoid requiring a compiler/libc headers on minimal servers.
  GOCACHE="${REPO_ROOT}/.gocache" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${BIN_PATH}" ./cmd/atlas
)
chmod 0755 "${BIN_PATH}" || true

ADMIN_PASS="$(rand_password)"

# Create config if needed (random by default; allow manual input if declined).
if [[ ! -f "${CFG_PATH}" ]]; then
  PORT="$(prompt_port "$(rand_port 20000 60000)")"
  BASE_PATH="$(prompt_base_path "/$(rand_hex 12)")"
  ADMIN_PASS="$(prompt_password "${ADMIN_PASS}")"

  cat > "${CFG_PATH}" <<EOF
{
  "listen": "127.0.0.1:${PORT}",
  "root": "/",
  "base_path": "${BASE_PATH}",
  "cookie_secure": true,
  "enable_exec": true,
  "enable_firewall": true,
  "enable_admin_actions": true,
  "service_name": "atlas.service",
  "fs_sudo": true,
  "fs_users": ["*"],
  "master_key_file": "${INSTALL_DIR}/atlas.master.key",
  "user_db_path": "${INSTALL_DIR}/atlas.users.db",
  "firewall_db_path": "${INSTALL_DIR}/atlas.firewall.db"
}
EOF
  chmod 0600 "${CFG_PATH}" 2>/dev/null || true
fi

# Create/update admin user with all privileges (also creates config + keys + user DB if missing).
"${BIN_PATH}" -config "${CFG_PATH}" user add \
  -user admin \
  -pass "${ADMIN_PASS}" \
  -role admin \
  -exec=true \
  -procs=true \
  -fw=true \
  -fs-sudo=true \
  -fs-users='*' >/dev/null

listen="$(sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${CFG_PATH}" | head -n 1)"
base_path="$(sed -n 's/.*"base_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${CFG_PATH}" | head -n 1)"
scheme="https"

port=""
if [[ "${listen}" == *:* ]]; then
  port="${listen##*:}"
fi
base_path="$(normalize_base_path "${base_path:-/}")"
base_path="${base_path%/}"
if [[ "${base_path}" == "/" ]]; then
  base_path=""
fi

url="${scheme}://localhost"
if [[ -n "${port}" ]]; then
  url="${url}:${port}"
fi
url="${url}${base_path}/login"

cat <<EOF
Installed:
  bin:    ${BIN_PATH}
  config: ${CFG_PATH}

Login:
  url:  ${url}
  user: admin
  pass: ${ADMIN_PASS}

Run:
  ${BIN_PATH} -config ${CFG_PATH}
EOF
