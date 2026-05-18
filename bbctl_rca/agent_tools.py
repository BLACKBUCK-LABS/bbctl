"""Tool implementations for the Option C agent-only RCA path.

Each function here is the runtime for one entry in tool_schemas.TOOLS.
Naming convention: function name MUST match the `function.name` in the
schema (e.g. `jira_get_ticket` here ↔ `"name": "jira_get_ticket"` in
the schema), so agent.py's dispatcher can resolve them dynamically.

Existing tool functions stay where they are (mcp_tools.py for
repo_*/get_jenkins_job_config, jira.py:fetch_ticket, etc.) — this
module provides thin wrappers that match the agent contract + adds
the 8 NEW tools that didn't exist before.

What's NEW here (Phase 2):
  - jira_get_ticket      (wrap jira.fetch_ticket)
  - jira_search          (new — JQL search)
  - github_get_commit    (wrap github.fetch_commit)
  - github_find_pr_for_commit  (new)
  - github_read_file     (new — raw.githubusercontent.com)
  - github_recent_commits (new)
  - list_runbooks        (new — list docops/runbooks/*.md)
  - read_runbook         (new — read one file)

What stays in mcp_tools.py / jenkins.py for Phase 3 to dispatch from:
  - repo_read_file, repo_search, repo_find_function, repo_recent_commits
  - get_jenkins_job_config

What lands in Phase 5 (AWS):
  - aws_describe_target_health, aws_describe_target_group,
    aws_describe_instance, aws_describe_listener_rule,
    aws_run_ssm_command

What lands in Phase 6:
  - code_review (uses gpt-4o-mini via existing OPENAI_API_KEY)
"""
from __future__ import annotations

import asyncio
import base64
import os
from pathlib import Path

import httpx

from . import cache, github, jira

# ─── runbook tools ──────────────────────────────────────────────────────

# docops/ on disk. Falls back to the repo path during local dev.
RUNBOOKS_DIR = Path(os.environ.get(
    "BBCTL_RCA_RUNBOOKS_DIR",
    "/opt/bbctl-rca/docops/runbooks",
))
RUNBOOKS_DIR_FALLBACK = Path(__file__).resolve().parent.parent / "docops" / "runbooks"


def _runbooks_dir() -> Path:
    if RUNBOOKS_DIR.is_dir():
        return RUNBOOKS_DIR
    return RUNBOOKS_DIR_FALLBACK


def list_runbooks() -> list[dict]:
    """List available runbook files with their first-paragraph summary.

    Reads the "## What this class means" or first non-heading paragraph
    so the LLM can pick which one to read_runbook() without loading them
    all into context.
    """
    d = _runbooks_dir()
    if not d.is_dir():
        return [{"error": f"runbooks dir not found at {d}"}]
    out = []
    for f in sorted(d.glob("*.md")):
        try:
            text = f.read_text(errors="replace")
        except Exception as e:
            out.append({"name": f.stem, "summary": f"<read error: {e}>"})
            continue
        # Extract a one-line summary: first non-empty line after the
        # "## What this class means" header, falling back to the second
        # non-empty line of the file (after the H1).
        summary = ""
        marker = "## What this class means"
        if marker in text:
            after = text.split(marker, 1)[1]
            for line in after.splitlines():
                line = line.strip()
                if line and not line.startswith("#"):
                    summary = line
                    break
        if not summary:
            # fallback: first non-heading, non-blank line
            for line in text.splitlines():
                ls = line.strip()
                if ls and not ls.startswith("#"):
                    summary = ls
                    break
        out.append({"name": f.stem, "summary": summary[:200]})
    return out


def read_runbook(name: str) -> str:
    """Read one runbook's full markdown by name (without .md)."""
    d = _runbooks_dir()
    p = d / f"{name}.md"
    if not p.is_file():
        avail = ", ".join(sorted(f.stem for f in d.glob("*.md")))
        return f"runbook '{name}' not found. Available: {avail}"
    try:
        return p.read_text(errors="replace")
    except Exception as e:
        return f"runbook read error: {e}"


# ─── jira tools ─────────────────────────────────────────────────────────


async def jira_get_ticket(key: str) -> dict:
    """Fetch one Jira ticket. Thin wrapper over jira.fetch_ticket()
    which already returns a slim dict (key, summary, status, assignee,
    components, fix_versions, description, custom_fields)."""
    return await jira.fetch_ticket(key)


async def jira_search(jql: str, max: int = 10) -> list[dict]:
    """JQL search via Jira REST. Returns ticket summaries (no full
    fields — call jira_get_ticket per key if you need detail)."""
    if not (jira.JIRA_URL and jira.JIRA_USER and jira.JIRA_TOKEN):
        return [{"error": "jira creds not configured"}]
    cache_key = {"jql": jql, "max": max}
    cached = cache.get_tool_cache("jira_search", cache_key)
    if cached is not None:
        return cached

    url = f"{jira.JIRA_URL.rstrip('/')}/rest/api/2/search"
    headers = jira._auth_header()
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            r = await client.get(
                url,
                headers=headers,
                params={
                    "jql": jql,
                    "maxResults": max,
                    "fields": "summary,status,assignee",
                },
            )
            r.raise_for_status()
            data = r.json()
    except Exception as e:
        return [{"error": f"jira search failed: {e}"}]

    out = []
    for it in data.get("issues", [])[:max]:
        f = it.get("fields") or {}
        out.append({
            "key": it.get("key"),
            "summary": f.get("summary"),
            "status": (f.get("status") or {}).get("name"),
            "assignee": (f.get("assignee") or {}).get("displayName"),
        })
    cache.set_tool_cache("jira_search", cache_key, out)
    return out


