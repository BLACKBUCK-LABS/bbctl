"""Persist every RCA call to disk for debugging + future RAG corpus.

Each call writes one JSON file at /var/log/bbctl-rca/<request_id>.json with:
- request_id, timestamp, job, build, service
- error_class
- token usage + cost
- prompt (clean log window only, not full prompt — re-derivable)
- response (LLM JSON output)
"""
import json
import os
from datetime import datetime, timezone
from pathlib import Path

LOG_DIR = Path(os.environ.get("BBCTL_AUDIT_DIR", "/var/log/bbctl-rca"))


_REQUEST_ID_RE = __import__("re").compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")


def record(payload: dict) -> Path | None:
    """Write one RCA record. Returns path or None on failure."""
    try:
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        request_id = payload.get("request_id", "unknown")
        path = LOG_DIR / f"{request_id}.json"
        payload = {"recorded_at": datetime.now(timezone.utc).isoformat(), **payload}
        path.write_text(json.dumps(payload, indent=2, default=str))
        return path
    except Exception as e:
        # Audit must never fail the RCA call
        print(f"[audit] failed to record: {e}")
        return None


def read_by_request_id(request_id: str) -> dict | None:
    """Load an audit record by request_id. Returns the parsed dict or None.

    Validates the request_id matches the canonical uuid pattern before touching
    disk — defense-in-depth against path traversal since the value may come
    from an HTTP path param.
    """
    if not _REQUEST_ID_RE.match(request_id or ""):
        return None
    path = LOG_DIR / f"{request_id}.json"
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except Exception as e:
        print(f"[audit] failed to read {request_id}: {e}")
        return None
