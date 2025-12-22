#!/usr/bin/env bash
set -euo pipefail

TAG="${1:-}"
if [[ -z "${TAG}" ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${ROOT}/dist"
BIN="${DIST}/linux_amd64/atlas"

if [[ ! -x "${BIN}" ]]; then
  echo "missing binary: ${BIN}" >&2
  echo "build it first: GOOS=linux GOARCH=amd64 go build -o ${BIN} ./cmd/atlas" >&2
  exit 2
fi

APPDIR="${DIST}/AppDir"
rm -rf "${APPDIR}"
mkdir -p "${APPDIR}/usr/bin" "${APPDIR}/usr/share/applications" "${APPDIR}/usr/share/icons/hicolor/scalable/apps"

cp "${BIN}" "${APPDIR}/usr/bin/atlas"
cp "${ROOT}/packaging/atlas.desktop" "${APPDIR}/usr/share/applications/atlas.desktop"
cp "${ROOT}/packaging/atlas.svg" "${APPDIR}/usr/share/icons/hicolor/scalable/apps/atlas.svg"

cat >"${APPDIR}/AppRun" <<'EOF'
#!/usr/bin/env sh
set -eu

CFG="${ATLAS_CONFIG:-$HOME/.config/atlas/atlas.json}"
CFG_DIR="$(dirname "$CFG")"
mkdir -p "$CFG_DIR" >/dev/null 2>&1 || true

export ATLAS_CONFIG="$CFG"
exec "$APPDIR/usr/bin/atlas" "$@"
EOF
chmod +x "${APPDIR}/AppRun"

cp "${ROOT}/packaging/atlas.svg" "${APPDIR}/atlas.svg"
cp "${ROOT}/packaging/atlas.desktop" "${APPDIR}/atlas.desktop"

OUT="${DIST}/Atlas-${TAG}-x86_64.AppImage"
APPIMAGETOOL="${APPIMAGETOOL:-appimagetool}"

if command -v "${APPIMAGETOOL}" >/dev/null 2>&1; then
  "${APPIMAGETOOL}" "${APPDIR}" "${OUT}"
elif [[ -x "${ROOT}/appimagetool-x86_64.AppImage" ]]; then
  "${ROOT}/appimagetool-x86_64.AppImage" --appimage-extract-and-run "${APPDIR}" "${OUT}"
else
  echo "appimagetool not found; set APPIMAGETOOL or provide appimagetool-x86_64.AppImage" >&2
  exit 2
fi

echo "built: ${OUT}"

