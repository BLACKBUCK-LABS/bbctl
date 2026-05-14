import hmac
import hashlib
import json
import os
import uuid

from fastapi import FastAPI, APIRouter, Request, HTTPException, Header
from fastapi.responses import JSONResponse, HTMLResponse
from jinja2 import Environment, FileSystemLoader, select_autoescape

from .models import WebhookPayload, RCARequest, RCAResponse
from .jenkins import get_console_log, get_build_meta
from .window import extract_window, extract_failed_stage
from .sanitize import sanitize
from .classifier import classify
from .llm import run_rca, build_initial_tool_ctx
from .agent import run_agent
from .git_fresh import ensure_fresh_many
from .cache import (
    is_duplicate, mark_processed, over_daily_cap, add_spend,
    get_rca, set_rca,
)
from .evidence import verify as verify_evidence
from .audit import record as audit_record, read_by_request_id
from .slack import post as slack_post
import subprocess
import yaml
from pathlib import Path

# Jinja2 environment for HTML report rendering. Autoescape ON for all .html
# templates so values from the audit JSON can't inject script tags.
_TEMPLATE_DIR = Path(__file__).parent / "templates"
_jinja = Environment(
    loader=FileSystemLoader(str(_TEMPLATE_DIR)),
    autoescape=select_autoescape(["html"]),
)

# Color hint per error_class for the badge in the HTML report.
_CLASS_COLORS = {
    "compliance":          "bg-amber-200 text-amber-900",
    "canary_fail":         "bg-red-200 text-red-900",
    "canary_script_error": "bg-violet-200 text-violet-900",
    "health_check":        "bg-rose-200 text-rose-900",
    "aws_limit":           "bg-orange-200 text-orange-900",
    "parse_error":         "bg-yellow-200 text-yellow-900",
    "java_runtime":        "bg-red-200 text-red-900",
    "scm":                 "bg-indigo-200 text-indigo-900",
    "network":             "bg-sky-200 text-sky-900",
    "dependency":          "bg-fuchsia-200 text-fuchsia-900",
    "ssm":                 "bg-cyan-200 text-cyan-900",
    "timeout":             "bg-amber-200 text-amber-900",
    "unknown":             "bg-slate-200 text-slate-700",
}

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


# NOTE on route order: FastAPI evaluates routes in registration order. The
# `.json` route MUST be registered before the catch-all HTML route, otherwise
# the HTML route's `{request_id}` would greedily match `<uuid>.json` (with
# `.json` ending up as part of request_id), failing the uuid regex inside
# `read_by_request_id` and returning a misleading 404.
@router.get("/v1/report/{request_id}.json")
async def rca_report_json(request_id: str):
    """Raw JSON view of the audit record — for debugging / scripts."""
    audit = read_by_request_id(request_id)
    if not audit:
        raise HTTPException(status_code=404, detail="report not found")
    return audit


@router.get("/v1/report/{request_id}", response_class=HTMLResponse)
async def rca_report(request_id: str):
    """Render a stored RCA result as an HTML page.

    Looks up the audit record by request_id (uuid). The audit record is
    written by `audit_record(...)` after every RCA run and lives at
    /var/log/bbctl-rca/<uuid>.json. The HTML view is the shareable canonical
    surface — same URL appears in Jenkins console, Jenkins build description,
    Slack alerts, and VictorOps details.
    """
    audit = read_by_request_id(request_id)
    if not audit:
        raise HTTPException(status_code=404, detail="report not found")
    return _render_report(audit)


def _render_report(audit: dict) -> str:
    """Build template vars from the audit record and render the HTML page."""
    rca = audit.get("rca") or {}
    fix = rca.get("suggested_fix")
    fix_is_map = isinstance(fix, dict)
    fix_items = list(fix.items()) if fix_is_map else []

    job = audit.get("job", "")
    build = audit.get("build", "")
    build_url = audit.get("build_url") or _guess_build_url(job, build)

    tmpl = _jinja.get_template("rca_report.html")
    return tmpl.render(
        request_id=audit.get("request_id", ""),
        recorded_at=audit.get("recorded_at", ""),
        job=job,
        build=build,
        build_url=build_url,
        service=audit.get("service", ""),
        error_class=rca.get("error_class") or audit.get("error_class") or "unknown",
        class_color=_CLASS_COLORS.get(rca.get("error_class") or audit.get("error_class"), _CLASS_COLORS["unknown"]),
        failed_stage=rca.get("failed_stage", "—"),
        confidence=rca.get("confidence", "—"),
        needs_deeper=bool(rca.get("needs_deeper")),
        cost_usd=audit.get("cost_usd") or rca.get("cost_usd") or 0,
        tokens_in=(rca.get("tokens_used") or {}).get("input", 0),
        tokens_out=(rca.get("tokens_used") or {}).get("output", 0),
        summary=rca.get("summary", "—"),
        root_cause=rca.get("root_cause", "—"),
        suggested_fix=fix if not fix_is_map else "",
        fix_is_map=fix_is_map,
        fix_items=fix_items,
        suggested_commands=rca.get("suggested_commands", []),
        evidence=rca.get("evidence", []),
        provider=audit.get("provider", "—"),
        redactions=", ".join(audit.get("redactions") or []) or None,
        log_window_chars=audit.get("log_window_chars", 0),
    )


