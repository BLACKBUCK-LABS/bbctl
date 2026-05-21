"""Single name → callable map for the agent loop's tool dispatcher.

Imports the actual implementations from their natural homes:
  - mcp_tools: repo_*, get_jenkins_job_config, list_runbooks, read_runbook
  - jira     : jira_get_ticket, jira_search
  - github   : github_get_commit, github_find_pr_for_commit,
               github_read_file, github_recent_commits

Phase 5 will extend this with aws_* tools from aws_tools.py.
Phase 6 will add code_review from claude_review.py (will likely be
renamed since we use OpenAI gpt-4o-mini, not Anthropic).

agent.py's dispatcher reads TOOL_DISPATCH to resolve LLM-requested
tool names → Python callables. Each value is sync or async; the
dispatcher awaits coroutines.
"""
from . import aws_tools, github, jira, mcp_tools

# rag is imported lazily inside the dispatch wrapper so the agent loop
# can still start when Postgres / pgvector aren't installed. R1 is
# dormant infra; this lets non-RAG environments run unchanged.
try:
    from . import rag as _rag_mod
except Exception:
    _rag_mod = None


def _rag_search_wrapper(query: str, k: int = 5,
                        source_types: list[str] | None = None,
                        error_class: str | None = None) -> str:
    """Adapter — returns a formatted string of top-k hits so the LLM
    can read it back cleanly. Falls back to a clear error string when
    rag.py is unimportable or PG is down."""
    if _rag_mod is None:
        return ("rag_search unavailable: psycopg/pgvector not installed "
                "or rag module failed to import. Use repo_search / "
                "list_docs as fallback.")
    try:
        hits = _rag_mod.search(
            query, k=int(k or 5),
            source_types=source_types, error_class=error_class,
        )
    except Exception as e:
        return f"rag_search error: {e}"
    if not hits:
        return "rag_search: no matches"
    lines = []
    for h in hits:
        meta = h.get("meta") or {}
        eclass = meta.get("error_class") or ""
        lines.append(
            f"[{h['score']:.3f}] {h['source_type']}/{h['source_id']}"
            f"{(' (class=' + eclass + ')') if eclass else ''}\n"
            f"  {h['chunk_text'][:600]}…"
        )
    return "\n\n".join(lines)


TOOL_DISPATCH: dict[str, callable] = {
    # ── runbook (error-class drill plans) ──
    "list_runbooks":               mcp_tools.list_runbooks,
    "read_runbook":                mcp_tools.read_runbook,

    # ── job_flow (per-pipeline-family orientation docs) ──
    "list_job_flows":              mcp_tools.list_job_flows,
    "read_job_flow":               mcp_tools.read_job_flow,

    # ── org docs (broader docops/*.md beyond runbooks + job_flows) ──
    "list_docs":                   mcp_tools.list_docs,
    "read_doc":                    mcp_tools.read_doc,

    # ── local repos (jenkins_pipeline / InfraComposer) ──
    "repo_read_file":              mcp_tools.repo_read_file,
    "repo_list_dir":               mcp_tools.repo_list_dir,
    "repo_search":                 mcp_tools.repo_search,
    "repo_find_function":          mcp_tools.repo_find_function,
    "repo_recent_commits":         mcp_tools.repo_recent_commits,

    # ── Jenkins API ──
    # NOTE: existing get_jenkins_job_config lives in main.py / jenkins.py.
    # Phase 3 will wire this entry; left as None placeholder if not yet
    # importable from mcp_tools.
    # "get_jenkins_job_config":    will be wired in Phase 3.

    # ── Jira (live API, shares jira.py creds) ──
    "jira_get_ticket":             jira.fetch_ticket,
    "jira_search":                 jira.search,

    # ── GitHub (live API, shares github.py creds: BBCTL_GITHUB_PAT) ──
    "github_get_commit":           github.fetch_commit,
    "github_find_pr_for_commit":   github.find_pr_for_commit,
    "github_read_file":            github.read_file,
    "github_recent_commits":       github.recent_commits,

    # ── AWS cross-account (Option A — single generic describe) ──
    # Replaces the four narrow tools (describe_target_health,
    # _target_group, _instance, _listener_rule). Same coverage, less
    # spec spam, auto-extends to RDS / Lambda / Logs / etc. without
    # new tool definitions.
    # SSM SendCommand is intentionally NOT exposed — Option C decision:
    # RCA never logs into instances; operator uses `bbctl shell <id>`
    # themselves when service-side detail is needed.
    "aws_describe":                 aws_tools.describe,

    # ── RAG semantic search (R2) ──
    # Postgres + pgvector. Wrapper imports rag.py lazily and degrades
    # gracefully when PG is offline so the agent loop still works on
    # non-RAG hosts. See bbctl/docs/rca/RAGflow.md for design.
    "rag_search":                   _rag_search_wrapper,

    # ── Sanity (Phase 6) ──
    # "code_review":                  claude_review.code_review,
}
