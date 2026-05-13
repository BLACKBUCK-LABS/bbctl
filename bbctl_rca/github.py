"""GitHub commit metadata fetch for SCM/compliance RCA.

For commit-SHA hashes mentioned in the log, query GitHub REST API to get
author / date / message / files-changed summary. Helps RCA explain WHY two
commits differ (e.g. signed-off vs resolved).

Auth: GITHUB_PAT loaded from env (Secrets Manager → BBCTL_GITHUB_PAT).
Cached 24h via existing tool_cache.

We don't know the service's actual git repo in general, so we accept a hint
(`service` name) and try common org repo names. If repo unknown, fetches
commits using the bare SHA-only endpoint when possible (org-wide search).
"""
import os
import re
import httpx
from . import cache


SHA_RE = re.compile(r"\b([0-9a-f]{40})\b")
SHORT_SHA_RE = re.compile(r"\b([0-9a-f]{7,12})\b")

GH_PAT = os.environ.get("BBCTL_GITHUB_PAT", "")
GH_ORG = os.environ.get("BBCTL_GITHUB_ORG", "BLACKBUCK-LABS")
MAX_COMMITS = 4   # cap how many commits we fetch per call


def _extract_shas(text: str) -> list[str]:
    """Return unique 40-char SHAs in encounter order, capped."""
    seen = []
    for m in SHA_RE.finditer(text or ""):
        s = m.group(1)
        if s not in seen:
            seen.append(s)
        if len(seen) >= MAX_COMMITS:
            break
    return seen


def _candidate_repos(service: str) -> list[str]:
    """Guess likely repo names for a service. First match wins on GitHub."""
    s = service.strip()
    cands = [s]
    # Common BlackBuck transforms
    if "_" in s:
        cands.append(s.replace("_", "-"))
    if "-" in s:
        cands.append(s.replace("-", "_"))
    # de-dup preserving order
    return list(dict.fromkeys(cands))


async def fetch_commit(repo: str, sha: str) -> dict | None:
    """Fetch one commit from GitHub. Returns slim dict or None on 404/error."""
    cached = cache.get_tool_cache("gh_commit", {"repo": repo, "sha": sha})
    if cached is not None:
        return cached if cached else None

    if not GH_PAT:
        return None

    url = f"https://api.github.com/repos/{GH_ORG}/{repo}/commits/{sha}"
    headers = {
        "Authorization": f"Bearer {GH_PAT}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    try:
        async with httpx.AsyncClient(timeout=8.0) as client:
            r = await client.get(url, headers=headers)
            if r.status_code == 404:
                cache.set_tool_cache("gh_commit", {"repo": repo, "sha": sha}, {})
                return None
            r.raise_for_status()
            data = r.json()
    except Exception:
        return None

    commit = data.get("commit") or {}
    author = commit.get("author") or {}
    files = data.get("files") or []
    slim = {
        "repo": repo,
        "sha": data.get("sha", sha)[:12],
        "author": author.get("name"),
        "email": author.get("email"),
        "date": author.get("date"),
        "message": (commit.get("message") or "")[:300],
        "files_changed": [
            {"path": f.get("filename"), "status": f.get("status"),
             "additions": f.get("additions"), "deletions": f.get("deletions")}
            for f in files[:10]
        ],
        "files_total": len(files),
    }
    cache.set_tool_cache("gh_commit", {"repo": repo, "sha": sha}, slim)
    return slim


async def _resolve_repo_for_sha(sha: str, service: str) -> tuple[str, dict] | None:
    """Try candidate repos for this service until one returns a hit."""
    for repo in _candidate_repos(service):
        result = await fetch_commit(repo, sha)
        if result:
            return repo, result
    return None


async def fetch_commits_from_log(log_window: str, service: str) -> list[dict]:
    """Extract SHAs from log, fetch commit details. Returns list of slim dicts."""
    if not GH_PAT:
        return []
    shas = _extract_shas(log_window)
    if not shas:
        return []

    results = []
    for sha in shas:
        hit = await _resolve_repo_for_sha(sha, service)
        if hit:
            _, commit = hit
            results.append(commit)
    return results
