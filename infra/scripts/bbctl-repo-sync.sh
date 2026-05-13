#!/bin/bash
set -e
REPOS_DIR="/opt/bbctl-rca/repos"
LOG="/var/log/bbctl-rca/repo-sync.log"
echo "$(date -u) starting repo sync" >> "$LOG"

for repo in jenkins_pipeline InfraComposer; do
    dir="$REPOS_DIR/$repo"
    chmod -R u+w "$dir"
    git -C "$dir" fetch --prune origin >> "$LOG" 2>&1
    default_branch=$(git -C "$dir" remote show origin | grep 'HEAD branch' | awk '{print $NF}')
    git -C "$dir" reset --hard "origin/${default_branch}" >> "$LOG" 2>&1
    chmod -R a-w "$dir"
    echo "$(date -u) $repo synced branch=${default_branch} $(git -C $dir rev-parse --short HEAD)" >> "$LOG"
done
