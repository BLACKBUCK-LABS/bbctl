import hmac
import hashlib
import json
import os
import uuid

from fastapi import FastAPI, APIRouter, Request, HTTPException, Header
from fastapi.responses import JSONResponse

from .models import WebhookPayload, RCARequest, RCAResponse
from .jenkins import get_console_log, get_build_meta
from .window import extract_window, extract_failed_stage
from .sanitize import sanitize
from .classifier import classify
from .llm import run_rca
from .cache import (
    is_duplicate, mark_processed, over_daily_cap, add_spend,
    get_rca, set_rca,
)
from .evidence import verify as verify_evidence
from .audit import record as audit_record
from .slack import post as slack_post
import subprocess
import yaml
from pathlib import Path

app = FastAPI(title="bbctl-rca", version="0.1.0")
# All routes go on this router so we can mount them at both root and /rca.
# /rca prefix is for ALB path-based routing (bbctl.blackbuck.com/rca/*);
# root mount keeps direct-port access working for backward compat.
router = APIRouter()

# Config loaded from env (set via SOPS decrypt on startup)
JENKINS_URL = os.environ.get("BBCTL_JENKINS_URL", "http://10.34.42.254:8080")
JENKINS_USER = os.environ.get("BBCTL_JENKINS_USER", "g.hariharan@blackbuck.com")
JENKINS_TOKEN = os.environ.get("BBCTL_JENKINS_TOKEN", "")
WEBHOOK_SECRET = os.environ.get("BBCTL_WEBHOOK_SECRET", "")
LLM_API_KEY = os.environ.get("BBCTL_LLM_API_KEY", "")
LLM_PROVIDER = os.environ.get("BBCTL_LLM_PROVIDER", "gemini")

JENKINS_AUTH = (JENKINS_USER, JENKINS_TOKEN)


@router.get("/healthz")
async def health():
    return {"status": "ok", "provider": LLM_PROVIDER}


def verify_hmac(payload: bytes, signature: str) -> bool:
    expected = "sha256=" + hmac.new(
        WEBHOOK_SECRET.encode(), payload, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature)


@router.post("/v1/rca/webhook")
async def rca_webhook(
    request: Request,
    x_bbctl_signature: str = Header(None),
):
    body = await request.body()

    if WEBHOOK_SECRET and x_bbctl_signature:
        if not verify_hmac(body, x_bbctl_signature):
            raise HTTPException(status_code=401, detail="invalid signature")

    payload = WebhookPayload(**json.loads(body))
    return await _run_rca(payload.job, payload.build, payload.service, deep=False)


@router.post("/v1/rca")
async def rca_cli(req: RCARequest):
    meta = await get_build_meta(req.job, req.build, JENKINS_URL, JENKINS_AUTH)
    service = meta.get("actions", [{}])[0].get("parameters", [{}])
    # extract SERVICE param
    svc = req.job
    for action in meta.get("actions", []):
        for param in action.get("parameters", []):
            if param.get("name") == "SERVICE":
                svc = param["value"]
    return await _run_rca(req.job, req.build, svc, deep=req.deep)


async def _run_rca(job: str, build: int, service: str, deep: bool = False) -> dict:
    if over_daily_cap():
        raise HTTPException(status_code=429, detail="daily cost cap reached")

    # 24h cache: same job+build returns prior RCA without LLM call.
    # `deep=true` bypasses cache (operator explicitly wants re-analysis with
    # wider context). `?nocache=true` query param could be added later.
    if not deep:
        cached = get_rca(job, build)
        if cached:
            cached_copy = dict(cached)
            cached_copy["from_cache"] = True
            return cached_copy

    existing = is_duplicate(job, build)
    if existing and not deep:
        return {"cached": True, "request_id": existing}

    request_id = str(uuid.uuid4())

    raw_log = await get_console_log(job, build, JENKINS_URL, JENKINS_AUTH)
    build_meta = await get_build_meta(job, build, JENKINS_URL, JENKINS_AUTH)

    window = extract_window(raw_log, deep=deep)
    clean_window, redactions = sanitize(window)
    error_class = classify(clean_window)
    # Annotate build_meta with the actual last-entered stage from the log so
    # LLM doesn't guess between similarly-named stages (Prod+1 vs Prod, etc.).
    detected_stage = extract_failed_stage(raw_log)
    if detected_stage:
        build_meta = dict(build_meta)
        build_meta["detected_failed_stage"] = detected_stage
    # Also stash raw_log on build_meta for canary stage analyzer (needs full
    # log since canary blocks can span thousands of filtered lines).
    if error_class == "canary_fail":
        if not isinstance(build_meta, dict):
            build_meta = dict(build_meta)
        build_meta["_raw_log"] = raw_log

    result = await run_rca(
        LLM_PROVIDER,
        api_key=LLM_API_KEY,
        service=service,
        build_meta=build_meta,
        log_window=clean_window,
        error_class=error_class,
        deep=deep,
    )
    # Strip raw_log from build_meta after LLM call so it doesn't leak into
    # audit log / response (it's massive).
    if isinstance(build_meta, dict) and "_raw_log" in build_meta:
        build_meta = {k: v for k, v in build_meta.items() if k != "_raw_log"}
    result["request_id"] = request_id

    # cost estimate by provider
    tokens_in = result["tokens_used"].get("input", 0)
    tokens_out = result["tokens_used"].get("output", 0)
    if LLM_PROVIDER == "openai":
        # gpt-4o-mini: $0.15/1M input, $0.60/1M output
        cost = (tokens_in / 1_000_000 * 0.15) + (tokens_out / 1_000_000 * 0.60)
    else:
        # gemini-2.0-flash: $0.075/1M input, $0.30/1M output
        cost = (tokens_in / 1_000_000 * 0.075) + (tokens_out / 1_000_000 * 0.30)
    add_spend(cost)
    result["cost_usd"] = round(cost, 6)

    # Verify each evidence citation against repos on disk
    if "evidence" in result:
        result["evidence"] = verify_evidence(result["evidence"])

    mark_processed(job, build, request_id)
    set_rca(job, build, result)  # 24h cache for future repeat queries

    # Audit log + Slack notify (non-blocking, best-effort)
    audit_record({
        "request_id": request_id,
        "job": job,
        "build": build,
        "service": service,
        "error_class": error_class,
        "provider": LLM_PROVIDER,
        "cost_usd": cost,
        "redactions": redactions,
        "log_window_chars": len(clean_window),
        "log_window_sample": clean_window[:500],
        "rca": result,
    })
    await slack_post(result, job, build)

    return result


# Mount routes at both root (for direct port access) and /rca (for ALB
# path-based routing via bbctl.blackbuck.com/rca/*). Both URL shapes work.
app.include_router(router)
app.include_router(router, prefix="/rca")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("bbctl_rca.main:app", host="0.0.0.0", port=7070, reload=False)
