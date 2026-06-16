#!/usr/bin/env bash
# Pull the latest jenkins_pipeline + InfraComposer + bbctl repo content used
# by the bbctl-rca service. Run via cron every 2 hours.
#
# bbctl:           git pull on the service checkout — picks up new docops/,
#                  prompts/, bbctl_rca/ code, runbooks, job_flows.
# Reference repos: hard reset against origin/<branch> — never carry local
#                  edits. Auto-clones if not present yet (reads GITHUB_PAT
#                  from Secrets Manager).
# Docs:            REMOVED S3 sync (May 2026). docops/ is now canonical in
#                  the bbctl repo itself. The legacy S3 mirror at
#                  s3://docops-doc-storage/docs/ was overlaying + deleting
#                  Phase 1+5 work (runbooks, job_flows, MAP.md) on every
#                  cron run. Single source of truth = git.
#
# Re-index RAG after pulling bbctl (only when docops/ actually changed —
# checked via git diff against the prior HEAD).
#
# Restart bbctl-rca at the end so its in-process caches reload.
set -uo pipefail

BASE_DIR="/home/ubuntu/project/bbctl"
REPOS_DIR="$BASE_DIR/repos"
DOCS_DIR="$BASE_DIR/docops"
LOG="/var/log/bbctl-rca/sync.log"

# Branches to track per repo (override via env if needed).
# jenkins_pipeline → master. Master is the canonical default branch and
# carries the latest landed fixes (e.g. JiraDetails build-param fallback).
# Earlier the agent tracked release/REQ-463-staggerprodplusupdate-v2, but
# that branch lagged behind master and the RCA agent saw stale gate code.
# If a specific historical build needs to be diagnosed against an older
# branch, override via env: `JP_BRANCH=<branch> ./bbctl-sync.sh`.
JP_BRANCH="${JP_BRANCH:-master}"
IC_BRANCH="${IC_BRANCH:-main}"

# bbctl service repo (this checkout). Production currently runs the
# LangGraph-gates branch (feature/bbctl-rca-agent-RAG-LANG).
BBCTL_BRANCH="${BBCTL_BRANCH:-feature/bbctl-rca-agent-RAG-LANG}"

REGION="${AWS_REGION:-ap-south-1}"
SECRET_ID="${BBCTL_SECRET_ID:-bbctl-rca/prod}"

mkdir -p "$REPOS_DIR" "$(dirname "$LOG")"
ts() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[$(ts)] $*" | tee -a "$LOG"; }

restart_service() {
    log "restarting bbctl-rca to pick up new config.json / docs"
    sudo systemctl restart bbctl-rca
}
trap 'restart_service' EXIT

log "==== sync start ===="

# Fetch GitHub PAT from Secrets Manager (used for initial clone only)
get_github_pat() {
    aws secretsmanager get-secret-value \
        --secret-id "$SECRET_ID" \
        --region "$REGION" \
        --query 'SecretString' \
        --output text 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('github_pat',''))" 2>/dev/null || true
}

# Self-heal permissions
for d in jenkins_pipeline InfraComposer; do
    if [ -d "$REPOS_DIR/$d" ]; then
        chown -R ubuntu:ubuntu "$REPOS_DIR/$d" 2>/dev/null || true
        chmod -R u+w "$REPOS_DIR/$d" 2>/dev/null || true
    fi
done

# 1. jenkins_pipeline
if [ -d "$REPOS_DIR/jenkins_pipeline/.git" ]; then
    log "syncing jenkins_pipeline (branch=$JP_BRANCH)"
    sudo -u ubuntu git -C "$REPOS_DIR/jenkins_pipeline" fetch --quiet origin "$JP_BRANCH" \
        && sudo -u ubuntu git -C "$REPOS_DIR/jenkins_pipeline" reset --hard "origin/$JP_BRANCH" --quiet \
        || log "WARN: jenkins_pipeline sync failed"
else
    log "jenkins_pipeline not cloned — cloning now"
    PAT=$(get_github_pat)
    if [ -n "$PAT" ]; then
        sudo -u ubuntu git clone --branch "$JP_BRANCH" \
            "https://x-access-token:${PAT}@github.com/BLACKBUCK-LABS/jenkins_pipeline.git" \
            "$REPOS_DIR/jenkins_pipeline" \
            && log "jenkins_pipeline cloned (branch=$JP_BRANCH)" \
            || log "WARN: jenkins_pipeline clone failed"
    else
        log "WARN: no github_pat in Secrets Manager — cannot clone jenkins_pipeline"
    fi
