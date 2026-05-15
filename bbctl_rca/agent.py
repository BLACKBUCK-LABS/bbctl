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


MAX_TOOL_CALLS = 6           # iterations; each iter may issue multiple tool calls
COST_CAP_USD = 0.25
# Per-tool-result cap. Lower = less context-bloat per iteration. Older
# results are also elided (see TRIM_HISTORY_AFTER) so this is mainly the
# CURRENT iteration's read budget.
PER_TOOL_RESULT_CAP = 1500   # was 3000; agent rarely needs more than ~50 lines
# After this many iterations, elide older tool-result bodies (keep the
# tool-call shell intact so the LLM still sees what was asked). Cuts the
# replay weight that drives input-token cost.
TRIM_HISTORY_AFTER = 1       # was 2; tighter trim, only keep last iter full

# Per-model pricing in USD per 1M tokens. Used for cost cap enforcement
# AND audit-record cost reporting. Update when OpenAI changes rates.
# Unknown models fall through to gpt-4o pricing (conservative — over-
# bills slightly rather than under-bills, so cost cap stays effective).
_MODEL_PRICING = {
    "gpt-4o":        {"in":  2.50, "out": 10.00},
    "gpt-4o-mini":   {"in":  0.15, "out":  0.60},
    "gpt-4.1":       {"in":  2.00, "out":  8.00},
    "gpt-4.1-mini":  {"in":  0.40, "out":  1.60},
    "gpt-5":         {"in":  3.00, "out": 15.00},   # approximate — verify before prod use
    "gpt-5-mini":    {"in":  0.50, "out":  2.00},   # approximate — verify before prod use
    "o1":            {"in": 15.00, "out": 60.00},
    "o1-mini":       {"in":  1.10, "out":  4.40},
    "o3-mini":       {"in":  1.10, "out":  4.40},
}


def _pricing_for(model: str) -> tuple[float, float]:
    """(input_per_token, output_per_token) for the given model name.
    Falls back to gpt-4o pricing if unknown."""
    p = _MODEL_PRICING.get(model, _MODEL_PRICING["gpt-4o"])
    return p["in"] / 1_000_000, p["out"] / 1_000_000


# Default agent model. Override per-RCA via BBCTL_RCA_MODEL env var to
# A/B test different models without code change. e.g.:
#   sudo systemctl set-environment BBCTL_RCA_MODEL=gpt-5
#   sudo systemctl restart bbctl-rca
# Default = gpt-4.1: better reasoning + 1M context vs gpt-4o, similar
# price ($2/$8 vs $2.50/$10 per 1M tokens).
_DEFAULT_MODEL = os.environ.get("BBCTL_RCA_MODEL", "gpt-4.1")

# Back-compat: keep the old module-level constants pointing at the
# default-model pricing so any external import doesn't break. Active
# code in run_agent now reads pricing per-call via _pricing_for().
INPUT_USD_PER_TOKEN, OUTPUT_USD_PER_TOKEN = _pricing_for(_DEFAULT_MODEL)


def _log(msg: str) -> None:
    print(f"[agent] {msg}", file=sys.stderr, flush=True)