def _guess_build_url(job: str, build) -> str:
    """Best-effort Jenkins URL when the audit record didn't capture build_url."""
    base = JENKINS_URL.rstrip("/")
    if not job or build in (None, ""):
        return base
    # Jenkins URL-encodes path segments but spaces become %20 in the job name.
    safe_job = str(job).replace(" ", "%20")
    return f"{base}/job/{safe_job}/{build}/"


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

    # Per-RCA freshness pull (hybrid model). Cheap shallow fetch on both
    # repos so the agent / tool-context sees the latest commit. Cron at
    # /etc/cron.d/bbctl-rca-sync is a backstop; this is the fast path.
    freshness = ensure_fresh_many([
        ("jenkins_pipeline", None),
        ("InfraComposer", None),
    ])

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
    # Also stash raw_log on build_meta for analyzers that need full log
    # (canary stage parser; health_check `healthy.sh` line parser) — both
    # need data that the filtered window often drops.
    if error_class in ("canary_fail", "health_check"):
        if not isinstance(build_meta, dict):
            build_meta = dict(build_meta)
        build_meta["_raw_log"] = raw_log

    # Classes worth the agent's deeper trace through the actual source code.
    # Cheap one-shot stays good enough for the rest (timeout, ssm, network,
    # dependency, java_runtime when stack trace is self-explanatory).
    # compliance is excluded: it's a Jira-field-missing problem, not a code-trace
    # problem — primer already carries jira.tickets + runbook + mode 1-5 guidance,
    # so the agent has nothing to trace and tends to drift into prose output.
    AGENT_CLASSES = {
        "canary_fail", "canary_script_error",
        "health_check", "parse_error", "scm", "unknown",
    }

    if LLM_PROVIDER == "openai" and error_class in AGENT_CLASSES:
        # Run the agent. It still wants the same pre-computed tool context
        # as a primer so it doesn't burn calls re-fetching cheap things.
        initial_ctx = await build_initial_tool_ctx(
            service=service, error_class=error_class,
            log_window=clean_window, build_meta=build_meta,
        )
        result = await run_agent(
            api_key=LLM_API_KEY,
            job=job, build=build, service=service,
            build_meta=build_meta,
            log_window=clean_window,
            error_class=error_class,
            initial_tool_ctx=initial_ctx,
            jenkins_url=JENKINS_URL, jenkins_auth=JENKINS_AUTH,
        )
    else:
        result = await run_rca(
            LLM_PROVIDER,
            api_key=LLM_API_KEY,
            service=service,
            build_meta=build_meta,
            log_window=clean_window,
            error_class=error_class,
            deep=deep,
        )

    # Stash freshness info on the result so it surfaces in the audit/report
    result["repos_freshness"] = freshness
    # Strip raw_log from build_meta after LLM call so it doesn't leak into
    # audit log / response (it's massive).
    if isinstance(build_meta, dict) and "_raw_log" in build_meta:
        build_meta = {k: v for k, v in build_meta.items() if k != "_raw_log"}
    result["request_id"] = request_id

    # cost estimate by provider
    tokens_in = result["tokens_used"].get("input", 0)
    tokens_out = result["tokens_used"].get("output", 0)
    if LLM_PROVIDER == "openai":
        # gpt-4o: $2.50/1M input, $10.00/1M output
        cost = (tokens_in / 1_000_000 * 2.50) + (tokens_out / 1_000_000 * 10.00)
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
        "build_url": build_meta.get("url") if isinstance(build_meta, dict) else None,
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
