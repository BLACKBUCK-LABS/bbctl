import json
import google.generativeai as genai
from pathlib import Path
from . import mcp_tools
from . import jira
from . import source_trace
from . import github as gh
from . import runbook
from . import newrelic as nr


# Class-specific runbook docs (in /opt/bbctl-rca/docops/). If file present,
# loaded into prompt when error_class matches. Keep doc short — it's prompted.
CLASS_DOCS = {
    "compliance": "JiraDetailsCompliance.md",
    "scm": "SCMTroubleshoot.md",
    "canary_fail": "CanaryRollback.md",
    "aws_limit": "AwsLimitTroubleshoot.md",
    "parse_error": "ConfigJsonParseError.md",
}

# Classes for which we fetch git commit metadata from GitHub
GIT_CLASSES = {"compliance", "scm"}


SONNET = "claude-sonnet-4-6"
OPUS = "claude-opus-4-7"

RCA_SCHEMA = {
    "summary": "string",
    "failed_stage": "string",
    "error_class": "parse_error|java_runtime|ssm|scm|network|dependency|health_check|canary_fail|timeout|unknown",
    "root_cause": "string with file:line citations",
    "evidence": [{"source": "string", "snippet": "string"}],
    "suggested_fix": "string",
    "suggested_commands": [{"cmd": "string", "tier": "safe|restricted", "rationale": "string"}],
    "confidence": 0.0,
    "needs_deeper": False,
    "tokens_used": {"input": 0, "output": 0}
}


def _load_prompt(name: str) -> str:
    p = Path(__file__).parent.parent / "prompts" / name
    return p.read_text() if p.exists() else ""


async def _build_tool_context(service: str, error_class: str, log_window: str, build_meta: dict | None = None) -> str:
    """Eagerly fetch key context before calling LLM. Compact output."""
    parts = []

    # service config — slim fields only (see mcp_tools.service_lookup)
    svc = mcp_tools.service_lookup(service)
    parts.append(f"## service.lookup({service})\n```json\n{json.dumps(svc)}\n```")

    # if parse_error: read groovy snippet from known offending location
    if error_class == "parse_error":
        snippet = mcp_tools.repo_read_file("jenkins_pipeline", "vars/createGreenInfra.groovy", 330, 345)
        parts.append(f"## createGreenInfra.groovy:330-345\n```\n{snippet}\n```")

    # if canary_fail: load canary.groovy + threshold info + slow tx from NewRelic
    if error_class == "canary_fail":
        groovy = mcp_tools.repo_read_file("jenkins_pipeline", "vars/canary.groovy", 1, 80)
        parts.append(f"## canary.groovy:1-80\n```groovy\n{groovy[:1500]}\n```")
        parts.append(
            "## canary.thresholds\n"
            "Kayenta scores canary 0-100. Per resources/canary.py: pass=80, marginal=80. "
            "FAIL = score < 80 → new build's metrics regressed vs baseline beyond tolerance."
        )

        # Window from Jenkins build_meta timestamps (build start/end approximates
        # canary window; canary typically runs in last 30-60 min of build).
        bm = build_meta or {}
        ts = bm.get("timestamp")
        dur = bm.get("duration") or bm.get("estimatedDuration") or 0
        if ts and dur:
            from datetime import datetime, timezone
            end_dt = datetime.fromtimestamp((ts + dur) / 1000, tz=timezone.utc)
            # Canary runs in last ~30 min before failure — use that as window
            start_dt = datetime.fromtimestamp(max(ts, ts + dur - 30 * 60 * 1000) / 1000, tz=timezone.utc)
            start_iso = start_dt.strftime("%Y-%m-%d %H:%M:%S")
            end_iso = end_dt.strftime("%Y-%m-%d %H:%M:%S")
            # NewRelic appName often != service name (e.g. config has
            # new_relic_name="FMS - GPS" for service "prod-gps"). Try the
            # explicit field first, then heuristic variants.
            candidates = []
            nr_name = svc.get("new_relic_name") if isinstance(svc, dict) else None
            if nr_name:
                candidates.append(nr_name)
            for v in (service, service.replace("-", "_"), service.replace("_", "-"),
                      service.replace("prod-", "")):
                if v not in candidates:
                    candidates.append(v)
            for app in candidates:
                slow = await nr.slow_transactions(app, start_iso, end_iso, limit=5)
                if slow:
                    parts.append(
                        f"## newrelic.slow_transactions ({app}, {start_iso} → {end_iso} UTC)\n"
                        f"```json\n{json.dumps(slow, indent=2)}\n```"
                    )
                    break

    # Trace error strings → source code. Only include queries with hits.
    traces = [t for t in source_trace.trace(log_window) if t.get("hits")]
    if traces:
        lines = ["## source.trace"]
        for t in traces:
            lines.append(f"query: {t['query']}")
            lines.extend(t["hits"][:3])  # cap hits per query
        parts.append("\n".join(lines))

    # Class-specific runbook doc — load only the sections most relevant to
    # the current error class (intro + failure/remediation sections).
    doc_name = CLASS_DOCS.get(error_class)
    if doc_name:
        doc = mcp_tools.docs_get(doc_name)
        if doc and not doc.startswith("doc not found"):
            extract = runbook.extract_relevant(doc, error_class, budget_chars=6000)
            parts.append(f"## docs.{doc_name}\n{extract}")

    # Jira tickets — already slim from fetch_ticket
    ticket_keys = jira.extract_tickets(log_window)
    if ticket_keys:
        tickets = await jira.fetch_all(ticket_keys)
        parts.append(f"## jira.tickets\n```json\n{json.dumps(tickets)}\n```")

    # Git commits — for compliance/scm classes, fetch commit metadata from
    # GitHub for any SHA-looking strings in the log (helps RCA understand
    # what changed between signed-off and resolved commits).
    if error_class in GIT_CLASSES:
        commits_info = await gh.fetch_commits_from_log(log_window, service)
        if commits_info:
            parts.append(f"## github.commits\n```json\n{json.dumps(commits_info)}\n```")

    return '\n\n'.join(parts)