# Forced final-answer prompt. Used both for "tool budget exhausted" and
# "cost cap reached" paths. The explicit schema + "JSON object only, no
# markdown" guard rails are necessary because gpt-4o sometimes emits a
# markdown report ("### Summary\n...") when its prior tool call errored —
# the response_format=json_object constraint alone hasn't been enough.
_FORCE_FINAL_PROMPT = (
    "Stop calling tools. Emit your FINAL answer NOW as a single JSON object "
    "(NOT markdown, NOT ###headings — ONLY a JSON object that parses with "
    "json.loads). Schema:\n"
    "{\n"
    '  "summary": "string",\n'
    '  "failed_stage": "string",\n'
    '  "error_class": "compliance|canary_fail|canary_script_error|health_check|aws_limit|parse_error|java_runtime|scm|network|dependency|ssm|timeout|unknown",\n'
    '  "root_cause": "string with citations from files you read",\n'
    '  "evidence": [{"source": "jenkins_log|jira.tickets|<repo>/<file>:<line>", "snippet": "string", "verified": true}],\n'
    '  "suggested_fix": "string OR {Finding,Action,Verify}",\n'
    '  "suggested_commands": [{"cmd": "string", "tier": "safe|restricted", "rationale": "string"}],\n'
    '  "confidence": 0.0,\n'
    '  "needs_deeper": false\n'
    "}\n"
    "If a tool errored earlier, that's fine — use the context you already "
    "have (primer + earlier tool results) to compose the JSON.\n"
    "\n"
    "USE THE EVIDENCE YOU HAVE — but you MUST open at least one source file "
    "via `repo_read_file` before finalizing (see system-prompt cross-check "
    "rule). Examples:\n"
    "  • `MissingMethodException: No signature of method: X.call(...) is "
    "applicable for argument types: (...). Possible solutions: "
    "call(A,B,C)` — the log NAMES the fix, but you still MUST open the "
    "caller (mapped from WorkflowScript:<line> via get_jenkins_job_config) "
    "AND `vars/X.groovy` to confirm the signature. Cite BOTH file:line "
    "entries in evidence.\n"
    "  • Stack trace with file:line in a file you READ — cite "
    "`<repo>/<file>:<line>` exactly.\n"
    "  • `Health Status: unhealthy` + you read healthy.sh — cite poll-loop "
    "line and explain the timeout.\n"
    "\n"
    "OPERATOR-FACING LANGUAGE — banned terms in `summary` / `root_cause` / "
    "`suggested_fix`:\n"
    "  ✗ \"agent budget\", \"tool calls\", \"iterations\", \"implementation "
    "site not reached\", \"could not be reached within the tool budget\", "
    "\"my budget\", \"my reasoning\"\n"
    "  ✓ Write as if a senior SRE diagnosed it from the log — concrete, "
    "operator-actionable. Mention `vars/JiraDetails.groovy`, file paths, "
    "line numbers, real signatures from the log.\n"
    "\n"
    "ONLY use `needs_deeper: true` if BOTH (a) you did not read any "
    "relevant repo file AND (b) the log itself does not contain a clear "
    "exception type with a usable hint. If the log spells out the answer, "
    "`needs_deeper` is wrong — set it `false`.\n"
    "\n"
    "Output the JSON object only — no prose before or after."
)


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
    model: str | None = None,
) -> dict:
    """Run a function-calling agent until it emits final RCA JSON or hits caps.

    `initial_tool_ctx` is the same pre-computed context block used by the
    one-shot path (service.lookup, source.trace, docs.<class>.md, jira.tickets,
    etc.). We feed it to the agent as a primer so cheap classes still get
    instant grounding without burning tool calls.

    `model` defaults to the BBCTL_RCA_MODEL env var (or gpt-4.1). Cost cap
    uses per-model pricing so swapping to a cheaper / more expensive model
    just shifts the iteration ceiling at the dollar bound.
    """
    if model is None:
        model = _DEFAULT_MODEL
    in_per_tok, out_per_tok = _pricing_for(model)
    _log(f"model={model} input=${in_per_tok*1_000_000:.2f}/M output=${out_per_tok*1_000_000:.2f}/M")

    from openai import OpenAI
    client = OpenAI(api_key=api_key)

    system = _load_prompt("rca_agent_system.md")
    primer = _build_primer(job, build, service, error_class, build_meta,
                           log_window, initial_tool_ctx)
    # Concatenate primer into the system message so OpenAI's automatic prompt
    # caching can reuse the prefix across iterations (cached tokens billed at
    # ~50% rate). The user message stays short — just kicks off the trace.
    system_full = system + "\n\n" + primer

    messages = [
        {"role": "system", "content": system_full},
        {"role": "user", "content": (
            "Begin the trace. Use the primer above. Don't re-fetch what's "
            "already there. Cite repo evidence at IMPLEMENTATION lines."
        )},
    ]

    # Optional prompt dump for debugging.
    # Enable: sudo systemctl set-environment BBCTL_RCA_DEBUG_PROMPT=1
    # Reads:  cat /tmp/bbctl-rca-last-prompt.txt
    if os.environ.get("BBCTL_RCA_DEBUG_PROMPT"):
        try:
            with open("/tmp/bbctl-rca-last-prompt.txt", "w") as _f:
                _f.write("=== MODEL ===\n" + model + "\n\n")
                _f.write("=== MODE ===\nagent\n\n")
                _f.write("=== SYSTEM MESSAGE (includes primer) ===\n" + system_full + "\n\n")
                _f.write("=== INITIAL USER MESSAGE ===\n" + messages[1]["content"] + "\n\n")
                _f.write("=== TOOLS SCHEMA ===\n" + json.dumps(TOOLS, indent=2) + "\n")
        except Exception as _e:
            _log(f"prompt dump failed: {_e}")

    # Optional full-transcript dump (each iter's request, response, tool
    # calls, tool results) — for manager-grade audit / training data.
    # Enable: BBCTL_RCA_DEBUG_TRACE=1
    # Reads:  cat /tmp/bbctl-rca-last-trace.txt
    _trace_enabled = bool(os.environ.get("BBCTL_RCA_DEBUG_TRACE"))
    _trace_path = "/tmp/bbctl-rca-last-trace.txt"
    if _trace_enabled:
        try:
            with open(_trace_path, "w") as _f:
                _f.write(f"=== AGENT TRACE — job={job} build={build} service={service} model={model} ===\n\n")
                _f.write("=== INITIAL SYSTEM MESSAGE (truncated; full in /tmp/bbctl-rca-last-prompt.txt) ===\n")
                _f.write(system_full[:4000] + ("\n…[truncated]\n" if len(system_full) > 4000 else "\n"))
                _f.write("\n=== INITIAL USER MESSAGE ===\n")
                _f.write(messages[1]["content"] + "\n\n")
        except Exception as _e:
            _log(f"trace init failed: {_e}")
            _trace_enabled = False

    def _trace(label: str, body: str) -> None:
        if not _trace_enabled:
            return
        try:
            with open(_trace_path, "a") as _f:
                _f.write(f"\n--- {label} ---\n{body}\n")
        except Exception:
            pass

    def _fmt_request_payload(msgs: list, kwargs: dict) -> str:
        """Render the exact OpenAI request payload for trace logs.

        Truncates per-message content to keep file size sane — full
        unredacted prompt + tool schemas live in /tmp/bbctl-rca-last-prompt.txt.
        """
        PER_MSG_CHARS = 1500
        lines = []
        lines.append(f"model: {kwargs.get('model')}")
        lines.append(f"temperature: {kwargs.get('temperature')}")
        if "response_format" in kwargs:
            lines.append(f"response_format: {kwargs['response_format']}")
        if "tools" in kwargs:
            tool_names = [t.get("function", {}).get("name", "?") for t in kwargs["tools"]]
            lines.append(f"tools: {tool_names}")
        if "tool_choice" in kwargs:
            lines.append(f"tool_choice: {kwargs['tool_choice']}")
        lines.append(f"messages ({len(msgs)} total):")
        for i, m in enumerate(msgs):
            role = m.get("role", "?")
            content = m.get("content") or ""
            if len(content) > PER_MSG_CHARS:
                content = content[:PER_MSG_CHARS] + f"\n…[truncated, +{len(m.get('content',''))-PER_MSG_CHARS} chars]"
            extras = []
            if m.get("tool_calls"):
                extras.append(f"tool_calls={[(tc['function']['name'], tc['function']['arguments'][:200]) for tc in m['tool_calls']]}")
            if m.get("tool_call_id"):
                extras.append(f"tool_call_id={m['tool_call_id']}")
            if m.get("name"):
                extras.append(f"name={m['name']}")
            extra_str = (" " + " ".join(extras)) if extras else ""
            lines.append(f"  [{i}] role={role}{extra_str}")
            if content:
                lines.append(f"      content: {content}")
        return "\n".join(lines)

    ctx = {"jenkins_url": jenkins_url, "jenkins_auth": jenkins_auth}
    total_in = total_out = 0
    tool_call_count = 0
    final_text = None
    # Track every successful repo_read_file call so we can validate the
    # final evidence array against it (post-parse hallucination guard).
    # Key: "<repo>/<path>" — line range is ignored, any read counts.
    read_files: set[str] = set()
    # Dedup map: (tool_name, sorted_args_json) -> (iter, cached_result).
    # If the LLM repeats an identical call, return the cached result with
    # a hint to try a different approach. Saves disk I/O AND signals to
    # the LLM that it's wandering (which the wandering-avoidance rule in
    # the system prompt warns against).
    tool_call_cache: dict[tuple[str, str], tuple[int, str]] = {}

    for iteration in range(MAX_TOOL_CALLS + 1):
        cost_so_far = total_in * in_per_tok + total_out * out_per_tok
        if cost_so_far >= COST_CAP_USD:
            _log(f"cost cap hit at ${cost_so_far:.4f} — forcing final answer")
            messages.append({
                "role": "user",
                "content": _FORCE_FINAL_PROMPT,
            })
            _cap_kwargs = {
                "model": model, "messages": messages,
                "response_format": {"type": "json_object"}, "temperature": 0.1,
            }
            _trace("COST-CAP FORCED FINAL REQUEST",
                   _fmt_request_payload(messages, _cap_kwargs))
            response = client.chat.completions.create(**_cap_kwargs)
            total_in += response.usage.prompt_tokens
            total_out += response.usage.completion_tokens
            final_text = response.choices[0].message.content
            _trace("COST-CAP FORCED FINAL RESPONSE",
                   f"prompt_tokens={response.usage.prompt_tokens} "
                   f"completion_tokens={response.usage.completion_tokens}\n"
                   f"content={(final_text or '')[:1500]}")
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
                "content": _FORCE_FINAL_PROMPT,
            })
        else:
            kwargs["tools"] = TOOLS
            kwargs["tool_choice"] = "auto"

        _trace(f"ITER {iteration} REQUEST",
               f"force_final={force_final} cost_so_far=${cost_so_far:.4f} "
               f"messages_count={len(messages)} tokens_so_far={total_in}+{total_out}\n"
               + _fmt_request_payload(messages, kwargs))
        response = client.chat.completions.create(**kwargs)
        total_in += response.usage.prompt_tokens
        total_out += response.usage.completion_tokens
        msg = response.choices[0].message
        try:
            _raw_resp = json.dumps(response.model_dump(), indent=2, default=str)
        except Exception as _e:
            _raw_resp = f"[model_dump failed: {_e}]"
        _RESP_CAP = 12000
        _trace(
            f"ITER {iteration} RESPONSE",
            f"prompt_tokens={response.usage.prompt_tokens} "
            f"completion_tokens={response.usage.completion_tokens}\n"
            f"finish_reason={response.choices[0].finish_reason}\n"
            f"content={(msg.content or '')[:1500]}\n"
            f"tool_calls={[(tc.function.name, tc.function.arguments) for tc in (msg.tool_calls or [])]}\n"
            f"--- raw OpenAI response (model_dump, {len(_raw_resp)} chars) ---\n"
            f"{_raw_resp[:_RESP_CAP]}"
            + (f"\n…[truncated, +{len(_raw_resp) - _RESP_CAP} more chars]"
               if len(_raw_resp) > _RESP_CAP else ""),
        )

        if force_final or not msg.tool_calls:
            final_text = msg.content
            # Voluntary-bail rescue: when the LLM stops emitting tool_calls
            # mid-loop (it thinks it has enough info), this path was NOT
            # bound by response_format=json_object — so the LLM is free to
            # dump a markdown report ("### Summary\n...") which trips the
            # fallback stub. If that happens, do ONE retry with the JSON
            # constraint + force-final prompt. Adds ~$0.05 in worst case
            # but rescues the otherwise-wasted 6 tool calls.
            if not force_final and _parse_final_json(final_text) is None:
                _log("LLM bailed early with non-JSON content; "
                     "re-prompting with response_format=json_object")
                messages.append({"role": "user", "content": _FORCE_FINAL_PROMPT})
                _retry_kwargs = {
                    "model": model, "messages": messages,
                    "response_format": {"type": "json_object"},
                    "temperature": 0.1,
                }
                _trace("VOLUNTARY-BAIL RESCUE REQUEST",
                       _fmt_request_payload(messages, _retry_kwargs))
                retry = client.chat.completions.create(**_retry_kwargs)
                total_in += retry.usage.prompt_tokens
                total_out += retry.usage.completion_tokens
                final_text = retry.choices[0].message.content
                _trace("VOLUNTARY-BAIL RESCUE RESPONSE",
                       f"prompt_tokens={retry.usage.prompt_tokens} "
                       f"completion_tokens={retry.usage.completion_tokens}\n"
                       f"content={(final_text or '')[:1500]}")
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

            # Tool-call dedup: same (name, args) within this RCA → serve
            # cached result + hint to LLM to try a different approach.
            # Saves I/O and breaks the "repeat the same search 3 times"
            # wandering pattern.
            fingerprint = (tc.function.name, json.dumps(args, sort_keys=True))
            if fingerprint in tool_call_cache:
                prev_iter, prev_result = tool_call_cache[fingerprint]
                _log(f"  → DUP: same call ran in iter {prev_iter}; serving cached result")
                result = (
                    f"[DUP_CALL: this exact call was already executed in iter "
                    f"{prev_iter}. Result was:]\n{prev_result}\n"
                    f"[end of cached result — try a DIFFERENT query or read a "
                    f"DIFFERENT file; repeating the same call wastes the budget.]"
                )
            else:
                result = await _dispatch_tool(tc.function.name, args, ctx)
                # Cap each tool result so a runaway grep doesn't blow the window
                if len(result) > PER_TOOL_RESULT_CAP:
                    result = result[:PER_TOOL_RESULT_CAP] + "\n…[truncated]"
                tool_call_cache[fingerprint] = (iteration, result)

            # Track repo_read_file paths so the final evidence array can
            # be validated against actual reads. Only track on a successful
            # (non-error) read so failed paths don't get whitelisted.
            if tc.function.name == "repo_read_file":
                repo = args.get("repo")
                path = args.get("path")
                if repo and path and not result.startswith(("error:", "[error", "ERROR:")):
                    read_files.add(f"{repo}/{path}")

            _result_str = result if isinstance(result, str) else str(result)
            _DUMP_CAP = 8000
            _trace(
                f"ITER {iteration} TOOL #{tool_call_count} {tc.function.name}",
                f"args={json.dumps(args)}\n"
                f"result_len={len(_result_str)} chars\n"
                f"result=\n{_result_str[:_DUMP_CAP]}"
                + (f"\n…[truncated, +{len(_result_str) - _DUMP_CAP} more chars]"
                   if len(_result_str) > _DUMP_CAP else "")
                + ("\n[NOTE: tool returned empty string]" if not _result_str else ""),
            )
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": result,
            })

        # Trim history: elide tool-result bodies from older iterations to
        # cut the token-replay cost. Keep the tool-call shells so the LLM
        # still sees the question + the fact it got answered.
        _elide_old_tool_results(messages, current_iter=iteration,
                                keep_recent=TRIM_HISTORY_AFTER)

    # Final JSON parse — tolerate markdown code fences (LLMs sometimes wrap
    # despite response_format=json_object). Log the raw text on failure so
    # the actual model output is visible in journalctl for debugging.
    rca = _parse_final_json(final_text)
    if rca is None:
        _log("agent did not emit valid JSON; falling back to error stub")
        _log(f"raw final_text (first 800 chars): {(final_text or '')[:800]!r}")
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

    # Evidence validator: drop fabricated repo-path citations.
    # If evidence[i].source looks like `<repo>/<path>:<line>` but no
    # repo_read_file ever opened that file, the LLM hallucinated the cite.
    # Drop those entries so the operator doesn't follow them. Keep the
    # non-repo sources (jenkins_log, jira.tickets, build_meta) untouched.
    rca["evidence"] = _filter_fake_repo_evidence(rca.get("evidence", []), read_files)

    rca["tokens_used"] = {"input": total_in, "output": total_out}
    rca["agent_tool_calls"] = tool_call_count
    rca["files_read"] = sorted(read_files)
    _log(f"done. tool_calls={tool_call_count} tokens={total_in}+{total_out} read_files={len(read_files)}")
    _trace("FINAL OUTPUT",
           f"tool_calls={tool_call_count} tokens={total_in}+{total_out} "
           f"cost=${total_in*in_per_tok + total_out*out_per_tok:.4f} "
           f"files_read={sorted(read_files)}\n"
           f"final_text=\n{(final_text or '')[:3000]}")
    return rca


