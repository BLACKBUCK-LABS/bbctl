# bbctl-rca Agent-Only Migration Plan

**Status:** APPROVED — implementation pending one final check.
**Branch:** `feature/bbctl-rca-agent-only`
**Author:** g.hariharan
**Last revised:** 2026-05-18

---

## Locked architecture decisions

### 1. Boot-pack — 3 blocks ONLY

Every RCA's initial user message contains these and nothing else:

| Block | Source | Why kept |
|---|---|---|
| `log_window` | Jenkins `wfapi/describe` + `consoleText` | Starting evidence |
| `build_meta` | Jenkins `/api/json` | job, build_id, result, url, timestamp |
| `service.lookup(<svc>)` | local `config.json` | LLM needs `aws_account`, `aws_region`, `rule_arn`, `target_port`, `git_repo`, `log_path` to call AWS tools — without these, LLM cannot operate. Pure local lookup, no decision pre-baked. |

**Dropped from boot-pack** (LLM fetches via tools instead):
- `error_class` — LLM classifies on its own from log content
- `detected_failed_stage` — LLM scans `[Pipeline] { (...)` markers itself
- `source.trace` hints — LLM uses `repo_search` if needed
- `jira.tickets` — `jira_get_ticket` tool
- `github.commits` — `github_get_commit` tool
- `docs.<NAME>.md` runbook content — `read_runbook` tool

### 2. Classification — LLM autonomous via runbook files

System prompt holds only generic method. Per-class drill plans live in markdown
runbooks under `/opt/bbctl-rca/docops/runbooks/`. LLM discovers them with
`list_runbooks()`, reads with `read_runbook(name)`, follows the plan inside.

Runbook files to author:
```
compliance.md       parse_error.md      java_runtime.md
health_check.md     canary_fail.md      canary_script_error.md
terraform.md        scm.md              aws_limit.md
unknown.md
```

Each runbook contains:
- **Detect signals** — log patterns that confirm this class
- **Modes** — common sub-types of this failure
- **Drill plan** — exact tool sequence to call
- **Fix templates** — Finding / Action / Verify scaffolding

`unknown.md` is the fallback when no other runbook matches. Its drill plan is
generic: regex log for Jira keys / SHAs / instance IDs / ARNs / Java
exceptions / Groovy stack frames; for each match, call the matching tool;
keep going until clear RCA.

### 3. Mandatory pipeline-stage cross-check (ALL classes)

Every RCA, regardless of class, must:
1. Identify failed stage by scanning log for last `[Pipeline] { (<name>)` marker
2. Call `get_jenkins_job_config(job)` → resolve `scriptPath`
3. Call `repo_read_file("jenkins_pipeline", <scriptPath>, ...)` around the stage block
4. Cite at least one `jenkins_pipeline/<file>:<line>` in `evidence[]`

Enforced in system prompt + post-RCA validator.

### 4. Stopping rule — LLM decides

LLM stops iterating when it has clear RCA. No confidence-threshold bail.
Server enforces three hard safety caps only:

| Cap | Value | Purpose |
|---|---|---|
| `MAX_TOOL_CALLS` | 25 | Runaway-loop guard |
| `WALL_CLOCK_SEC` | 180 (3 min) | Jenkins post-block timeout |
| `COST_HARD_KILL_USD` | 5.00 | Panic killswitch — single RCA should never spend $5; if it does, something is broken |

Hitting any cap appends "BUDGET_EXHAUSTED" system message, forces JSON-only
response, sets `needs_deeper: true` automatically.

### 5. Confidence field

Removed from output schema. LLM iterates until it finds clear RCA; no
self-rated score gating the loop. Dashboard ranks by `needs_deeper` flag
+ evidence count instead.

