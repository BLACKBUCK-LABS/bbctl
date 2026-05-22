"""Full-flow LangGraph orchestrator (Phase 8 — observability spike).

Wraps the existing imperative RCA flow as a StateGraph WITHOUT
rewriting any function bodies. Each node is a 5-15 line wrapper that
calls the existing function and threads state. Production agent loop,
validator gates, RAG, etc. remain untouched.

What this buys (env-gated, opt-in via `BBCTL_RCA_USE_FULL_GRAPH=1`):

1. **LangSmith trace** — set `LANGSMITH_API_KEY` + `LANGSMITH_TRACING=
   true` (LangSmith auto-detects) → every node call appears in the
   LangSmith UI with input/output/timing. Replaces journalctl grep.

2. **PG checkpointer** — set `BBCTL_RCA_GRAPH_CHECKPOINT=1` → graph
   state persists to the same Postgres we already run. Service
   restart mid-RCA → resume from last completed node, no re-cost.
   Falls back to in-memory MemorySaver when env not set.

3. **Streaming events** — `astream_rca_via_graph` async generator
   yields node-start/node-end events for SSE to the UI. Live
   progress instead of curl hanging 30-60s blind.

4. **Visual graph render** — `python -m bbctl_rca.full_graph mermaid`
   prints the mermaid diagram of the flow for inclusion in docs.

What this does NOT change:
- Same final RCA JSON (byte-identical to imperative path)
- Same cost (one extra orchestration layer, negligible overhead)
- Same failure_signals
- Same RAG, prompt, agent loop, gates.py, validators

When toggle is OFF (default), full_graph isn't imported at all.
Lazy-import via `bbctl_rca.main:_run_rca` keeps zero-impact on the
legacy path.
"""
from __future__ import annotations

import json
import os
import uuid
from typing import Any, AsyncIterator, Optional

try:
    from langgraph.graph import StateGraph, END
    from typing_extensions import TypedDict
    _HAS_LANGGRAPH = True
except Exception:
    StateGraph = None  # type: ignore
    END = "__end__"
    TypedDict = dict  # type: ignore
    _HAS_LANGGRAPH = False

# PG checkpointer is a separate package; lazy-import so the spike still
# runs (with in-memory state) on hosts without it installed.
try:
    from langgraph.checkpoint.postgres import PostgresSaver
    _HAS_PG_SAVER = True
except Exception:
    PostgresSaver = None  # type: ignore
    _HAS_PG_SAVER = False

try:
    from langgraph.checkpoint.memory import MemorySaver
    _HAS_MEM_SAVER = True
except Exception:
    MemorySaver = None  # type: ignore
    _HAS_MEM_SAVER = False


def _log(msg: str) -> None:
    import sys
    print(f"[full_graph] {msg}", file=sys.stderr, flush=True)


# ─── State schema ──────────────────────────────────────────────────────

if _HAS_LANGGRAPH:
    class RcaGraphState(TypedDict, total=False):
        """State threaded through every node.

        Inputs (set at graph entry):
          job, build, service, deep, request_id

        Computed (set by individual nodes):
          raw_log         — full console text
          build_meta      — Jenkins API metadata + detected_failed_stage
          stage_errors    — workflow REST API per-stage errors
          log_window      — sliced + sanitized log
          error_class     — classifier output
          freshness       — repos pull status
          use_agent_path  — True if class in AGENT_CLASSES
          rca             — final RCA dict from agent / one-shot
          failure_signals — accumulated across all stages
        """
        job:             str
        build:           int
        service:         str
        deep:            bool
        request_id:      str
        raw_log:         str
        build_meta:      dict
        stage_errors:    list
        log_window:      str
        error_class:     str
        freshness:       list
        use_agent_path:  bool
        rca:             dict
        failure_signals: list


# ─── Node wrappers ─────────────────────────────────────────────────────
# Each node is a thin wrapper around an existing function. Nothing in
# the wrapped functions changes; we just thread state in/out.