def _build_user_msg(
    build_meta: dict, service: str, error_class: str, tool_ctx: str,
    log_window: str, include_examples: bool,
) -> str:
    parts = []
    if include_examples:
        ex = _load_prompt("rca_examples.md")
        if ex:
            parts.append(ex)
    stage_line = (
        f"\n- detected_failed_stage: {build_meta['detected_failed_stage']}"
        if build_meta.get("detected_failed_stage") else ""
    )
    parts.append(
        f"## Build context\n"
        f"- job: {build_meta.get('fullDisplayName', '')}\n"
        f"- result: {build_meta.get('result', '')}\n"
        f"- error_class: {error_class}\n"
        f"- service: {service}{stage_line}"
    )
    parts.append(tool_ctx)
    parts.append(f"## Log window (sanitized)\n```\n{log_window}\n```")
    parts.append(
        "Return ONLY valid JSON. Required keys: summary, failed_stage, "
        "error_class, root_cause, evidence[{source,snippet}], suggested_fix, "
        "suggested_commands[{cmd,tier,rationale}], confidence (0-1), needs_deeper."
    )
    return '\n\n'.join(parts)


async def run_rca_gemini(
    api_key: str,
    service: str,
    build_meta: dict,
    log_window: str,
    error_class: str,
    deep: bool = False,
) -> dict:
    genai.configure(api_key=api_key)
    model = genai.GenerativeModel("gemini-2.0-flash")

    system = _load_prompt("rca_system.md")
    tool_ctx = await _build_tool_context(service, error_class, log_window, build_meta)
    include_examples = error_class == "unknown" or deep

    user_msg = _build_user_msg(build_meta, service, error_class, tool_ctx, log_window, include_examples)
    prompt = f"{system}\n\n{user_msg}"

    response = model.generate_content(prompt)
    text = response.text.strip()
    if text.startswith("```"):
        text = text.split("```")[1]
        if text.startswith("json"):
            text = text[4:]

    result = json.loads(text)
    result["tokens_used"] = {
        "input": response.usage_metadata.prompt_token_count if response.usage_metadata else 0,
        "output": response.usage_metadata.candidates_token_count if response.usage_metadata else 0,
    }
    return result


async def run_rca_openai(
    api_key: str,
    service: str,
    build_meta: dict,
    log_window: str,
    error_class: str,
    deep: bool = False,
) -> dict:
    from openai import OpenAI
    client = OpenAI(api_key=api_key)

    system = _load_prompt("rca_system.md")
    tool_ctx = await _build_tool_context(service, error_class, log_window, build_meta)
    include_examples = error_class == "unknown" or deep

    user_msg = _build_user_msg(build_meta, service, error_class, tool_ctx, log_window, include_examples)

    response = client.chat.completions.create(
        model="gpt-4o-mini",
        messages=[
            {"role": "system", "content": system},
            {"role": "user", "content": user_msg},
        ],
        # JSON mode means model is constrained to emit valid JSON; schema is
        # documented in system prompt + final instruction. Saves ~150 tk vs
        # dumping the full schema struct in prompt.
        response_format={"type": "json_object"},
        temperature=0.1,
    )
    text = response.choices[0].message.content.strip()
    result = json.loads(text)
    result["tokens_used"] = {
        "input": response.usage.prompt_tokens,
        "output": response.usage.completion_tokens,
    }
    return result


LLM_DISPATCH = {
    "gemini": run_rca_gemini,
    "openai": run_rca_openai,
}


async def run_rca(provider: str, **kwargs) -> dict:
    fn = LLM_DISPATCH.get(provider, run_rca_gemini)
    return await fn(**kwargs)
