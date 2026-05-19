"""OpenAI function-calling schemas for all 19 RCA agent tools.

This module is schemas-only — no tool implementations. The actual Python
runtime for each tool lives in:
  - mcp_tools.py        (repo_*, jenkins, runbook tools)
  - jira_client.py      (jira_*)
  - github_client.py    (github_*)
  - aws_tools.py        (aws_*)
  - claude_review.py    (code_review)  — file may not exist yet at Phase 1

The agent loop imports TOOLS from here and passes it verbatim to OpenAI's
chat.completions.create(tools=...) argument. The "name" field in each
schema MUST match the Python function name the agent's dispatcher calls
(see agent.py `_execute_tool_call()`).

When adding a new tool:
  1. Append schema here.
  2. Implement Python in the matching <domain>_tools.py file.
  3. Wire dispatcher in agent.py to route the new name.
  4. Update prompts/rca_agent_system_v2.md if it changes the method.

Schemas follow OpenAI function-calling JSON Schema. Keep descriptions
short but unambiguous — the LLM reads them to decide when to call.
"""

TOOLS: list[dict] = [
    # ─── JIRA (2) ──────────────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "jira_get_ticket",
            "description": (
                "Fetch one Jira ticket's fields and custom_fields. Use for "
                "compliance class when the log names a ticket key. Returns "
                "summary, status, assignee, components, fix_versions, "
                "description, and the custom_fields dict (which includes "
                "'Signed Off Commit ID' when set)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "key": {
                        "type": "string",
                        "description": "Jira ticket key as it appears in the log or commit message.",
                    },
                },
                "required": ["key"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "jira_search",
            "description": (
                "Run a JQL search and return matching ticket summaries. Use "
                "for clone-chain discovery or to find related tickets by "
                "component/status. Returns up to `max` results."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "jql": {
                        "type": "string",
                        "description": "JQL query string. Pass a complete JQL expression.",
                    },
                    "max": {
                        "type": "integer",
                        "description": "Max results to return (default 10).",
                        "default": 10,
                    },
                },
                "required": ["jql"],
            },
        },
    },

    # ─── GITHUB (4) ────────────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "github_get_commit",
            "description": (
                "Fetch a commit's metadata and files changed. Use for "
                "compliance commit-mismatch (compare signed-off vs resolved "
                "SHA) and to identify the author/files of a suspect commit."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "description": "Repo name within BLACKBUCK-LABS org. Derive from service.lookup.git_repo or service.lookup.repo, or from the log line referencing the failing file.",
                    },
                    "sha": {
                        "type": "string",
                        "description": "Commit SHA (7-40 hex chars).",
                    },
                },
                "required": ["repo", "sha"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "github_find_pr_for_commit",
            "description": (
                "Find the merged pull request that contains a given commit. "
                "Returns PR number, title, merged_at, author. Returns null if "
                "no merged PR. Use for compliance Mode 5 (PR title must "
                "contain Jira ticket key)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "description": "Repo name in BLACKBUCK-LABS org.",
                    },
                    "sha": {
                        "type": "string",
                        "description": "Commit SHA.",
                    },
                },
                "required": ["repo", "sha"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "github_read_file",
            "description": (
                "Read a slice of a file from a GitHub repo at a specific "
                "ref (branch / tag / SHA). Use for service-repo files "
                "that are NOT cloned locally. For jenkins_pipeline / "
                "InfraComposer use repo_read_file instead (local + "
                "fresher)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "description": "Repo name in BLACKBUCK-LABS org.",
                    },
                    "path": {
                        "type": "string",
                        "description": "File path inside the repo.",
                    },
                    "ref": {
                        "type": "string",
                        "description": "Branch name, tag, or full SHA to read from.",
                    },
                    "start": {
                        "type": "integer",
                        "description": "First line number to return (1-indexed, default 1).",
                        "default": 1,
                    },
                    "end": {
                        "type": "integer",
                        "description": "Last line number to return (inclusive, default 100).",
                        "default": 100,
                    },
                },
                "required": ["repo", "path", "ref"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "github_recent_commits",
            "description": (
                "Recent commits on a GitHub repo branch (default main). Use "
                "to spot recently-introduced regressions in service repos "
                "that aren't cloned locally."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {"type": "string"},
                    "branch": {
                        "type": "string",
                        "description": "Branch name, default 'main'.",
                        "default": "main",
                    },
                    "n": {
                        "type": "integer",
                        "description": "Number of commits to return (default 10).",
                        "default": 10,
                    },
                },
                "required": ["repo"],
            },
        },
    },

    # ─── LOCAL REPOS (4) ───────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "repo_read_file",
            "description": (
                "Read a line range from a locally-cloned repo. Repos available: "
                "'jenkins_pipeline' (Groovy pipeline lib) and 'InfraComposer' "
                "(Terraform configs/modules). Always-fresh: server runs "
                "`git fetch --depth 1 && git reset --hard origin/<branch>` "
                "before the RCA loop. Use this for the mandatory pipeline "
                "cross-check."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "enum": ["jenkins_pipeline", "InfraComposer"],
                    },
                    "path": {
                        "type": "string",
                        "description": "Path inside the repo. Derive from a job_flow doc, a runbook, a log line, or a prior repo_read_file result that named this file. Do NOT guess a path that has not been mentioned in your inputs.",
                    },
                    "start": {"type": "integer", "default": 1},
                    "end": {"type": "integer", "default": 100},
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
                "Run ripgrep across a local clone. Returns matches with "
                "line numbers + 2 lines of context. Use when you need to "
                "find a string/symbol but don't know which file. For "
                "jenkins_pipeline + InfraComposer only."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "enum": ["jenkins_pipeline", "InfraComposer"],
                    },
                    "query": {
                        "type": "string",
                        "description": "Literal or regex pattern.",
                    },
                    "max_results": {
                        "type": "integer",
                        "default": 10,
                    },
                },
                "required": ["repo", "query"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "repo_find_function",
            "description": (
                "Locate a function / pipeline-step definition. Handles the "
                "Jenkins shared-lib convention where `vars/<name>.groovy` "
                "with `def call(...)` IS the definition of step <name>(). "
                "If `vars/<name>.groovy` exists in jenkins_pipeline, the "
                "tool returns its `def call(...)` line as the authoritative "
                "definition (plus any generic regex matches as secondary)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "enum": ["jenkins_pipeline", "InfraComposer"],
                    },
                    "name": {
                        "type": "string",
                        "description": "Function or pipeline-step name as it appears in the calling code or in a job_flow doc you read.",
                    },
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
                "Recent commits on a locally-cloned repo (jenkins_pipeline "
                "or InfraComposer). Useful when a previously-green job "
                "starts failing — a recent commit is often the cause."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "repo": {
                        "type": "string",
                        "enum": ["jenkins_pipeline", "InfraComposer"],
                    },
                    "n": {"type": "integer", "default": 10},
                },
                "required": ["repo"],
            },
        },
    },

    # ─── JENKINS (1) ───────────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "get_jenkins_job_config",
            "description": (
                "Fetch a Jenkins job's config.xml and extract: scm_url, "
                "scm_branch, script_path, inline_script (when defined "
                "inline). The script_path is the entrypoint .groovy file "
                "Jenkins runs — call this first to locate the pipeline "
                "source for the mandatory cross-check."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "job": {
                        "type": "string",
                        "description": "Jenkins job name as it appears in build_meta.job.",
                    },
                },
                "required": ["job"],
            },
        },
    },

    # ─── SERVICE LOOKUP (1) ────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "service_lookup",
            "description": (
                "Re-read a service's entry in jenkins_pipeline/resources/"
                "config.json. The boot-pack already includes the lookup "
                "for the build's primary service, so usually unnecessary. "
                "Use this only when the log references ANOTHER service "
                "(upstream dependency, sibling deploy) you need details "
                "for. Returns a slim dict with aws_account, region, "
                "rule_arn, target_port, log_path, etc."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "service": {
                        "type": "string",
                        "description": "Service name as it appears in config.json keys.",
                    },
                },
                "required": ["service"],
            },
        },
    },

    # ─── RUNBOOKS (2) ──────────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "list_runbooks",
            "description": (
                "List available runbook files under docops/runbooks/, each "
                "with a one-line summary. Use when you're unsure which "
                "error_class fits the log signals. Returns a list to help "
                "you pick which runbook to read_runbook()."
            ),
            "parameters": {
                "type": "object",
                "properties": {},
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "read_runbook",
            "description": (
                "Read a runbook's full markdown content. Use after picking "
                "the matching error class to get its drill plan, common "
                "failure modes, and fix templates. Runbooks are indexed "
                "by error class — call list_runbooks() first if unsure "
                "which name to pass."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "name": {
                        "type": "string",
                        "description": (
                            "Runbook stem name without .md extension. Use "
                            "list_runbooks() to see available names."
                        ),
                    },
                },
                "required": ["name"],
            },
        },
    },

    # ─── JOB FLOWS (orient on pipeline shape per job family) ───────────
    {
        "type": "function",
        "function": {
            "name": "list_job_flows",
            "description": (
                "List available job-flow documentation files. Each entry "
                "is one Jenkins pipeline family describing its main "
                "pipeline file, top-level stages, which helper each "
                "stage delegates to, and where chains nest. Match the "
                "Jenkins job name (and inline_script signature when "
                "available) to the right flow, then read_job_flow() it "
                "before drilling into individual .groovy files. Job "
                "flows are factual descriptions — they do NOT contain "
                "fix templates (use runbooks for that)."
            ),
            "parameters": {"type": "object", "properties": {}},
        },
    },
    {
        "type": "function",
        "function": {
            "name": "read_job_flow",
            "description": (
                "Read a job-flow doc to learn the pipeline shape for a "
                "Jenkins job family. Tells you which main pipeline file "
                "to read, which top-level stages exist, and which helper "
                "file each stage calls. Use this BEFORE repo_read_file "
                "on a helper — the doc tells you which helper is real "
                "for THIS job (helper names that look similar between "
                "jobs are not interchangeable)."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "name": {
                        "type": "string",
                        "description": (
                            "Job-flow stem name without .md extension. "
                            "Use list_job_flows() to see available names."
                        ),
                    },
                },
                "required": ["name"],
            },
        },
    },

    # ─── AWS CROSS-ACCOUNT (1 generic — Option A) ─────────────────────
    # Single tool covers all AWS read APIs (Describe*/Get*/List*/Lookup*/
    # Search*/Show*/Estimate*). Server validates the operation name + does
    # STS AssumeRole into BBCTLRcaReadOnly for the target account. This
    # replaced the four narrow tools (describe_target_health, _target_
    # group, _instance, _listener_rule) — same coverage, less spec spam,
    # auto-extends to RDS / Lambda / Logs / Autoscaling / IAM Get* etc.
    # without a new tool definition per call site.
    {
        "type": "function",
        "function": {
            "name": "aws_describe",
            "description": (
                "Call any AWS read-only API. The server validates that "
                "the operation name starts with one of Describe / Get / "
                "List / Lookup / Search / Show / Estimate. Write actions "
                "are rejected. Cross-account STS AssumeRole into "
                "BBCTLRcaReadOnly is handled by the server when "
                "aws_account is not the host account. Pass values you "
                "have seen in the log, in service.lookup, or in a "
                "prior tool result — do not invent ARNs, instance IDs, "
                "or rule IDs."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "service": {
                        "type": "string",
                        "description": "boto3 service code (lowercase).",
                    },
                    "operation": {
                        "type": "string",
                        "description": "PascalCase boto3 operation name. MUST start with Describe / Get / List / Lookup / Search / Show / Estimate.",
                    },
                    "params": {
                        "type": "object",
                        "description": "Operation parameters as a JSON object. Pass {} when the operation requires none.",
                    },
                    "aws_account": {
                        "type": "string",
                        "description": "Account name from service.lookup.aws_account or its 12-digit account ID. Determines STS AssumeRole target.",
                    },
                    "aws_region": {
                        "type": "string",
                        "description": "AWS region code from service.lookup.aws_region or from the log.",
                    },
                },
                "required": ["service", "operation", "params",
                             "aws_account", "aws_region"],
            },
        },
    },
    # aws_run_ssm_command — REMOVED per Option C decision. RCA never
    # logs into instances; service-side root cause stays out of scope.
    # For health_check / java_runtime instance-level failures the LLM
    # tells the operator to use `bbctl shell <instance_id>` themselves.

    # ─── SANITY (1) ───────────────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "code_review",
            "description": (
                "Second-opinion sanity check using gpt-4o-mini. Pass a "
                "diff/snippet/path plus a question; returns verdict "
                "(looks_good | concerns | rejected) plus notes. Use "
                "sparingly when your suggested fix touches nontrivial "
                "code, or to verify an evidence snippet matches the "
                "actual file contents."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "diff_or_path": {
                        "type": "string",
                        "description": (
                            "Either a unified diff to review, or a "
                            "'<repo>/<path>:<line>-<line>' reference to "
                            "have the reviewer fetch + read."
                        ),
                    },
                    "prompt": {
                        "type": "string",
                        "description": "The specific verification question for the reviewer.",
                    },
                },
                "required": ["diff_or_path", "prompt"],
            },
        },
    },
]


def get_tool_names() -> list[str]:
    """Return just the tool names. Used in the system prompt summary."""
    return [t["function"]["name"] for t in TOOLS]


def get_tool_by_name(name: str) -> dict | None:
    """Look up a tool schema by function name."""
    for t in TOOLS:
        if t["function"]["name"] == name:
            return t
    return None