async def _node_fetch_log(state: dict) -> dict:
    """Fetch Jenkins console log + build_meta + stage_errors. Wraps
    jenkins.get_console_log + get_build_meta + get_stage_errors."""
    from . import jenkins as _jenkins
    from .main import JENKINS_URL, JENKINS_AUTH
    raw_log = await _jenkins.get_console_log(
        state["job"], state["build"], JENKINS_URL, JENKINS_AUTH)
    build_meta = await _jenkins.get_build_meta(
        state["job"], state["build"], JENKINS_URL, JENKINS_AUTH)
    stage_errors = await _jenkins.get_stage_errors(
        state["job"], state["build"], JENKINS_URL, JENKINS_AUTH)
    return {
        "raw_log":      raw_log,
        "build_meta":   build_meta,
        "stage_errors": stage_errors,
    }


def _node_classify(state: dict) -> dict:
    """Sanitize + slice log window + regex classify. Wraps
    window.extract_window + window.extract_failed_stage +
    sanitize.sanitize + classifier.classify."""
    from .window import extract_window, extract_failed_stage
    from .sanitize import sanitize
    from .classifier import classify

    raw_log      = state.get("raw_log", "")
    build_meta   = dict(state.get("build_meta", {}))
    stage_errors = state.get("stage_errors", []) or []

    window = extract_window(raw_log, deep=bool(state.get("deep")))
    if stage_errors:
        err_block_lines = ["=== Failed stages (from Jenkins workflow API) ==="]
        for se in stage_errors:
            err_block_lines.append(
                f"Stage '{se['name']}' status={se['status']}"
            )
            if se.get("error_message"):
                err_block_lines.append(se["error_message"])
        window = "\n".join(err_block_lines) + "\n\n" + window
    clean_window, _redactions = sanitize(window)
    error_class = classify(clean_window)

    detected_stage = extract_failed_stage(raw_log)
    build_meta["job"]   = state["job"]
    build_meta["build"] = state["build"]
    if detected_stage:
        build_meta["detected_failed_stage"] = detected_stage
    if error_class in ("canary_fail", "health_check"):
        build_meta["_raw_log"] = raw_log

    # AGENT_CLASSES set lives in main.py — duplicating the names here
    # would cause drift. Import lazily.
    from .main import AGENT_CLASSES_PATH
    return {
        "log_window":     clean_window,
        "error_class":    error_class,
        "build_meta":     build_meta,
        "use_agent_path": error_class in AGENT_CLASSES_PATH,
    }


def _node_route(state: dict) -> str:
    """Conditional edge: agent vs one-shot. Returns the next node name."""
    if state.get("use_agent_path"):
        return "agent_iter"
    return "oneshot_iter"


async def _node_agent_iter(state: dict) -> dict:
    """Run the OpenAI function-calling agent loop. Wraps
    agent.run_agent + the pre-computed tool context primer."""
    from . import agent, llm
    from .main import JENKINS_URL, JENKINS_AUTH, LLM_API_KEY, _DEFAULT_MODEL

    initial_ctx = await llm.build_initial_tool_ctx(
        service=state["service"],
        error_class=state["error_class"],
        log_window=state["log_window"],
        build_meta=state.get("build_meta"),
    )
    rca = await agent.run_agent(
        api_key=LLM_API_KEY,
        job=state["job"],
        build=state["build"],
        service=state["service"],
        build_meta=state.get("build_meta") or {},
        log_window=state.get("log_window") or "",
        error_class=state.get("error_class") or "unknown",
        initial_tool_ctx=initial_ctx,
        jenkins_url=JENKINS_URL,
        jenkins_auth=JENKINS_AUTH,
        model=_DEFAULT_MODEL,
    )
    rca["request_id"] = state.get("request_id") or rca.get("request_id")
    return {
        "rca":             rca,
        "failure_signals": rca.get("failure_signals", []) or [],
    }


