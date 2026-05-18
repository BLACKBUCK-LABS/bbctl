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


TOOL_DISPATCH: dict[str, callable] = {
    # ── runbook (local file read) ──
    "list_runbooks":               mcp_tools.list_runbooks,
    "read_runbook":                mcp_tools.read_runbook,

    # ── local repos (jenkins_pipeline / InfraComposer) ──
    "repo_read_file":              mcp_tools.repo_read_file,
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

    # ── AWS cross-account describes (Phase 5) ──
    # SSM SendCommand is intentionally NOT exposed — Option C decision:
    # RCA never logs into instances; operator uses `bbctl shell <id>`
    # themselves when service-side detail is needed.
    "aws_describe_target_health":   aws_tools.describe_target_health,
    "aws_describe_target_group":    aws_tools.describe_target_group,
    "aws_describe_instance":        aws_tools.describe_instance,
    "aws_describe_listener_rule":   aws_tools.describe_listener_rule,

    # ── Sanity (Phase 6) ──
    # "code_review":                  claude_review.code_review,
}
