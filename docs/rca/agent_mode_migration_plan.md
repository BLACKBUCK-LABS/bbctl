# bbctl-rca Agent-Mode Migration Plan

**Status:** DRAFT — awaiting sign-off before implementation.
**Branch:** `feature/bbctl-rca-agent-only`
**Author:** g.hariharan
**Date:** 2026-05-18

---

## Motivation

Manager review of build-15 (compliance class) trace flagged that the LLM
received fully pre-fetched context (Jira tickets, runbook, GitHub commits)
in the user message and emitted a verdict on the first turn — i.e. the
LLM "decided nothing." Target architecture: every RCA goes through the
agent loop, the LLM reads the error log first, then *chooses* which
tool to call (Jira API, GitHub API, AWS API, repo file, runbook).

## Scope

In:
- Drop pre-fetched `jira.tickets`, `github.commits`, `docs.<NAME>.md`
  from the user-message primer; expose each as a tool the LLM calls
  on demand.
- Convert every `error_class` to use `agent.py` loop (kill the one-shot
  `llm.py` path for OpenAI).
- Add AWS read tools (cross-account assume-role across zinka, bbfinserv,
  divum, tzf).
- Add GitHub raw-file tool so service-repo source code is fetched
  on demand (no N-clones-on-disk).
- Add Claude code-review integration for evidence cross-checking.

Out (deferred):
- Migrating from gpt-4.1 to Claude as the agent model. Stays gpt-4.1
  for now; Claude only used in code-review tool role.
- Slack approval workflows for `restricted`-tier suggested_commands.

## Boot-pack (kept in primer)

These remain in the initial user message because they are cheap, local,
deterministic, and hand the LLM the IDs it needs to query everything else:

| Block | Source | Why kept |
|---|---|---|
| `log_window` | Jenkins `wfapi/describe` + `consoleText` | The starting evidence |
| `build_meta` | Jenkins `/api/json` | job, build, result, duration |
| `detected_failed_stage` | regex over log | LLM doesn't have to scan log for stage markers |
| `service.lookup(<svc>)` | local `config.json` | Hands LLM the AWS resource IDs (rule_arn, target_port, account, region, log paths) — without these the LLM cannot query AWS tools meaningfully |
| `source.trace` (HINTS only) | regex against repo file list | List of *candidate paths* matched on error string. NOT file contents — LLM still calls `repo_read_file` to see them. |
| `error_class` | classifier | Hint only; LLM may override |

Dropped from primer (now tools):

| Was | Now |
|---|---|
| `jira.tickets[]` block | `jira_get_ticket(key)` tool |
| `github.commits[]` block | `github_get_commit(repo, sha)` + `github_find_pr_for_commit(repo, sha)` |
| `docs.<NAME>.md` runbook | `read_runbook(name)` tool |

## New tools

### Jira
- `jira_get_ticket(key)` → `{summary, status, assignee, components, fix_versions, resolution, description, custom_fields}`
- `jira_search(jql, max=10)` → list of ticket summaries (for clone-chain discovery)

### GitHub
- `github_get_commit(repo, sha)` → `{author, date, message, files_changed[]}`
- `github_find_pr_for_commit(repo, sha)` → `{number, title, merged_at, author}` or null
- `github_read_file(repo, path, ref, start?, end?)` → file slice via raw.githubusercontent.com (replaces N service-repo clones)
- `github_recent_commits(repo, branch, n=10)` — already covered by `repo_recent_commits` for jenkins_pipeline/InfraComposer; add this for service repos

### AWS (cross-account)
- `aws_describe_target_health(target_group_arn)` → list of `{target_id, state, reason, description}`
- `aws_describe_target_group(target_group_arn)` → health check path/port/protocol/interval/threshold
- `aws_describe_instance(instance_id)` → state, private_ip, sg ids, launch_time, tags
- `aws_describe_listener_rule(rule_arn)` → conditions, actions, weights
- `aws_run_ssm_command(instance_id, cmd)` → ONLY tagged-as-`safe` shell commands (whitelist: `tail`, `ss`, `curl localhost`, `systemctl status`, `cat`, `ls`); no `sudo systemctl restart`, no file writes. Returns stdout+stderr.

Account resolution: each AWS tool reads `aws_account` + `aws_region` from
the service's `service.lookup` block in primer. Server-side STS assume-role
to the matching account before each call. Cached for the RCA's lifetime
(2nd call to same account reuses creds).