_REPO_PREFIXES = ("jenkins_pipeline/", "InfraComposer/")


def _filter_fake_repo_evidence(evidence: list, read_files: set[str]) -> list:
    """Drop evidence entries whose source is a repo path not actually read.

    A repo-path source looks like `jenkins_pipeline/vars/foo.groovy:42`.
    Strip the `:<line>` part; if `<repo>/<path>` is NOT in `read_files`,
    the LLM made it up — drop the entry. Non-repo sources
    (`jenkins_log`, `jira.tickets`, `build_meta`) pass through unchanged.
    """
    if not isinstance(evidence, list):
        return evidence
    kept = []
    for item in evidence:
        if not isinstance(item, dict):
            kept.append(item)
            continue
        src = (item.get("source") or "").strip()
        if not any(src.startswith(p) for p in _REPO_PREFIXES):
            kept.append(item)
            continue
        # Strip the :<line> suffix if present
        path_part = src.rsplit(":", 1)[0] if ":" in src else src
        if path_part in read_files:
            kept.append(item)
        else:
            _log(f"  evidence validator: dropped fake cite source={src!r} "
                 f"(not in read_files)")
    return kept


def _parse_final_json(text: str | None) -> dict | None:
    """Tolerantly parse the agent's final response.

    Handles three real-world shapes:
      1. Pure JSON object — `json.loads` directly.
      2. Markdown-wrapped JSON — ```json\n{...}\n``` or ```\n{...}\n```.
      3. Trailing/leading prose — find the largest `{...}` substring.
    """
    if not text:
        return None
    s = text.strip()
    # 1. Direct parse
    try:
        v = json.loads(s)
        if isinstance(v, dict):
            return v
    except json.JSONDecodeError:
        pass
    # 2. Strip code fences
    if s.startswith("```"):
        inner = s[3:].lstrip()
        # optional 'json' tag
        if inner.lower().startswith("json"):
            inner = inner[4:].lstrip()
        if inner.endswith("```"):
            inner = inner[:-3].rstrip()
        try:
            v = json.loads(inner)
            if isinstance(v, dict):
                return v
        except json.JSONDecodeError:
            pass
    # 3. Pull out first `{...}` block by brace matching
    first = s.find("{")
    last = s.rfind("}")
    if first != -1 and last != -1 and last > first:
        candidate = s[first:last + 1]
        try:
            v = json.loads(candidate)
            if isinstance(v, dict):
                return v
        except json.JSONDecodeError:
            pass
    return None


