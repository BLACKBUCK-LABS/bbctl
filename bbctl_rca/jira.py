"""Jira ticket fetch + extraction.

Detects Jira ticket IDs in text via regex (e.g. FMSCAT-5887), fetches summary
+ status + key fields via Jira REST API. Result fed into LLM tool context.

Auth uses email + API token (Atlassian Cloud). Set via secrets:
- jira_url       e.g. https://blackbuck.atlassian.net
- jira_user      operator email
- jira_api_token Atlassian API token

Cached for TOOL_CACHE_TTL (24h) — ticket details don't change every minute.
"""
import os
import re
import base64
import httpx
from . import cache


TICKET_RE = re.compile(r"\b([A-Z][A-Z0-9]+-\d+)\b")
MAX_TICKETS = 3

JIRA_URL = os.environ.get("BBCTL_JIRA_URL", "")
JIRA_USER = os.environ.get("BBCTL_JIRA_USER", "")
JIRA_TOKEN = os.environ.get("BBCTL_JIRA_API_TOKEN", "")


def extract_tickets(text: str) -> list[str]:
    """Return unique ticket keys in encounter order, capped at MAX_TICKETS."""
    seen = []
    for m in TICKET_RE.finditer(text or ""):
        k = m.group(1)
        if k not in seen:
            seen.append(k)
        if len(seen) >= MAX_TICKETS:
            break
    return seen


def _auth_header() -> dict:
    creds = f"{JIRA_USER}:{JIRA_TOKEN}".encode()
    return {"Authorization": "Basic " + base64.b64encode(creds).decode(), "Accept": "application/json"}


async def fetch_ticket(key: str) -> dict:
    """Fetch a single ticket. Returns slim dict; on error returns {error}."""
    if not (JIRA_URL and JIRA_USER and JIRA_TOKEN):
        return {"error": "jira creds not configured"}

    cached = cache.get_tool_cache("jira_ticket", {"key": key})
    if cached is not None:
        return cached

    url = f"{JIRA_URL.rstrip('/')}/rest/api/2/issue/{key}"
    try:
        async with httpx.AsyncClient(timeout=8.0) as client:
            r = await client.get(url, headers=_auth_header(), params={
                "fields": "summary,status,assignee,reporter,fixVersions,components,description,labels,priority,resolution"
            })
            r.raise_for_status()
            data = r.json()
    except Exception as e:
        result = {"error": f"jira fetch failed: {e}", "key": key}
        return result

    f = data.get("fields", {})
    result = {
        "key": data.get("key", key),
        "summary": f.get("summary", ""),
        "status": (f.get("status") or {}).get("name"),
        "priority": (f.get("priority") or {}).get("name"),
        "assignee": ((f.get("assignee") or {}).get("displayName")),
        "reporter": ((f.get("reporter") or {}).get("displayName")),
        "labels": f.get("labels", []),
        "components": [c.get("name") for c in (f.get("components") or [])],
        "fix_versions": [v.get("name") for v in (f.get("fixVersions") or [])],
        "resolution": (f.get("resolution") or {}).get("name") if f.get("resolution") else None,
        # description can be huge — truncate
        "description": (f.get("description") or "")[:1000],
    }
    cache.set_tool_cache("jira_ticket", {"key": key}, result)
    return result


async def fetch_all(keys: list[str]) -> list[dict]:
    """Fetch multiple tickets sequentially (small N, no need for parallel)."""
    return [await fetch_ticket(k) for k in keys]
