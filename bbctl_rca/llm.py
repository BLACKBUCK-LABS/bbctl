import json
import re
import google.generativeai as genai
from pathlib import Path
from . import mcp_tools
from . import jira
from . import source_trace
from . import github as gh
from . import runbook
from . import newrelic as nr
from . import canary_analyzer


# Class-specific runbook docs (in /opt/bbctl-rca/docops/). If file present,
# loaded into prompt when error_class matches. Keep doc short — it's prompted.
CLASS_DOCS = {
    "compliance": "JiraDetailsCompliance.md",
    "scm": "SCMTroubleshoot.md",
    "canary_fail": "StaggerProdPlusOneDeploy.md",
    "canary_script_error": "StaggerProdPlusOneDeploy.md",
    "aws_limit": "AwsLimitTroubleshoot.md",
    "parse_error": "ConfigJsonParseError.md",
    "health_check": "HealthCheckFailure.md",
}

# Regex to extract TG ARN + instance + region from the `healthy.sh` invocation
# line. Pipeline emits exactly:
#   + bash ./healthy.sh <tg-arn> <region> <instance-id> <env>
_HEALTHY_SH_RE = re.compile(
    r"bash \./healthy\.sh\s+"
    r"(arn:aws:elasticloadbalancing:[^:]+:\d+:targetgroup/([^/]+)/[a-f0-9]+)\s+"
    r"(\S+)\s+"
    r"(i-[0-9a-f]+)\s+"
    r"(\S+)"
)

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

    # if canary_script_error: load canary.py around the DEEPEST frame (actual crash)
    if error_class == "canary_script_error":
        # Traceback prints frames outer-to-inner. The LAST canary.py frame
        # is where the error actually occurred (deepest in stack). Outer
        # frames are just main() / dispatch.
        matches = re.findall(r'canary\.py", line (\d+), in', log_window)
        line_no = int(matches[-1]) if matches else 80
        snippet = mcp_tools.repo_read_file(
            "jenkins_pipeline", "resources/canary.py",
            max(1, line_no - 10), line_no + 10
        )
        parts.append(f"## canary.py:{line_no}±10 (deepest traceback frame — actual crash site)\n```python\n{snippet}\n```")
        parts.append(
            "## canary.script_error.context\n"
            "This is a SCRIPT CRASH in canary.py, NOT a service performance regression. "
            "The Jenkins build failed because canary.py exited non-zero — but the service being deployed "
            "may be perfectly fine. Common root causes:\n"
            "  1. NewRelic has no transactions for the configured appName in last 7 days (NRQL returns null)\n"
            "  2. NewRelic appName in config.json's new_relic_name doesn't match what the service actually reports\n"
            "  3. canary.py lacks None handling at the failing line\n"
            "Action focus: verify NewRelic appName has data; fix canary.py defensive coding."
        )

    # if canary_fail: pre-compute stage-level pass/fail + load info + slow tx
    if error_class == "canary_fail":
        # Prefer full raw_log stashed on build_meta for accurate stage parse
        bm_dict = build_meta or {}
        full_log = bm_dict.get("_raw_log") or log_window
        stage_analysis = canary_analyzer.analyze(full_log)
        if stage_analysis["stages"]:
            parts.append(
                f"## canary.stage_analysis\n```json\n"
                f"{json.dumps(stage_analysis, indent=2)}\n```"
            )

        groovy = mcp_tools.repo_read_file("jenkins_pipeline", "vars/canary.groovy", 1, 80)
        parts.append(f"## canary.groovy:1-80\n```groovy\n{groovy[:1500]}\n```")
        parts.append(
            "## canary.judge_logic (from resources/canary.py)\n"
            "Judge: Kayenta NetflixACAJudge-v1.0. Score 0-100. pass=80, marginal=80.\n"
            "FAIL means score < 80 — new build worse than baseline beyond `effectSize.allowedIncrease`:\n"
            "  - latency, Web transactions:   2.5x baseline allowed\n"
            "  - latency, Other (non-Web):    50x baseline allowed  (very lenient — failing this = catastrophic)\n"
            "  - latency, per-txn duration>10s: configured per-config (2.5x or 50x)\n"
            "  - latency, per-txn duration<=10s: 15x baseline allowed\n"
            "  - error-rate (any type):       1x baseline allowed  (no increase)\n"
            "Whole canary FAILs if ANY of the 7 configs fails (canary.py:545-548).\n"
            "The numeric score is in Kayenta API response only, NOT in the build log."
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

    # if health_check: parse `healthy.sh` invocation for TG/instance/region;
    # query NewRelic transactions in the deploy window (often shows the
    # service was deployed but never reported any transactions, i.e. it
    # never started or never bound the expected port).
    if error_class == "health_check":
        bm_dict = build_meta or {}
        full_log = bm_dict.get("_raw_log") or log_window
        # Pipeline runs healthy.sh multiple times (Prod+1 passes first, then
        # Deploy stage call fails). Use the LAST invocation — that's the one
        # whose probe loop the pipeline actually failed on.
        matches = _HEALTHY_SH_RE.findall(full_log)
        if matches:
            tg_arn, tg_name, region, instance_id, env = matches[-1]
            # Count failed iterations to give LLM a concrete duration signal.
            iter_match = re.search(
                r"Health Status for .* after (\d+) iterations:\s*unhealthy",
                full_log, re.IGNORECASE,
            )
            failed_iterations = int(iter_match.group(1)) if iter_match else None
            health_ctx = {
                "target_group_name": tg_name,
                "target_group_arn": tg_arn,
                "instance_id": instance_id,
                "region": region,
                "env": env,
            }
            if failed_iterations is not None:
                health_ctx["failed_iterations"] = failed_iterations
            parts.append(
                f"## health_check.target\n```json\n{json.dumps(health_ctx, indent=2)}\n```"
            )

        # Surface service log path + port from config.json so LLM can tell
        # operator EXACTLY where to look on the instance. Real-world config
        # uses different field names per service (this org uses `target_port`,
        # `filebeat_log_path`, `key_name`, `server_command` instead of the
        # canonical `health_check_port` / `log_path` / `service_port`).
        # Resolve canonical → actual; mark unresolved as NOT_IN_CONFIG so the
        # LLM SEES the absence rather than fabricating `<placeholder>` strings.
        if isinstance(svc, dict):
            def _first(*keys):
                for k in keys:
                    v = svc.get(k)
                    if v not in (None, "", [], {}):
                        return v
                return None

            log_path = _first("log_path", "filebeat_log_path")
            port = _first("service_port", "port", "app_port", "container_port", "target_port", "health_check_port")
            hc_path = _first("health_check_path")
            key_name = _first("key_name")
            server_cmd = _first("server_command")

            # Parse `-Dlog.dir=...` from the java startup command — for services
            # that lack `filebeat_log_path` but include a `-Dlog.dir` JVM arg.
            log_dir_hint = None
            if server_cmd:
                m = re.search(r"-Dlog\.dir=(\S+)", server_cmd)
                if m:
                    log_dir_hint = m.group(1).rstrip("/")

            resolved = {
                "log_path": log_path or "NOT_IN_CONFIG",
                "log_dir_hint_from_server_command": log_dir_hint or "NOT_IN_CONFIG",
                "port": port or "NOT_IN_CONFIG",
                "health_check_path": hc_path or "NOT_IN_CONFIG",
                "key_name": key_name or "NOT_IN_CONFIG",
                "pem_path_hint": f"/var/lib/jenkins/.ssh/{key_name}.pem" if key_name else "NOT_IN_CONFIG",
            }
            parts.append(
                f"## health_check.service_config\n```json\n{json.dumps(resolved, indent=2)}\n```"
            )

        # NewRelic transactions during deploy window — if the service is
        # registered with NR but reports zero transactions, that proves it
        # never accepted traffic. Same pattern as canary_fail block.
        bm = build_meta or {}
        ts = bm.get("timestamp")
        dur = bm.get("duration") or bm.get("estimatedDuration") or 0
        if ts and dur and isinstance(svc, dict):
            from datetime import datetime, timezone
            end_dt = datetime.fromtimestamp((ts + dur) / 1000, tz=timezone.utc)
            start_dt = datetime.fromtimestamp(ts / 1000, tz=timezone.utc)
            start_iso = start_dt.strftime("%Y-%m-%d %H:%M:%S")
            end_iso = end_dt.strftime("%Y-%m-%d %H:%M:%S")
            candidates = []
            nr_name = svc.get("new_relic_name")
            if nr_name:
                candidates.append(nr_name)
            for v in (service, service.replace("-", "_"), service.replace("_", "-")):
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

        parts.append(
            "## health_check.guide\n"
            "ALB target group probe stayed `unhealthy` for the full poll window. "
            "Pipeline declared deploy failed. Common root causes (check in order):\n"
            "  1. Service didn't start — check service log at `log_path` on the instance for crash/exception\n"
            "  2. Port mismatch — service listening on a different port than TG `health_check_port`\n"
            "  3. Health endpoint path wrong / returns non-2xx — verify `health_check_path` returns 200 via `curl` from inside instance\n"
            "  4. Security group blocks ALB → instance on the TG port\n"
            "  5. Slow boot vs threshold — service takes longer to come up than TG `healthy_threshold * interval`\n"
            "  6. Dependency unreachable — service starts but health endpoint depends on DB/Redis/Kafka which is down/blocked\n"
            "ORG ACCESS PATTERN: use `bbctl` (org-standard CLI) to reach the instance — NOT raw ssh.\n"
            "  `bbctl shell <instance_id>` opens an interactive shell.\n"
            "  `bbctl run <instance_id> -- '<cmd>'` runs a one-shot command (preferred for suggested_commands).\n"
            "  Substitute the real instance_id from `health_check.target`. Never emit `<instance-id>` placeholders.\n"
            "DO NOT cite SSH host-key warnings or NewRelic `Application X does not exist` as the cause — both are non-fatal upstream noise."
        )

    # Trace error strings → source code. Only include queries with hits.
    # For `unknown` class, run a wider sweep so the LLM has more candidate
    # origin points to self-classify from source evidence.
    deep_trace = (error_class == "unknown")
    traces = [t for t in source_trace.trace(log_window, deep=deep_trace) if t.get("hits")]
    if traces:
        lines = ["## source.trace"]
        hits_cap = 6 if deep_trace else 3
        for t in traces:
            lines.append(f"query: {t['query']}")
            lines.extend(t["hits"][:hits_cap])
        parts.append("\n".join(lines))

    # Class-specific runbook doc — load only the sections most relevant to
    # the current error class (intro + failure/remediation sections).
    doc_name = CLASS_DOCS.get(error_class)
    if doc_name:
        doc = mcp_tools.docs_get(doc_name)
        if doc and not doc.startswith("doc not found"):
            extract = runbook.extract_relevant(doc, error_class, budget_chars=6000)
            parts.append(f"## docs.{doc_name}\n{extract}")

    # Unknown class — give the LLM a catalog of available runbooks + class
    # taxonomy so it can self-classify by reading. Wider source.trace already
    # ran (deep=True). Include the first heading + first ~250 chars of each
    # docops/*.md so the LLM can spot which runbook matches.
    if error_class == "unknown":
        try:
            doc_names = mcp_tools.docs_list()
        except Exception:
            doc_names = []
        catalog_lines = ["## docs.catalog (read these to self-classify)"]
        for name in doc_names:
            body = mcp_tools.docs_get(name)
            if not body or body.startswith("doc not found"):
                continue
            # First non-blank line as a heading; first ~250 chars as preview.
            heading = next((ln.strip() for ln in body.splitlines() if ln.strip()), "")
            preview = body.replace("\n", " ").strip()[:250]
            catalog_lines.append(f"- **{name}** — {heading[:80]}")
            catalog_lines.append(f"    preview: {preview}…")
        if len(catalog_lines) > 1:
            parts.append("\n".join(catalog_lines))

        parts.append(
            "## unknown_class.guide\n"
            "The rule-based classifier could not match this failure to a known class. "
            "Before composing the RCA:\n"
            "  1. Read the `source.trace` hits above — they often point at the exact "
            "Groovy/Terraform line that emitted the error message.\n"
            "  2. Skim the `docs.catalog` previews above. If any docs entry's heading "
            "or preview matches the failure pattern in the log, fetch its remediation "
            "steps mentally and use them in your `suggested_fix`.\n"
            "  3. Pick the best-fit `error_class` from the enum (compliance, canary_fail, "
            "canary_script_error, health_check, aws_limit, parse_error, java_runtime, "
            "scm, network, dependency, health_check, ssm, timeout). If truly nothing "
            "fits, keep `error_class: \"unknown\"` and set `needs_deeper: true`.\n"
            "  4. In Finding, name the specific evidence line + the source.trace path "
            "that explains it. Do NOT speculate without evidence — better to say "
            "'cannot determine from log alone' and `needs_deeper: true`."
        )

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

    # Log what blocks made it into the tool context so we can audit retroactively
    block_titles = [ln.strip() for ln in tool_ctx.split("\n") if ln.startswith("## ")]
    print(f"[llm] tool_ctx blocks: {block_titles}", file=__import__('sys').stderr, flush=True)

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