### Runbooks
- `read_runbook(name)` → `docs/runbooks/<name>.md` content

### Claude code review
- `claude_code_review(diff_or_path, prompt)` → calls Claude (claude-opus-4-7 or claude-sonnet-4-6 via Anthropic API) to:
  - validate that a suggested code fix in `suggested_fix.Action` is sane against the current source
  - sanity-check evidence (does the cited file:line actually contain what the LLM claims?)
  - second-opinion on confidence score
  Used by post-RCA evidence validator + optionally as an agent tool the LLM can call mid-loop.

## Cross-account AWS IAM provisioning

New Terraform module: `InfraComposer/module/bbctl_rca_reader/`

```hcl
# main.tf
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

resource "aws_iam_role_policy" "bbctl_rca_reader" {
  role = aws_iam_role.bbctl_rca_reader.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "elasticloadbalancing:Describe*",
        "ec2:Describe*",
        "logs:Get*", "logs:Filter*", "logs:Describe*",
        "ssm:DescribeInstanceInformation",
        "ssm:GetCommandInvocation",
        "ssm:SendCommand"
      ]
      Resource = "*"
    }, {
      Effect = "Allow"
      Action = "ssm:SendCommand"
      Resource = [
        "arn:aws:ssm:*::document/AWS-RunShellScript"
      ]
      Condition = {
        StringEquals = {
          "ssm:resourceTag/Owner" = "blackbuck"
        }
      }
    }]
  })
}
```

Apply to all 4 accounts. Trust principal = bbctl-rca EC2 instance role
(currently `bbctl-rca-host`).

bbctl-rca host role gets:
```json
{
  "Effect": "Allow",
  "Action": "sts:AssumeRole",
  "Resource": [
    "arn:aws:iam::<zinka-id>:role/BBCTLRcaReadOnly",
    "arn:aws:iam::<bbfinserv-id>:role/BBCTLRcaReadOnly",
    "arn:aws:iam::<divum-id>:role/BBCTLRcaReadOnly",
    "arn:aws:iam::<tzf-id>:role/BBCTLRcaReadOnly"
  ]
}
```

## Service repos — no clones

Today: `jenkins_pipeline` + `InfraComposer` cloned to `/opt/bbctl-rca/repos/`.
Service repos (alchemist, fms-*, demand, etc.) NOT cloned.

