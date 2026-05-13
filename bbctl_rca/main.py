import hmac
import hashlib
import json
import os
import uuid

from fastapi import FastAPI, Request, HTTPException, Header
from fastapi.responses import JSONResponse

from .models import WebhookPayload, RCARequest, RCAResponse
from .jenkins import get_console_log, get_build_meta
from .window import extract_window
from .sanitize import sanitize
from .classifier import classify
from .llm import run_rca_gemini
from .cache import is_duplicate, mark_processed, over_daily_cap, add_spend
import subprocess
import yaml
from pathlib import Path

app = FastAPI(title="bbctl-rca", version="0.1.0")

# Config loaded from env (set via SOPS decrypt on startup)
JENKINS_URL = os.environ.get("BBCTL_JENKINS_URL", "http://10.34.42.254:8080")
JENKINS_USER = os.environ.get("BBCTL_JENKINS_USER", "g.hariharan@blackbuck.com")
JENKINS_TOKEN = os.environ.get("BBCTL_JENKINS_TOKEN", "")
WEBHOOK_SECRET = os.environ.get("BBCTL_WEBHOOK_SECRET", "")
LLM_API_KEY = os.environ.get("BBCTL_LLM_API_KEY", "")
LLM_PROVIDER = os.environ.get("BBCTL_LLM_PROVIDER", "gemini")

JENKINS_AUTH = (JENKINS_USER, JENKINS_TOKEN)


@app.get("/healthz")
async def health():
    return {"status": "ok", "provider": LLM_PROVIDER}


def verify_hmac(payload: bytes, signature: str) -> bool:
    expected = "sha256=" + hmac.new(
        WEBHOOK_SECRET.encode(), payload, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature)


@app.post("/v1/rca/webhook")
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


@app.post("/v1/rca")
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

    existing = is_duplicate(job, build)
    if existing and not deep:
        return {"cached": True, "request_id": existing}

    request_id = str(uuid.uuid4())

    raw_log = await get_console_log(job, build, JENKINS_URL, JENKINS_AUTH)
    build_meta = await get_build_meta(job, build, JENKINS_URL, JENKINS_AUTH)

    window = extract_window(raw_log)
    clean_window, redactions = sanitize(window)
    error_class = classify(clean_window)

    result = await run_rca_gemini(
        api_key=LLM_API_KEY,
        service=service,
        build_meta=build_meta,
        log_window=clean_window,
        error_class=error_class,
        deep=deep,
    )
    result["request_id"] = request_id

    # estimate cost: gemini-2.0-flash ~$0.075/1M input, $0.30/1M output
    tokens_in = result["tokens_used"].get("input", 0)
    tokens_out = result["tokens_used"].get("output", 0)
    cost = (tokens_in / 1_000_000 * 0.075) + (tokens_out / 1_000_000 * 0.30)
    add_spend(cost)

    mark_processed(job, build, request_id)
    return result


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("bbctl_rca.main:app", host="0.0.0.0", port=7070, reload=False)
