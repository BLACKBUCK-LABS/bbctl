"""Agent-mode RCA: OpenAI function-calling loop with iteration + cost cap.

When the classifier picks a "deep" class (compliance / canary_* /
health_check / parse_error / unknown / scm) we delegate to this module
instead of the one-shot path in `llm.py`.

The agent gets a tool palette and works the failure backwards from the
Jenkins job config → entrypoint pipeline file → failed stage body →
implementation functions. It keeps going until it either identifies the
file+line that caused the error or hits the budget.
"""
import json
import os
import sys
import time
from pathlib import Path

from . import mcp_tools
from . import jenkins as jenkins_api


MAX_TOOL_CALLS = 8
COST_CAP_USD = 0.25
# gpt-4o pricing (must match main.py)
INPUT_USD_PER_TOKEN = 2.50 / 1_000_000
OUTPUT_USD_PER_TOKEN = 10.00 / 1_000_000


def _log(msg: str) -> None:
    print(f"[agent] {msg}", file=sys.stderr, flush=True)


def _load_prompt(name: str) -> str:
    p = Path(__file__).parent.parent / "prompts" / name
    return p.read_text() if p.exists() else ""


# ---------------------------------------------------------------------------
# Tool definitions exposed to the LLM (OpenAI function-calling schema)
# ---------------------------------------------------------------------------

TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "repo_read_file",
            "description": (
                "Read a slice of a file from one of the local repos "
                "(`jenkins_pipeline` or `InfraComposer`). Prefer narrow ranges "
                "(50-150 lines). Line numbers in the output are real file line "
                "numbers, suitable for evidence citations."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string", "enum": ["jenkins_pipeline", "InfraComposer"]},
                    "path": {"type": "string", "description": "Path inside the repo, e.g. 'vars/canary.groovy'"},
                    "start": {"type": "integer", "description": "1-based start line. 0 = whole file (capped)."},
                    "end": {"type": "integer", "description": "1-based inclusive end line. 0 = use start + 99."},
                },
                "required": ["repo", "path"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "repo_search",
            "description": (
                "ripgrep across a repo. Use this to locate strings from the "
                "log inside Groovy/Java/Terraform source. Returns up to 20 "
                "matching lines with file:line:content."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string", "enum": ["jenkins_pipeline", "InfraComposer"]},
                    "query": {"type": "string", "description": "Literal string to search for"},
                    "max_results": {"type": "integer", "default": 20},
                },
                "required": ["repo", "query"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "repo_list_dir",
            "description": (
                "List immediate children of a directory in a repo. Useful when "
                "you don't know the exact filename (e.g. exploring `vars/`)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string", "enum": ["jenkins_pipeline", "InfraComposer"]},
                    "path": {"type": "string", "default": ""},
                },
                "required": ["repo"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "repo_find_function",
            "description": (
                "Find where a Groovy / Java / Python function is DEFINED in a "
                "repo. Returns ripgrep-style hits (file:line) showing the "
                "definition site so you can then `repo_read_file` it."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string", "enum": ["jenkins_pipeline", "InfraComposer"]},
                    "name": {"type": "string"},
                },
                "required": ["repo", "name"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "repo_recent_commits",
            "description": (
                "Show the last N commits in a repo (short SHA, date, author, "
                "message). Use this when a previously-green pipeline starts "
                "failing — the cause is often a recent commit."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string", "enum": ["jenkins_pipeline", "InfraComposer"]},
                    "n": {"type": "integer", "default": 10},
                },
                "required": ["repo"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "get_jenkins_job_config",
            "description": (
                "Fetch the Jenkins job's config.xml and extract SCM repo URL, "
                "branch, and the pipeline scriptPath. ALWAYS call this first "
                "so you know which file to read as the entrypoint."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "job": {"type": "string"},
                },
                "required": ["job"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "service_lookup",
            "description": (
                "Look up the service's slim config from "
                "`jenkins_pipeline/resources/config.json` (target port, "
                "log_path, NewRelic name, etc.)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "service": {"type": "string"},
                },
                "required": ["service"],
            },
        },
    },
]


# ---------------------------------------------------------------------------
# Tool dispatch
# ---------------------------------------------------------------------------

async def _dispatch_tool(name: str, args: dict, ctx: dict) -> str:
    """Invoke the local helper for a tool the LLM asked to call.

    `ctx` carries per-RCA context (Jenkins creds, etc.) so we don't have to
    plumb them through every tool signature.
    """
    try:
        if name == "repo_read_file":
            return mcp_tools.repo_read_file(
                args["repo"], args["path"],
                args.get("start", 0), args.get("end", 0),
            )
        if name == "repo_search":
            return mcp_tools.repo_search(
                args["repo"], args["query"], args.get("max_results", 20),
            )
        if name == "repo_list_dir":
            out = mcp_tools.repo_list_dir(args["repo"], args.get("path", ""))
            return "\n".join(out)
        if name == "repo_find_function":
            return mcp_tools.repo_find_function(args["repo"], args["name"])
        if name == "repo_recent_commits":
            return mcp_tools.repo_recent_commits(args["repo"], args.get("n", 10))
        if name == "get_jenkins_job_config":
            cfg = await jenkins_api.get_job_config(
                args["job"], ctx["jenkins_url"], ctx["jenkins_auth"],
            )
            # Drop the giant raw_xml from the response shown to the LLM —
            # the structured fields are what it needs to pick a script path.
            cfg = {k: v for k, v in cfg.items() if k != "raw_xml"}
            return json.dumps(cfg, indent=2)
        if name == "service_lookup":
            return json.dumps(mcp_tools.service_lookup(args["service"]), indent=2)
    except Exception as e:
        return f"tool error: {type(e).__name__}: {e}"
    return f"unknown tool: {name}"


# ---------------------------------------------------------------------------
# Agent loop
# ---------------------------------------------------------------------------

async def run_agent(
    *,
    api_key: str,
    job: str,
    build: int,
    service: str,
    build_meta: dict,
    log_window: str,
    error_class: str,
    initial_tool_ctx: str,
    jenkins_url: str,
    jenkins_auth: tuple,
    model: str = "gpt-4o",
) -> dict:
    """Run a function-calling agent until it emits final RCA JSON or hits caps.

    `initial_tool_ctx` is the same pre-computed context block used by the
    one-shot path (service.lookup, source.trace, docs.<class>.md, jira.tickets,
    etc.). We feed it to the agent as a primer so cheap classes still get
    instant grounding without burning tool calls.
    """
    from openai import OpenAI
    client = OpenAI(api_key=api_key)

    system = _load_prompt("rca_agent_system.md")
    user_msg = _build_primer(job, build, service, error_class, build_meta,
                             log_window, initial_tool_ctx)

    messages = [
        {"role": "system", "content": system},
        {"role": "user", "content": user_msg},
    ]

    ctx = {"jenkins_url": jenkins_url, "jenkins_auth": jenkins_auth}
    total_in = total_out = 0
    tool_call_count = 0
    final_text = None

    for iteration in range(MAX_TOOL_CALLS + 1):
        cost_so_far = total_in * INPUT_USD_PER_TOKEN + total_out * OUTPUT_USD_PER_TOKEN
        if cost_so_far >= COST_CAP_USD:
            _log(f"cost cap hit at ${cost_so_far:.4f} — forcing final answer")
            messages.append({
                "role": "user",
                "content": "Cost cap reached. Emit final RCA JSON now using only the evidence you have.",
            })
            response = client.chat.completions.create(
                model=model, messages=messages,
                response_format={"type": "json_object"}, temperature=0.1,
            )
            total_in += response.usage.prompt_tokens
            total_out += response.usage.completion_tokens
            final_text = response.choices[0].message.content
            break

        # On the final iteration, force JSON-only output (no more tools)
        force_final = iteration == MAX_TOOL_CALLS
        kwargs = {
            "model": model,
            "messages": messages,
            "temperature": 0.1,
        }
        if force_final:
            kwargs["response_format"] = {"type": "json_object"}
            messages.append({
                "role": "user",
                "content": "Tool budget exhausted. Emit final RCA JSON now.",
            })
        else:
            kwargs["tools"] = TOOLS
            kwargs["tool_choice"] = "auto"

        response = client.chat.completions.create(**kwargs)
        total_in += response.usage.prompt_tokens
        total_out += response.usage.completion_tokens
        msg = response.choices[0].message

        if force_final or not msg.tool_calls:
            final_text = msg.content
            break

        # Append assistant message + execute each tool call
        messages.append({
            "role": "assistant",
            "content": msg.content,
            "tool_calls": [
                {"id": tc.id, "type": "function",
                 "function": {"name": tc.function.name, "arguments": tc.function.arguments}}
                for tc in msg.tool_calls
            ],
        })
        for tc in msg.tool_calls:
            tool_call_count += 1
            try:
                args = json.loads(tc.function.arguments or "{}")
            except json.JSONDecodeError:
                args = {}
            _log(f"iter {iteration} tool#{tool_call_count}: {tc.function.name}({list(args)})")
            result = await _dispatch_tool(tc.function.name, args, ctx)
            # Cap each tool result so a runaway grep doesn't blow the window
            if len(result) > 8000:
                result = result[:8000] + "\n…[truncated]"
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": result,
            })

    # Final JSON parse
    try:
        rca = json.loads(final_text)
    except (json.JSONDecodeError, TypeError):
        _log(f"agent did not emit valid JSON; falling back to error stub")
        rca = {
            "summary": "Agent failed to emit valid JSON.",
            "failed_stage": build_meta.get("detected_failed_stage", "—"),
            "error_class": error_class,
            "root_cause": "Agent loop did not produce a parseable RCA JSON.",
            "evidence": [],
            "suggested_fix": "Re-run with deep:true or inspect agent stderr logs.",
            "suggested_commands": [],
            "confidence": 0.0,
            "needs_deeper": True,
        }

    rca["tokens_used"] = {"input": total_in, "output": total_out}
    rca["agent_tool_calls"] = tool_call_count
    _log(f"done. tool_calls={tool_call_count} tokens={total_in}+{total_out}")
    return rca


def _build_primer(
    job: str, build: int, service: str, error_class: str,
    build_meta: dict, log_window: str, initial_tool_ctx: str,
) -> str:
    """Compose the user message handed to the agent on the first turn."""
    detected = build_meta.get("detected_failed_stage", "—")
    parts = [
        f"## Build context",
        f"- job: {job}",
        f"- build: {build}",
        f"- service: {service}",
        f"- classifier hint: {error_class}",
        f"- detected_failed_stage: {detected}",
        "",
        "## Pre-fetched context (do not re-fetch — these are already in scope)",
        initial_tool_ctx or "(no pre-fetched context)",
        "",
        "## Log window (sanitized)",
        "```",
        log_window,
        "```",
        "",
        "Begin tracing. Start by calling `get_jenkins_job_config(job)` unless "
        "the pre-fetched context already names the entrypoint script.",
    ]
    return "\n".join(parts)