fi

# 2. InfraComposer
if [ -d "$REPOS_DIR/InfraComposer/.git" ]; then
    log "syncing InfraComposer (branch=$IC_BRANCH)"
    sudo -u ubuntu git -C "$REPOS_DIR/InfraComposer" fetch --quiet origin "$IC_BRANCH" \
        && sudo -u ubuntu git -C "$REPOS_DIR/InfraComposer" reset --hard "origin/$IC_BRANCH" --quiet \
        || log "WARN: InfraComposer sync failed"
else
    log "InfraComposer not cloned — cloning now"
    PAT=$(get_github_pat)
    if [ -n "$PAT" ]; then
        sudo -u ubuntu git clone \
            "https://x-access-token:${PAT}@github.com/BLACKBUCK-LABS/InfraComposer.git" \
            "$REPOS_DIR/InfraComposer" \
            && log "InfraComposer cloned (branch=$IC_BRANCH)" \
            || log "WARN: InfraComposer clone failed"
    else
        log "WARN: no github_pat in Secrets Manager — cannot clone InfraComposer"
    fi
fi

# 3. bbctl service repo — `git pull` on the checkout running the service.
#    Captures docops/, prompts/, bbctl_rca/ updates without needing a
#    separate deploy step. Hard reset to origin/<branch> to mirror the
#    same "never carry local edits" rule as the reference repos.
#
#    Tracks `feature/bbctl-rca-agent-RAG-LANG` (RAG + LangGraph gates).
#    Override with `BBCTL_BRANCH=<branch>` env when rolling back.
if [ -d "$BASE_DIR/.git" ]; then
    # Detect whether docops/ changed in this pull — only re-index RAG
    # when the embedded corpus actually shifted.
    _docops_before=$(sudo -u ubuntu git -C "$BASE_DIR" rev-parse HEAD:docops 2>/dev/null || echo "none")

    log "syncing bbctl (branch=$BBCTL_BRANCH)"
    sudo -u ubuntu git -C "$BASE_DIR" fetch --quiet origin "$BBCTL_BRANCH" \
        && sudo -u ubuntu git -C "$BASE_DIR" reset --hard "origin/$BBCTL_BRANCH" --quiet \
        || log "WARN: bbctl sync failed"

    _docops_after=$(sudo -u ubuntu git -C "$BASE_DIR" rev-parse HEAD:docops 2>/dev/null || echo "none")
    if [ "$_docops_before" != "$_docops_after" ]; then
        log "docops/ tree changed ($_docops_before → $_docops_after) — re-indexing RAG"
        sudo -u ubuntu "$BASE_DIR/.venv/bin/python" -m bbctl_rca.rag index-docops \
            2>&1 | tee -a "$LOG" \
            || log "WARN: RAG index-docops failed"
    else
        log "docops/ unchanged — skipping RAG re-index"
    fi
else
    log "WARN: $BASE_DIR is not a git repo — cannot self-pull. Did the "
    log "WARN: initial deploy clone with git, not a tarball?"
fi

# 4. Index past RCA audits — past-incident semantic memory.
#    audit.py writes one JSON per RCA to /var/log/bbctl-rca/. The
#    RAG indexer reads these as `source_type=audit` chunks so future
#    RCAs can surface "we saw this exact failure 3 weeks ago, here's
#    what fixed it" via rag_search(source_types=["audit"]).
#    Content-hash dedup means already-indexed audits are no-op.
#    Stale records (>60 days) are skipped via BBCTL_RAG_AUDIT_MAX_DAYS.
#    Cost: ~$0.001 per new audit (text-embedding-3-small, ~500 tokens
#    per chunk).
log "indexing recent RCA audits"
sudo -u ubuntu "$BASE_DIR/.venv/bin/python" -m bbctl_rca.rag index-audits \
    2>&1 | tee -a "$LOG" \
    || log "WARN: RAG index-audits failed"

log "==== sync done ===="
