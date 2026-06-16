#!/usr/bin/env bash
# One-shot deploy of bbctl-rca to bbctl-ec2.
# Run from repo root on bbctl-ec2 (or pass REPO_DIR as arg).
set -euo pipefail

REPO_DIR="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
APP_DIR="/opt/bbctl-rca"
VENV="$APP_DIR/.venv"
CACHE_DIR="/var/cache/bbctl-rca"
AUDIT_DIR="/var/log/bbctl-rca"
SYSTEMD_DEST="/etc/systemd/system"

echo "==> source: $REPO_DIR"
echo "==> target: $APP_DIR"

# 1. Create dirs
sudo mkdir -p "$APP_DIR" "$CACHE_DIR" "$AUDIT_DIR"
sudo chown -R ubuntu:ubuntu "$APP_DIR" "$CACHE_DIR" "$AUDIT_DIR"

# 2. Sync package + supporting files.
# NO --delete: preserves repos/, docops/, .venv/ which are populated by other
# means (nightly sync scripts, pip install). Excludes Go artifacts.
rsync -av \
  --exclude='.git' \
  --exclude='*.go' \
  --exclude='go.mod' \
  --exclude='go.sum' \
  --exclude='.goreleaser.yml' \
  --exclude='.github' \
  --exclude='cmd/' \
  --exclude='commands/' \
  --exclude='internal/' \
  --exclude='repos/' \
  --exclude='docops/' \
  --exclude='.venv/' \
  "$REPO_DIR/" "$APP_DIR/"

# 3. Python venv + deps
if [[ ! -d "$VENV" ]]; then
  python3 -m venv "$VENV"
fi
"$VENV/bin/pip" install --upgrade pip -q
"$VENV/bin/pip" install -r "$APP_DIR/bbctl_rca/requirements.txt"

# 4. Start script executable
chmod +x "$APP_DIR/infra/scripts/bbctl-rca-start.sh"

# 5. Systemd unit
sudo cp "$APP_DIR/infra/systemd/bbctl-rca.service" "$SYSTEMD_DEST/"
sudo systemctl daemon-reload
sudo systemctl enable bbctl-rca.service
sudo systemctl restart bbctl-rca.service

echo ""
echo "==> done. status:"
sudo systemctl status bbctl-rca.service --no-pager -l