def _elide_old_tool_results(messages: list, *, current_iter: int, keep_recent: int) -> None:
    """Replace tool-result bodies from iterations older than (current - keep_recent)
    with a short placeholder. Preserves the assistant→tool message structure so
    OpenAI still threads the tool_call_id chain, just drops the heavy content.

    Each agent iteration appends 1 assistant message + N tool messages. We
    walk the message list, and for any `role==tool` whose position is older
    than the cutoff, swap its content for `[elided to save tokens — see
    earlier reasoning]`.

    Cheap heuristic: each iteration's tool messages are clustered after an
    assistant message. We count assistant messages backwards and elide any
    tool messages that belong to assistant turns older than the cutoff.
    """
    if current_iter < keep_recent:
        return
    # Walk backwards, counting assistant turns. Once we've passed
    # `keep_recent` assistant turns, elide subsequent (older) tool messages.
    assistant_seen = 0
    for i in range(len(messages) - 1, -1, -1):
        m = messages[i]
        role = m.get("role")
        if role == "assistant" and m.get("tool_calls"):
            assistant_seen += 1
            continue
        if role == "tool" and assistant_seen >= keep_recent:
            if not (m.get("content") or "").startswith("[elided"):
                m["content"] = "[elided to save tokens — see earlier reasoning]"