Going forward: keep that. For the rare RCA that needs service code,
LLM calls `github_read_file("alchemist", "src/main/...", ref=<COMMIT_ID>)`
which fetches via GitHub raw API. ~200ms per call vs local disk, but:
- No 40-repo sync infra
- Always reads the COMMIT_ID actually deployed (not whatever's on disk)
- GitHub rate-limit (5000 req/hr per PAT) more than enough for ~50 RCAs/day

## Cost / latency projections

Per-RCA estimates after Option C:

| Class | Today | After |
|---|---|---|
| compliance | $0.02 / 5s | $0.05 / 15s |
| parse_error | $0.02 / 5s | $0.04 / 12s |
| aws_limit | $0.02 / 5s | $0.04 / 10s |
| java_runtime | $0.10 / 30s | $0.10 / 30s (no change) |
| health_check | $0.08 / 25s | $0.12 / 35s (more AWS calls) |
| canary_fail | $0.10 / 30s | $0.10 / 30s |

Average per-RCA: ~$0.06–0.08 (vs $0.04 today). Cap stays at $0.20.

## Implementation phases

### Phase 1 — System-prompt + tool list (no code yet) [READY TO START]
- Rewrite `prompts/rca_agent_system.md` to handle all 7 error classes
  (compliance / parse_error / aws_limit / java_runtime / health_check /
  canary_fail / scm) — each with mandatory cross-check rules.
- Document all new tool schemas in OpenAI function-calling format.

### Phase 2 — New tools (Jira / GitHub / runbook) [NO AWS YET]
- `mcp_tools.py`: add `jira_get_ticket`, `github_get_commit`, `github_find_pr_for_commit`, `github_read_file`, `github_recent_commits`, `read_runbook`.
- Reuse existing `jira_client.py` / `github_client.py` modules where
  possible (currently called from `tool_context.py` to pre-fetch).
- Cache: per-RCA dict, so the same `jira_get_ticket(MB-7545)` called
  twice = one API call.

### Phase 3 — Route all classes to agent.py [BREAKING CHANGE]
- `main.py` `_run_rca`: drop the "compliance/aws_limit/parse_error → one-shot"
  branch. Always call `run_agent_rca`.
- Strip pre-fetched Jira/GitHub/runbook blocks from `_build_tool_context`.
- Keep `service.lookup` + `source.trace` HINTS + `log_window`.
- Rollout: env var `BBCTL_RCA_FORCE_AGENT_MODE=1` to opt-in for testing;
  flip default once validated.

### Phase 4 — AWS Terraform + cross-account assume-role [INFRA WORK]
- Author `InfraComposer/module/bbctl_rca_reader/`.
- Coordinate with infra team to apply across 4 accounts.
- Update bbctl-rca host role with the 4 assume-role permissions.
- Test STS assume + describe call against each account.

### Phase 5 — AWS tools [NEEDS PHASE 4 COMPLETE]
- `aws_describe_target_health`, `aws_describe_target_group`,
  `aws_describe_instance`, `aws_describe_listener_rule`, `aws_run_ssm_command`.
- All use `boto3` Session with sts.assume_role per account.
- Whitelist `aws_run_ssm_command` command set strictly.

### Phase 6 — Claude code-review integration
- New module `bbctl_rca/claude_review.py` using `anthropic` SDK.
- Two integration points:
  1. **Post-RCA validator**: after agent emits final JSON, call Claude
     to verify each `evidence[].source` line actually exists and matches
     the snippet. Drop any hallucinated citations. Optionally adjust
     `confidence` downward.
  2. **As-a-tool**: expose `claude_code_review` in the agent's tool
     list so the LLM can request a code-quality second opinion on
     suggested fixes mid-loop.
- Auth: `ANTHROPIC_API_KEY` env var via systemd drop-in.
- Model: `claude-sonnet-4-6` for cost (each review = ~1K tokens in,
  500 out, ~$0.003/review).

### Phase 7 — Tests + dashboard surfacing
- Sample-webhook test bench: replay last 30 days of audit logs through
  the new agent path, diff RCAs against current.
- Dashboard: show "Agent mode: X iters, Y tool calls, $Z" badge per RCA.

### Phase 8 — Cleanup
- Delete `llm.py:run_rca_openai` one-shot path once Phase 3 stable for 2 weeks.
- Move `_build_tool_context` boot-pack-only assembly to `agent.py` (it's
  the only consumer now).

## Open questions for sign-off

1. **AWS account IDs** — need exact 12-digit IDs for zinka, bbfinserv, divum, tzf.
2. **bbctl-rca host role ARN** — what's the current EC2 instance role? Need it for the trust policy.
3. **Claude code-review mode**:
   - (a) post-RCA validator only, OR
   - (b) also expose as a tool the LLM can call mid-loop?
   - (b) is more agentic, costlier, slower. (a) is cheaper safety net.
4. **Rollout strategy**: env-flag opt-in for compliance class first (test 2 weeks), then default-on for all, then delete one-shot. Or all-at-once? Prefer phased.
5. **Service-repo source fetching**: `github_read_file` via PAT is fine for read-only. Want me to scope down `jenkins-git-bb` PAT to read-only on service repos, or use a separate PAT?
6. **SSM `aws_run_ssm_command` whitelist** — confirm the safe-command set: `tail`, `ss -tlnp`, `curl localhost:*`, `systemctl status`, `cat /var/log/blackbuck/*.log`, `ls`, `journalctl -n N -u <svc>`. Anything else?

## Acceptance criteria

- [ ] Build 15 (compliance) RCA goes through agent loop in trace
  (visible `--- ITER 0 RESPONSE ---` with tool_calls, not direct final JSON).
- [ ] Compliance RCA cites Jira ticket data via tool fetch, not primer.
- [ ] Health-check RCA queries AWS target health via tool, includes
  live `state=unhealthy reason=...` in evidence.
- [ ] Total RCA cost stays under $0.20 cap for all classes.
- [ ] Average RCA cost ≤ $0.10.
- [ ] All 7 error classes pass end-to-end smoke test (sample webhook → final JSON).
- [ ] Trace file shows real LLM agency (≥2 distinct tool kinds called per RCA).
