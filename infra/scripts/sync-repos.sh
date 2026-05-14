#!/usr/bin/env bash
# Pull the latest jenkins_pipeline + InfraComposer + docops content used by
# the bbctl-rca LLM tool-context builder. Run via cron every 2 hours.
#
# Repos: hard reset against origin/<branch> — never carry local edits.
# Docs:  rsync from an S3 bucket (read-only mirror of s3_docs/docs/).
#
# Restart bbctl-rca at the end so its in-process config.json cache reloads.
#
# Failures must be loud but must NOT leave the service stopped — `set -e`
# bails before the restart, so restart runs in a trap.
set -uo pipefail

REPOS_DIR="/opt/bbctl-rca/repos"
DOCS_DIR="/opt/bbctl-rca/docops"
LOG="/var/log/bbctl-rca/sync.log"

# Branches to track per repo (override via env if needed)
JP_BRANCH="${JP_BRANCH:-master}"
IC_BRANCH="${IC_BRANCH:-main}"

# S3 source for docops/ (read-only mirror)
DOCS_S3_URI="${DOCS_S3_URI:-s3://docops-doc-storage/docs/}"

mkdir -p "$(dirname "$LOG")"
ts() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[$(ts)] $*" | tee -a "$LOG"; }

restart_service() {
    log "restarting bbctl-rca to pick up new config.json / docs"
    sudo systemctl restart bbctl-rca
}
# Ensure restart even if a sync step fails partway
trap 'restart_service' EXIT

log "==== sync start ===="

# 1. jenkins_pipeline
if [ -d "$REPOS_DIR/jenkins_pipeline/.git" ]; then
    log "syncing jenkins_pipeline (branch=$JP_BRANCH)"
    sudo -u ubuntu git -C "$REPOS_DIR/jenkins_pipeline" fetch --quiet origin "$JP_BRANCH" \
        && sudo -u ubuntu git -C "$REPOS_DIR/jenkins_pipeline" reset --hard "origin/$JP_BRANCH" --quiet \
        || log "WARN: jenkins_pipeline sync failed"
else
    log "WARN: $REPOS_DIR/jenkins_pipeline not a git clone — skipping"
fi

# 2. InfraComposer
if [ -d "$REPOS_DIR/InfraComposer/.git" ]; then
    log "syncing InfraComposer (branch=$IC_BRANCH)"
    sudo -u ubuntu git -C "$REPOS_DIR/InfraComposer" fetch --quiet origin "$IC_BRANCH" \
        && sudo -u ubuntu git -C "$REPOS_DIR/InfraComposer" reset --hard "origin/$IC_BRANCH" --quiet \
        || log "WARN: InfraComposer sync failed"
else
    log "WARN: $REPOS_DIR/InfraComposer not a git clone — skipping"
fi

# 3. docops/ from S3 (mirror — deletes anything not in S3)
if command -v aws >/dev/null 2>&1; then
    log "syncing docops from $DOCS_S3_URI"
    sudo -u ubuntu aws s3 sync "$DOCS_S3_URI" "$DOCS_DIR/" --delete --quiet \
        || log "WARN: docops S3 sync failed"
else
    log "WARN: aws CLI not found — skipping docops sync"
fi

log "==== sync done ===="
