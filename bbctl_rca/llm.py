import json
import google.generativeai as genai
from pathlib import Path
from . import mcp_tools
from . import jira
from . import source_trace


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


async def _build_tool_context(service: str, error_class: str, log_window: str) -> str:
    """Eagerly fetch key context before calling LLM. Compact output."""
    parts = []

    # service config — slim fields only (see mcp_tools.service_lookup)
    svc = mcp_tools.service_lookup(service)
    parts.append(f"## service.lookup({service})\n```json\n{json.dumps(svc)}\n```")

    # if parse_error: read groovy snippet from known offending location
    if error_class == "parse_error":
        snippet = mcp_tools.repo_read_file("jenkins_pipeline", "vars/createGreenInfra.groovy", 330, 345)
        parts.append(f"## createGreenInfra.groovy:330-345\n```\n{snippet}\n```")

    # Trace error strings → source code. Only include queries with hits.
    traces = [t for t in source_trace.trace(log_window) if t.get("hits")]
    if traces:
        lines = ["## source.trace"]
        for t in traces:
            lines.append(f"query: {t['query']}")
            lines.extend(t["hits"][:3])  # cap hits per query
        parts.append("\n".join(lines))

    # Jira tickets — already slim from fetch_ticket
    ticket_keys = jira.extract_tickets(log_window)
    if ticket_keys:
        tickets = await jira.fetch_all(ticket_keys)
        parts.append(f"## jira.tickets\n```json\n{json.dumps(tickets)}\n```")

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
    parts.append(
        f"## Build context\n"
        f"- job: {build_meta.get('fullDisplayName', '')}\n"
        f"- result: {build_meta.get('result', '')}\n"
        f"- error_class: {error_class}\n"
        f"- service: {service}"
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
    tool_ctx = await _build_tool_context(service, error_class, log_window)
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
    tool_ctx = await _build_tool_context(service, error_class, log_window)
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
