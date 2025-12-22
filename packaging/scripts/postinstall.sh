#!/usr/bin/env sh
set -eu

mkdir -p /var/lib/atlas || true

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

echo "Atlas installed."
echo "Config: /etc/atlas/atlas.json"
echo "Data:   /var/lib/atlas/"
echo "Start:  systemctl enable --now atlas"