async def _node_oneshot_iter(state: dict) -> dict:
    """Run the legacy one-shot (no tools) path for `unknown` + classes
    that don't benefit from the agent's deep code-trace."""
    from . import llm
    from .main import LLM_PROVIDER, LLM_API_KEY
    rca = await llm.run_rca(
        provider=LLM_PROVIDER,
        api_key=LLM_API_KEY,
        service=state["service"],
        build_meta=state.get("build_meta") or {},
        log_window=state.get("log_window") or "",
        error_class=state.get("error_class") or "unknown",
        deep=bool(state.get("deep")),
    )
    rca["request_id"] = state.get("request_id") or rca.get("request_id")
    return {
        "rca":             rca,
        "failure_signals": rca.get("failure_signals", []) or [],
    }


def _node_persist(state: dict) -> dict:
    """Persist audit JSON + outcome row + trace. Wraps
    audit.record + outcome_log.log."""
    from . import audit, outcome_log
    rca = state.get("rca") or {}
    payload = {
        "request_id":  state.get("request_id"),
        "job":         state.get("job"),
        "build":       state.get("build"),
        "service":     state.get("service"),
        "error_class": state.get("error_class"),
        "rca":         rca,
    }
    try:
        audit.record(payload)
    except Exception as e:
        _log(f"audit.record failed (non-fatal): {e}")
    try:
        outcome_log.log(
            job=state.get("job") or "?",
            build=int(state.get("build") or 0),
            service=state.get("service"),
            model=rca.get("model_used"),
            rca=rca,
            iters=rca.get("agent_iters", 0),
            tool_calls=rca.get("agent_tool_calls", 0),
            tokens_in=(rca.get("tokens_used") or {}).get("input", 0),
            tokens_out=(rca.get("tokens_used") or {}).get("output", 0),
            cost_usd=rca.get("cost_usd", 0.0),
            files_read=rca.get("files_read") or [],
            aws_apis=[],
            runbooks=[],
            failure_signals=rca.get("failure_signals") or [],
            trace_path=None,
        )
    except Exception as e:
        _log(f"outcome_log.log failed (non-fatal): {e}")
    return {}


# ─── Graph construction ────────────────────────────────────────────────

def build_full_graph(*, checkpointer: Optional[Any] = None) -> Optional[Any]:
    """Build the full-flow StateGraph. Returns the compiled graph or
    None when langgraph isn't installed."""
    if not _HAS_LANGGRAPH:
        return None
    g = StateGraph(RcaGraphState)
    g.add_node("fetch_log",     _node_fetch_log)
    g.add_node("classify",      _node_classify)
    g.add_node("agent_iter",    _node_agent_iter)
    g.add_node("oneshot_iter",  _node_oneshot_iter)
    g.add_node("persist",       _node_persist)

    g.set_entry_point("fetch_log")
    g.add_edge("fetch_log", "classify")
    g.add_conditional_edges(
        "classify", _node_route,
        {"agent_iter": "agent_iter", "oneshot_iter": "oneshot_iter"},
    )
    g.add_edge("agent_iter",   "persist")
    g.add_edge("oneshot_iter", "persist")
    g.add_edge("persist", END)

    return g.compile(checkpointer=checkpointer) if checkpointer else g.compile()


def _pick_checkpointer() -> Optional[Any]:
    """Pick PG checkpointer when configured + available, else in-memory."""
    want_pg = os.environ.get("BBCTL_RCA_GRAPH_CHECKPOINT") in ("1", "true", "pg")
    if want_pg and _HAS_PG_SAVER:
        try:
            from .rag import _conn_kwargs
            kw = _conn_kwargs()
            uri = (
                f"postgresql://{kw['user']}:{kw['password']}"
                f"@{kw['host']}:{kw['port']}/{kw['dbname']}"
            )
            saver = PostgresSaver.from_conn_string(uri)
            try:
                saver.setup()  # idempotent table create
            except Exception as e:
                _log(f"PG checkpointer setup warning (continuing): {e}")
            return saver
        except Exception as e:
            _log(f"PG checkpointer init failed, falling back to memory: {e}")
    if _HAS_MEM_SAVER:
        return MemorySaver()
    return None


# Compile once (module load) — checkpointer choice resolves at import.
_GRAPH = build_full_graph(checkpointer=_pick_checkpointer()) if _HAS_LANGGRAPH else None


