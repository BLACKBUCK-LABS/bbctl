#!/usr/bin/env bash
# Decrypt SOPS secrets, export as env vars, start bbctl-rca uvicorn
set -euo pipefail

KEYS_FILE="/etc/bbctl-rca/keys.enc.yaml"
AGE_KEY="/etc/bbctl-rca/keys/bbctl-rca.key"
APP_DIR="/opt/bbctl-rca"
VENV="$APP_DIR/.venv"

if [[ ! -f "$KEYS_FILE" ]]; then
  echo "ERROR: secrets file not found: $KEYS_FILE" >&2
  exit 1
fi

# Decrypt and export env vars
eval "$(
  SOPS_AGE_KEY_FILE="$AGE_KEY" sops --decrypt "$KEYS_FILE" \
    | python3 -c "
import sys, yaml
d = yaml.safe_load(sys.stdin)
for k, v in d.items():
    print(f'export BBCTL_{k.upper()}={v!r}')
"
)"

cd "$APP_DIR"
exec "$VENV/bin/uvicorn" bbctl_rca.main:app \
  --host 127.0.0.1 \
  --port 7070 \
  --workers 2 \
  --log-level info \
  --no-access-log
