import json
import google.generativeai as genai
from pathlib import Path
from . import mcp_tools


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


def _build_tool_context(service: str, error_class: str) -> str:
    """Eagerly fetch key context before calling LLM."""
    parts = []

    # service config
    svc = mcp_tools.service_lookup(service)
    parts.append(f"## service.lookup({service})\n```json\n{json.dumps(svc, indent=2)}\n```")

    # if parse_error: read relevant groovy + config.json area
    if error_class == "parse_error":
        snippet = mcp_tools.repo_read_file("jenkins_pipeline", "vars/createGreenInfra.groovy", 330, 345)
        parts.append(f"## createGreenInfra.groovy:330-345\n```groovy\n{snippet}\n```")

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
    examples = _load_prompt("rca_examples.md")
    tool_ctx = _build_tool_context(service, error_class)

    prompt = f"""{system}

{examples}

---
## Build context
- job: {build_meta.get('fullDisplayName', '')}
- result: {build_meta.get('result', '')}
- error_class: {error_class}
- service: {service}

{tool_ctx}

## Log window (sanitized)
```
{log_window}
```

Return ONLY valid JSON matching this schema:
{json.dumps(RCA_SCHEMA, indent=2)}
"""

    response = model.generate_content(prompt)
    text = response.text.strip()

    # strip markdown fences if present
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
    examples = _load_prompt("rca_examples.md")
    tool_ctx = _build_tool_context(service, error_class)

    user_msg = f"""{examples}

---
## Build context
- job: {build_meta.get('fullDisplayName', '')}
- result: {build_meta.get('result', '')}
- error_class: {error_class}
- service: {service}

{tool_ctx}

## Log window (sanitized)
```
{log_window}
```

Return ONLY valid JSON matching this schema:
{json.dumps(RCA_SCHEMA, indent=2)}
"""

    response = client.chat.completions.create(
        model="gpt-4o-mini",
        messages=[
            {"role": "system", "content": system},
            {"role": "user", "content": user_msg},
        ],
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
