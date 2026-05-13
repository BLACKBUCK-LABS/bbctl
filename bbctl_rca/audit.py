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