Final output schema:
```json
{
  "summary": "string",
  "failed_stage": "string",
  "error_class": "string",
  "root_cause": "string with citations",
  "evidence": [{"source": "...", "snippet": "..."}],
  "suggested_fix": {"Finding": "...", "Action": "...", "Verify": "..."},
  "suggested_commands": [{"cmd": "...", "tier": "safe|restricted", "rationale": "..."}],
  "needs_deeper": false
}
```

### 6. Tool list (19 total)

| # | Tool | Purpose |
|---|---|---|
| 1 | `jira_get_ticket(key)` | Jira ticket fields + custom_fields |
| 2 | `jira_search(jql, max=10)` | JQL search (clone-chain discovery) |
| 3 | `github_get_commit(repo, sha)` | Commit metadata + files |
| 4 | `github_find_pr_for_commit(repo, sha)` | PR for commit |
| 5 | `github_read_file(repo, path, ref, start?, end?)` | Read service-repo file via raw API |
| 6 | `github_recent_commits(repo, branch, n)` | Recent commits on non-cloned repo |
| 7 | `repo_read_file(repo, path, start, end)` | Local clone read (jenkins_pipeline / InfraComposer) |
| 8 | `repo_search(repo, query)` | ripgrep across local clone |
| 9 | `repo_find_function(repo, name)` | Find function def (incl. vars/ convention) |
| 10 | `repo_recent_commits(repo, n)` | Recent commits on local clone |
| 11 | `get_jenkins_job_config(job)` | Jenkins job XML config |
| 12 | `list_runbooks()` | List available runbook files + 1-line summary |
| 13 | `read_runbook(name)` | Read `docops/runbooks/<name>.md` |
| 14 | `aws_describe_target_health(tg_arn)` | ALB target health |
| 15 | `aws_describe_target_group(tg_arn)` | TG health-check config |
| 16 | `aws_describe_instance(instance_id)` | EC2 state, IP, tags |
| 17 | `aws_describe_listener_rule(rule_arn)` | ALB rule conditions/actions |
| 18 | `aws_run_ssm_command(instance_id, cmd)` | Whitelisted shell via SSM |
| 19 | `claude_code_review(diff_or_path, prompt)` | Anthropic sanity check |

### 7. AWS cross-account access — single IAM role per account

Approach: **`BBCTLRcaReadOnly`** role in each of 4 accounts (zinka, bbfinserv,
divum, tzf). Each role:

- **Trust:** bbctl-rca host's EC2 instance role (only)
- **Permissions:** AWS-managed `ReadOnlyAccess` policy + custom inline policy
  for SSM SendCommand (since `ReadOnlyAccess` doesn't include SendCommand)

```hcl
# InfraComposer/module/bbctl_rca_reader/main.tf
resource "aws_iam_role" "bbctl_rca_reader" {
  name = "BBCTLRcaReadOnly"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = { AWS = var.bbctl_rca_host_role_arn }
      Action = "sts:AssumeRole"
    }]
  })
}

# Broad read coverage — managed policy
resource "aws_iam_role_policy_attachment" "readonly" {
  role       = aws_iam_role.bbctl_rca_reader.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# Narrow SSM execute — inline policy
resource "aws_iam_role_policy" "ssm_send" {
  name = "bbctl-rca-ssm-send"
  role = aws_iam_role.bbctl_rca_reader.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ssm:SendCommand",
        "ssm:GetCommandInvocation",
        "ssm:DescribeInstanceInformation"
      ]
      Resource = "*"
      Condition = {
        StringEquals = {
          "ssm:DocumentName": ["AWS-RunShellScript"]
        }
      }
    }]
  })
}
```

Applied 4× (one per account workspace). bbctl-rca host role granted
`sts:AssumeRole` on all 4 target ARNs.

**Why `ReadOnlyAccess` (managed) instead of narrow custom policy:**
- LLM can call any describe/list/get without us pre-listing every API
- Future-proof — adding `aws_describe_<new_resource>` tool doesn't need
  another IAM change
- ReadOnlyAccess is AWS-vetted; no risk of accidental write permission
- Single inline policy isolates the only non-read action we use (SSM SendCommand)

### 8. SSM safe-command whitelist

LLM-callable but server enforces the command-pattern matches the whitelist
before calling AWS SSM. Anything outside this set → tool returns error
"command not in whitelist".

| Pattern | Purpose |
|---|---|
| `tail -n <N> <path>` | Read end of log file |
| `ss -tlnp \| grep <port>` | Check listening ports |
| `curl -i http://localhost:<port><path>` | Probe local health endpoint |
| `systemctl status <svc>` | Service state |
| `cat <path>` (under `/var/log/` or `/etc/blackbuck/`) | Read config / log |
| `ls <path>` (under `/var/log/blackbuck/` or `/opt/`) | List artifacts |
| `journalctl -n <N> -u <svc>` | systemd logs |
| `ps aux \| grep <name>` | Process listing |
| `df -h` / `free -m` / `uptime` | System health |

Whitelist enforced in `bbctl_rca/aws_tools.py:_validate_ssm_command()`. Each
command parsed, command-name + arg-pattern checked against allow-table.
Reject everything else with explanatory error.

### 9. Claude code-review integration

**Mode:** validator + optional mid-loop tool (both).

- **Post-RCA validator** (automatic): after LLM emits final JSON, server calls
  Claude to verify each `evidence[].source` actually exists and the cited
  snippet appears at the cited line. Drops hallucinated citations.
- **Mid-loop tool** (LLM-callable): `claude_code_review` exposed in tool
  list. LLM can call it to get a code-quality second opinion on its
  suggested fix or to sanity-check a diff. Used sparingly (~5-10% of RCAs
  where the suggested fix involves nontrivial code change).

Model: `claude-sonnet-4-6` (cost ~$0.003 per validator call, ~$0.01 per
mid-loop call).

API key: `ANTHROPIC_API_KEY` via systemd drop-in.

---

## Implementation phases

### Phase 1 — System prompt + tool schemas + runbook files [READY]

No Python yet. Just markdown + JSON schemas.

Deliverables:
- Rewrite `bbctl/prompts/rca_agent_system.md` to ~50-line generic method
- Author 10 runbook files in `bbctl/docops/runbooks/`
- Write OpenAI function-calling schemas for all 19 tools in a single
  `bbctl/bbctl_rca/tool_schemas.py` file

### Phase 2 — Tool implementations (Jira / GitHub / runbook) [NO AWS YET]

Deliverables:
- `bbctl_rca/mcp_tools.py`: add `jira_get_ticket`, `jira_search`,
  `github_get_commit`, `github_find_pr_for_commit`, `github_read_file`,
  `github_recent_commits`, `list_runbooks`, `read_runbook`
- Reuse existing `jira_client.py` / `github_client.py` where present
- Per-RCA tool cache (same call within RCA = 1 API hit)

### Phase 3 — Route ALL classes to agent.py [BREAKING]

Deliverables:
- `main.py:_run_rca`: drop one-shot branch. Always call `run_agent_rca`.
- Strip pre-fetched Jira / GitHub / runbook from `_build_tool_context`
- Boot-pack reduced to log + meta + service.lookup
- Env-flag `BBCTL_RCA_FORCE_AGENT_MODE=1` for opt-in testing on EC2
- Flip default after 2-week soak

### Phase 4 — AWS Terraform [INFRA WORK, BLOCKS PHASE 5]

Deliverables:
- Author `InfraComposer/module/bbctl_rca_reader/`
- Coordinate with infra team for apply across zinka / bbfinserv / divum / tzf
- bbctl-rca host role gets `sts:AssumeRole` for all 4 target ARNs
- Test STS + describe call against each account from EC2

### Phase 5 — AWS tools [NEEDS PHASE 4 COMPLETE]

Deliverables:
- `bbctl_rca/aws_tools.py`: 5 tools with cross-account STS assume-role
- SSM command whitelist + `_validate_ssm_command()`
- Per-RCA STS credential cache (1 assume-role per account, reused within RCA)

### Phase 6 — Claude code-review integration

Deliverables:
- `bbctl_rca/claude_review.py` using `anthropic` SDK
- Post-RCA validator wired into `main.py:_run_rca` after agent returns
- `claude_code_review` tool added to agent tool list
- `ANTHROPIC_API_KEY` systemd env

### Phase 7 — Tests + dashboard

Deliverables:
- Replay last 30 days of audit logs through new agent path
- Diff RCAs vs current; sanity-check 5 sample failures per class
- Dashboard: per-RCA "Agent mode: X iters, Y tools, $Z" badge
- Per-build trace download endpoint at `/rca/v1/dashboard/<job>/<build>/trace.txt`

### Phase 8 — Cleanup

Deliverables:
- Delete `llm.py:run_rca_openai` one-shot path
- Move `_build_tool_context` boot-pack-only assembly into `agent.py`
- Update `docs/rca/bbctlrca.md` with new architecture

---

## Open items (need answer before Phase 1)

| # | Item | Status |
|---|---|---|
| 1 | Boot-pack = log + meta + service.lookup | ✅ locked |
| 2 | LLM auto-classifies via runbook MD files | ✅ locked |
| 3 | Mandatory pipeline cross-check | ✅ locked |
| 4 | No confidence score in schema | ✅ locked |
| 5 | Caps: 25 tools / 180s / $5 panic | ✅ locked |
| 6 | AWS role = ReadOnlyAccess + SSM inline | ✅ locked |
| 7 | Claude review = validator + tool | ✅ locked |
| 8 | SSM whitelist | ✅ locked (9 patterns) |
| 9 | **AWS account IDs** for zinka/bbfinserv/divum/tzf | ⚠️ NEED FROM USER |
| 10 | **bbctl-rca host role ARN** (current EC2 instance role) | ⚠️ NEED FROM USER |
| 11 | **GitHub PAT** for github_read_file — reuse `jenkins-git-bb` or fresh? | ⚠️ NEED FROM USER |
| 12 | **ANTHROPIC_API_KEY** — exists in any account? | ⚠️ NEED FROM USER |

---

## Cost projections (no cap, LLM iterates freely)

| Class | Before (one-shot or current agent) | After Option C |
|---|---|---|
| compliance | $0.02 / 5s | $0.10 / 30s |
| parse_error | $0.02 / 5s | $0.09 / 25s |
| aws_limit | $0.02 / 5s | $0.08 / 22s |
| java_runtime | $0.10 / 30s | $0.15 / 40s |
| health_check | $0.08 / 25s | $0.22 / 60s |
| canary_fail | $0.10 / 30s | $0.18 / 45s |
| terraform | $0.10 / 30s | $0.16 / 42s |
| scm | $0.02 / 5s | $0.10 / 30s |
| unknown | $0.05 / 15s | $0.40 / 120s (worst case) |

Avg ~$0.14/RCA. Burst ~$0.50 in pathological cases.

---

## Acceptance criteria

- [ ] Build 15 (compliance) goes through agent loop — trace shows ≥3 distinct tool kinds called
- [ ] Compliance RCA evidence cites Jira ticket via tool fetch (not primer)
- [ ] Health check RCA calls AWS target health + SSM tail log
- [ ] Every RCA cites at least one `jenkins_pipeline/<file>:<line>` (mandatory cross-check)
- [ ] No RCA hits $5 hard killswitch in 30-day soak
- [ ] Average RCA latency ≤ 60s; p95 ≤ 120s
- [ ] All 10 runbook files authored and discoverable via `list_runbooks()`
- [ ] Trace file shows full LLM agency — REQUEST + RESPONSE (raw model_dump) + TOOL per iter
- [ ] Post-RCA Claude validator drops any hallucinated evidence citation
