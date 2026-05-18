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
                        "description": "Jira ticket key, e.g. 'MB-7545' or 'FMSCAT-5887'.",
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
                "for clone-chain discovery (e.g. 'issuekey in (X, Y, Z)') or "
                "to find related tickets by component/status. Returns up to "
                "`max` results."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "jql": {
                        "type": "string",
                        "description": "JQL query string, e.g. 'project = FMSCAT AND status = \"In Progress\"'.",
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
                        "description": "Repo name within BLACKBUCK-LABS org, e.g. 'alchemist'.",
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
                "ref (branch / tag / SHA). Use for service-repo files that "
                "are NOT cloned locally (alchemist, demand, fms-*, etc.). "
                "For jenkins_pipeline / InfraComposer use repo_read_file "
                "instead (local + fresher)."
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
                        "description": "Path inside the repo, e.g. 'vars/JiraDetails.groovy'.",
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
                        "description": "Function or pipeline-step name, e.g. 'JiraDetails', 'nonwebdeploy'.",
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
                        "description": "Jenkins job name, e.g. 'create-quick-infra-devops-test'.",
                    },
                },
                "required": ["job"],
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
                "Read a runbook's full markdown content. Use after you've "
                "picked the matching error class to get its drill plan, "
                "common failure modes, and fix templates."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "name": {
                        "type": "string",
                        "description": (
                            "Runbook name without extension, e.g. 'compliance', "
                            "'health_check', 'java_runtime', 'canary_fail', "
                            "'canary_script_error', 'terraform', 'scm', "
                            "'aws_limit', 'parse_error', 'unknown'."
                        ),
                    },
                },
                "required": ["name"],
            },
        },
    },

    # ─── AWS CROSS-ACCOUNT (5) ────────────────────────────────────────
    {
        "type": "function",
        "function": {
            "name": "aws_describe_target_health",
            "description": (
                "ALB target group's current target-health states. Returns "
                "list of {target_id, port, state, reason, description}. "
                "Account/region inferred from the ARN. STS AssumeRoles "
                "BBCTLRcaReadOnly when cross-account."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "target_group_arn": {
                        "type": "string",
                        "description": "Full ARN of the target group.",
                    },
                },
                "required": ["target_group_arn"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "aws_describe_target_group",
            "description": (
                "ALB target group's static config: name, protocol, port, "
                "health_check_path, health_check_port, "
                "health_check_interval_seconds, healthy_threshold_count, "
                "unhealthy_threshold_count, vpc_id."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "target_group_arn": {"type": "string"},
                },
                "required": ["target_group_arn"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "aws_describe_instance",
            "description": (
                "EC2 instance state, network, tags. Returns state, "
                "instance_type, private_ip, public_ip, vpc_id, subnet_id, "
                "security_groups, tags, launch_time, ami_id."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "instance_id": {
                        "type": "string",
                        "description": "EC2 instance ID, e.g. 'i-0a1b2c3d4e5f6g7h8'.",
                    },
                    "aws_account": {
                        "type": "string",
                        "description": (
                            "Account name from service.lookup "
                            "(zinka|bbfinserv|divum|tzf). Determines which "
                            "BBCTLRcaReadOnly role to assume."
                        ),
                    },
                    "aws_region": {
                        "type": "string",
                        "description": "AWS region, e.g. 'ap-south-1'.",
                    },
                },
                "required": ["instance_id", "aws_account", "aws_region"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "aws_describe_listener_rule",
            "description": (
                "ALB listener rule's conditions + actions. For weighted "
                "forward actions (used by canary traffic shifts) returns "
                "the {target_group_arn, weight} pairs."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "rule_arn": {"type": "string"},
                },
                "required": ["rule_arn"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "aws_run_ssm_command",
            "description": (
                "Run a WHITELISTED shell command on an EC2 instance via SSM "
                "SendCommand (AWS-RunShellScript). Server enforces a "
                "command-pattern whitelist; anything outside returns an "
                "error. Allowed patterns: "
                "tail -n <N> <path>, "
                "ss -tlnp [| grep <port>], "
                "curl -i http://localhost:<port><path>, "
                "systemctl status <svc>, "
                "cat <path under /var/log/ or /etc/blackbuck/>, "
                "ls <path under /var/log/blackbuck/ or /opt/>, "
                "journalctl -n <N> -u <svc>, "
                "ps aux | grep <name>, "
                "df -h | free -m | uptime. "
                "Returns {stdout, stderr, exit_code, command_id}."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "instance_id": {"type": "string"},
                    "cmd": {
                        "type": "string",
                        "description": "Shell command. Must match the whitelist.",
                    },
                    "aws_account": {
                        "type": "string",
                        "description": "Account name from service.lookup.",
                    },
                    "aws_region": {"type": "string"},
                },
                "required": ["instance_id", "cmd", "aws_account", "aws_region"],
            },
        },
    },

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
                        "description": (
                            "What to check, e.g. 'does this snippet at "
                            "vars/JiraDetails.groovy:9 actually require 3 "
                            "args as I claim?'"
                        ),
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