# ─── Public entry points ───────────────────────────────────────────────

async def run_rca_via_graph(*, job: str, build: int, service: str,
                            deep: bool = False, request_id: Optional[str] = None) -> dict:
    """Invoke the graph synchronously, return final RCA dict.

    Same return shape as legacy `main._run_rca`. Caller (main.py) wraps
    this with the 24h cache + dedup + cost-cap guards.
    """
    if _GRAPH is None:
        raise RuntimeError("langgraph not installed; BBCTL_RCA_USE_FULL_GRAPH "
                           "cannot route. pip install langgraph.")
    state_in: dict = {
        "job":        job,
        "build":      int(build),
        "service":    service,
        "deep":       bool(deep),
        "request_id": request_id or str(uuid.uuid4()),
    }
    config: dict = {"configurable": {"thread_id": state_in["request_id"]}}
    state_out = await _GRAPH.ainvoke(state_in, config=config)
    rca = state_out.get("rca") or {}
    rca["request_id"] = state_in["request_id"]
    return rca


async def astream_rca_via_graph(*, job: str, build: int, service: str,
                                deep: bool = False,
                                request_id: Optional[str] = None
                                ) -> AsyncIterator[dict]:
    """Stream node-by-node events for SSE to the UI. Yields dicts like:
       {"event": "node_start", "node": "fetch_log"}
       {"event": "node_end",   "node": "fetch_log", "elapsed_ms": 482}
       {"event": "final",      "rca": {...}}
    """
    if _GRAPH is None:
        raise RuntimeError("langgraph not installed; cannot stream.")
    state_in: dict = {
        "job":        job,
        "build":      int(build),
        "service":    service,
        "deep":       bool(deep),
        "request_id": request_id or str(uuid.uuid4()),
    }
    config: dict = {"configurable": {"thread_id": state_in["request_id"]}}
    import time
    start_times: dict[str, float] = {}
    final_state: dict = {}
    async for ev in _GRAPH.astream_events(state_in, config=config, version="v2"):
        et = ev.get("event")
        name = ev.get("name", "")
        if et == "on_chain_start" and name in (
            "fetch_log", "classify", "agent_iter", "oneshot_iter", "persist"
        ):
            start_times[name] = time.time()
            yield {"event": "node_start", "node": name}
        elif et == "on_chain_end" and name in start_times:
            elapsed = int((time.time() - start_times[name]) * 1000)
            yield {"event": "node_end", "node": name,
                   "elapsed_ms": elapsed}
            data = ev.get("data") or {}
            output = data.get("output")
            if isinstance(output, dict):
                final_state.update(output)
    yield {"event": "final", "rca": final_state.get("rca") or {}}


# ─── CLI ───────────────────────────────────────────────────────────────

def _cli_mermaid() -> int:
    """Print mermaid diagram of the graph to stdout."""
    if _GRAPH is None:
        print("langgraph not installed", flush=True)
        return 1
    try:
        diagram = _GRAPH.get_graph().draw_mermaid()
    except Exception as e:
        print(f"mermaid render failed: {e}", flush=True)
        return 1
    print(diagram)
    return 0


def _cli_smoke() -> int:
    """Confirm graph compiles + lists nodes."""
    if _GRAPH is None:
        print("langgraph not installed; full_graph disabled")
        return 1
    print("full_graph compiled OK")
    print(f"  nodes: fetch_log → classify → "
          f"(agent_iter | oneshot_iter) → persist → END")
    print(f"  checkpointer: "
          f"{'pg' if isinstance(_pick_checkpointer(), type(None)) is False else 'none'}")
    return 0


def main(argv: list[str] | None = None) -> int:
    import sys
    args = list(argv if argv is not None else sys.argv[1:])
    if not args or args[0] in ("-h", "--help", "smoke"):
        return _cli_smoke()
    if args[0] in ("mermaid", "diagram", "render"):
        return _cli_mermaid()
    print(f"unknown command: {args[0]}")
    return 2


if __name__ == "__main__":
    import sys
    raise SystemExit(main(sys.argv[1:]))
