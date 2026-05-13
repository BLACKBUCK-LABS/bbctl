#!/bin/bash
set -e
LOG="/var/log/bbctl-rca/docops-sync.log"
echo "$(date -u) starting docops sync" >> "$LOG"
aws s3 sync s3://docops-doc-storage/docs/ /opt/bbctl-rca/docops/ \
    --region ap-south-1 >> "$LOG" 2>&1
echo "$(date -u) docops sync done" >> "$LOG"