def _build_primer(
    job: str, build: int, service: str, error_class: str,
    build_meta: dict, log_window: str, initial_tool_ctx: str,
) -> str:
    """Compose the static primer block that becomes part of the system message.

    Structure (resolved-values up top so the LLM never has to hunt for them):
      1. Build context (job/build/service/stage)
      2. RESOLVED VALUES (instance, port, log_path, …) — USE VERBATIM
      3. Pre-fetched context blocks (service.lookup, source.trace, runbooks…)
      4. Log window (truncated)
    """
    detected = build_meta.get("detected_failed_stage", "—")
    parts = [
        "## Build context",
        f"- job: {job}",
        f"- build: {build}",
        f"- service: {service}",
        f"- classifier hint: {error_class}",
        f"- detected_failed_stage: {detected}",
        "",
        "## RESOLVED VALUES — substitute these VERBATIM in suggested_fix and commands",
        "Pulled from the pre-fetched context blocks below. NEVER write `<placeholder>`, `<log_path>`, `<port>`, etc.",
        _format_resolved_values(initial_tool_ctx),
        "",
        "## Pre-fetched context (do not re-fetch — already in scope)",
        initial_tool_ctx or "(no pre-fetched context)",
        "",
        "## Log window (sanitized)",
        "```",
        log_window[:30000],  # primer cap; full log otherwise re-replays heavy
        "```",
    ]
    return "\n".join(parts)


def _format_resolved_values(initial_tool_ctx: str) -> str:
    """Extract the resolved health_check.target / .service_config JSON blocks
    from the pre-fetched context and republish them at the top of the primer
    in a single, easy-to-spot section. Pure heuristic regex over the existing
    `## health_check.target` and `## health_check.service_config` markdown
    blocks emitted by `_build_tool_context` — no new fetches.
    """
    import re as _re
    out = []
    for header in ("health_check.target", "health_check.service_config"):
        m = _re.search(
            rf"## {_re.escape(header)}\s*\n```json\s*\n(.*?)\n```",
            initial_tool_ctx or "",
            flags=_re.DOTALL,
        )
        if m:
            out.append(f"### {header}\n```json\n{m.group(1)}\n```")
    if not out:
        return "(no resolved values block found — primer will rely on service.lookup directly)"
    return "\n\n".join(out)