# ─── github tools ───────────────────────────────────────────────────────


async def github_get_commit(repo: str, sha: str) -> dict | None:
    """Fetch one commit. Thin wrapper over github.fetch_commit()."""
    return await github.fetch_commit(repo, sha)


async def github_find_pr_for_commit(repo: str, sha: str) -> dict | None:
    """Locate the merged PR that contains a commit. Returns the first
    merged PR found, or None."""
    if not github.GH_PAT:
        return None
    cache_key = {"repo": repo, "sha": sha}
    cached = cache.get_tool_cache("gh_pr_for_commit", cache_key)
    if cached is not None:
        return cached if cached else None

    url = f"https://api.github.com/repos/{github.GH_ORG}/{repo}/commits/{sha}/pulls"
    headers = {
        "Authorization": f"Bearer {github.GH_PAT}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    try:
        async with httpx.AsyncClient(timeout=8.0) as client:
            r = await client.get(url, headers=headers)
            if r.status_code == 404:
                cache.set_tool_cache("gh_pr_for_commit", cache_key, {})
                return None
            r.raise_for_status()
            prs = r.json() or []
    except Exception:
        return None
    # Prefer merged PRs; fall back to first listed.
    chosen = next((p for p in prs if p.get("merged_at")), prs[0] if prs else None)
    if not chosen:
        cache.set_tool_cache("gh_pr_for_commit", cache_key, {})
        return None
    slim = {
        "number": chosen.get("number"),
        "title": chosen.get("title"),
        "merged_at": chosen.get("merged_at"),
        "author": (chosen.get("user") or {}).get("login"),
        "url": chosen.get("html_url"),
        "state": chosen.get("state"),
    }
    cache.set_tool_cache("gh_pr_for_commit", cache_key, slim)
    return slim


async def github_read_file(repo: str, path: str, ref: str,
                           start: int = 1, end: int = 100) -> str:
    """Read a line slice from a GitHub repo at a specific ref via the
    Contents API. Returns numbered lines as a string."""
    if not github.GH_PAT:
        return "error: GH_PAT not configured"
    cache_key = {"repo": repo, "path": path, "ref": ref,
                 "start": start, "end": end}
    cached = cache.get_tool_cache("gh_read_file", cache_key)
    if cached is not None:
        return cached

    url = f"https://api.github.com/repos/{github.GH_ORG}/{repo}/contents/{path}"
    headers = {
        "Authorization": f"Bearer {github.GH_PAT}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    params = {"ref": ref}
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            r = await client.get(url, headers=headers, params=params)
            if r.status_code == 404:
                return f"error: file {repo}/{path}@{ref} not found"
            r.raise_for_status()
            data = r.json()
    except Exception as e:
        return f"error: {e}"

    # GitHub returns dict for files, list for directories.
    if isinstance(data, list):
        names = ", ".join(item.get("name", "?") for item in data[:20])
        return f"error: path is a directory. children: {names}"
    enc = data.get("encoding") or ""
    if enc != "base64":
        return f"error: unexpected encoding '{enc}'"
    try:
        content = base64.b64decode(data["content"]).decode("utf-8", errors="replace")
    except Exception as e:
        return f"error: base64 decode failed: {e}"

    lines = content.splitlines()
    s = max(1, int(start)) - 1
    e = min(len(lines), int(end))
    sliced = lines[s:e]
    out = "\n".join(f"{i + s + 1}: {ln}" for i, ln in enumerate(sliced))
    cache.set_tool_cache("gh_read_file", cache_key, out)
    return out


async def github_recent_commits(repo: str, branch: str = "main",
                                n: int = 10) -> list[dict]:
    """Recent commits on a GitHub repo branch. Returns slim dicts."""
    if not github.GH_PAT:
        return [{"error": "GH_PAT not configured"}]
    cache_key = {"repo": repo, "branch": branch, "n": n}
    cached = cache.get_tool_cache("gh_recent_commits", cache_key)
    if cached is not None:
        return cached

    url = f"https://api.github.com/repos/{github.GH_ORG}/{repo}/commits"
    headers = {
        "Authorization": f"Bearer {github.GH_PAT}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    params = {"sha": branch, "per_page": min(int(n), 30)}
    try:
        async with httpx.AsyncClient(timeout=8.0) as client:
            r = await client.get(url, headers=headers, params=params)
            if r.status_code == 404:
                return [{"error": f"repo or branch not found: {repo}@{branch}"}]
            r.raise_for_status()
            data = r.json() or []
    except Exception as e:
        return [{"error": f"gh recent commits failed: {e}"}]

    out = []
    for c in data[:n]:
        commit = c.get("commit") or {}
        author = commit.get("author") or {}
        out.append({
            "sha": (c.get("sha") or "")[:12],
            "author": author.get("name"),
            "date": author.get("date"),
            "message": (commit.get("message") or "").splitlines()[0][:200],
        })
    cache.set_tool_cache("gh_recent_commits", cache_key, out)
    return out


# ─── dispatch helper ────────────────────────────────────────────────────


# Maps OpenAI function name → callable. agent.py's dispatcher uses this
# (alongside the existing mcp_tools.* registrations) to route LLM tool
# calls. Each value is either sync or async; the dispatcher awaits the
# coroutine if needed.
NEW_TOOL_DISPATCH: dict[str, callable] = {
    "list_runbooks":       list_runbooks,
    "read_runbook":        read_runbook,
    "jira_get_ticket":     jira_get_ticket,
    "jira_search":         jira_search,
    "github_get_commit":   github_get_commit,
    "github_find_pr_for_commit": github_find_pr_for_commit,
    "github_read_file":    github_read_file,
    "github_recent_commits": github_recent_commits,
}
