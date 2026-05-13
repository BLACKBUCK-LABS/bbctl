#!/usr/bin/env bash
# Fetch secrets from AWS Secrets Manager, export as env vars, start uvicorn.
# Requires IAM role on bbctl-ec2 with secretsmanager:GetSecretValue on
# arn:aws:secretsmanager:ap-south-1:<acct>:secret:bbctl-rca/prod-*
set -euo pipefail

APP_DIR="/opt/bbctl-rca"
VENV="$APP_DIR/.venv"
export AWS_REGION="${AWS_REGION:-ap-south-1}"
export BBCTL_SECRET_ID="${BBCTL_SECRET_ID:-bbctl-rca/prod}"

# Fetch secrets via boto3 and export as env vars
eval "$("$VENV/bin/python" -m bbctl_rca.secrets)"

cd "$APP_DIR"
exec "$VENV/bin/uvicorn" bbctl_rca.main:app \
  --host 127.0.0.1 \
  --port 7070 \
  --workers 2 \
  --log-level info \
  --no-access-log
