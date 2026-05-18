# bbctl-rca — High-Level Flow (Option C, agent-only)

**Status:** locked, ready for Phase 1 implementation.

The LLM drives every RCA from a minimal boot-pack (log + meta + service lookup).
Server's only role is to run the tools the LLM asks for and stitch results
back into the conversation. No pre-fetched Jira/GitHub/runbook data, no
pre-classifier, no pre-detected failed stage.

---

## 1. End-to-end flow at 30,000 ft

```
┌────────────────────────────────────────────────────────────────┐
│  Jenkins build fails                                           │
│  post { failure | unstable | always-NOT_BUILT } block fires    │
│  triggerRcaWebhook.groovy → POST jenkins-rca.jinka.in/v1/rca   │
└───────────────────────────┬────────────────────────────────────┘
                            ▼
┌────────────────────────────────────────────────────────────────┐
│  bbctl-rca server (FastAPI)                                    │
│   1. Pull fresh log:   wfapi/describe + consoleText            │
│   2. Pull build meta:  /api/json                               │
│   3. Read service.lookup from local config.json                │
│   4. git fetch --depth 1 + reset --hard on jenkins_pipeline    │
│      + InfraComposer (so LLM sees latest code)                 │
│   5. Build BOOT-PACK = { log + meta + service.lookup }         │
└───────────────────────────┬────────────────────────────────────┘
                            ▼
┌────────────────────────────────────────────────────────────────┐
│  Agent loop                                                    │
│   POST OpenAI /v1/chat/completions                             │
│   model=gpt-4.1, tools=[19 functions], tool_choice=auto        │
│   messages=[system_prompt, boot-pack]                          │
└───────────────────────────┬────────────────────────────────────┘
                            ▼
                  ┌─────────┴─────────┐
                  │  LLM responds:    │
                  │   tool_calls or   │
                  │   final JSON?     │
                  └────┬─────────┬────┘
       tool_calls=[…] /            \  finish_reason=stop
                  │                  │
                  ▼                  ▼
        ┌─────────────────┐   ┌──────────────────────┐
        │ Execute each    │   │ Final JSON received  │
        │ tool locally    │   │                      │
        │ Append result   │   │ Post-RCA validator:  │
        │ → loop ITER+1   │   │  - evidence path     │
        └─────────────────┘   │    existence check   │
                              │  - openai gpt-4o-mini│
                              │    sanity check      │
                              │  - drop hallucinated │
                              │    citations         │
                              └──────────┬───────────┘
                                         ▼
                              ┌──────────────────────┐
                              │ Write audit JSON +   │
                              │ per-build trace.txt  │
                              │ Render dashboard HTML│
                              │ Return JSON to       │
                              │ Jenkins post-block   │
                              └──────────┬───────────┘
                                         ▼
                              ┌──────────────────────┐
                              │ Pipeline echoes      │
                              │ summary + dashboard  │
                              │ link to console      │
                              └──────────────────────┘
```

---

## 2. Boot-pack — exactly 3 blocks

```
=== SYSTEM PROMPT (~50 lines, generic method) ===
You are an SRE root-cause analyzer.

Method:
1. Read log_window. Identify failed stage from [Pipeline] { markers.
2. ALWAYS call get_jenkins_job_config + repo_read_file on jenkins_pipeline
   stage source FIRST. Pipeline source is mandatory evidence.
3. Discover what kind of failure this is. If unsure, call list_runbooks(),
   then read_runbook(<best_match>) to get the drill plan.
4. Follow the runbook's drill plan: jira / github / aws / repo tools.
5. Iterate until you can name file:line, ticket field, or AWS resource
   state as the cause. No confidence threshold — keep going until clear.
6. Emit final JSON.

Output schema: {summary, failed_stage, error_class, root_cause,
evidence[], suggested_fix, suggested_commands[], needs_deeper}

Evidence MUST include ≥1 jenkins_pipeline/<file>:<line>. No invented paths.

=== USER MESSAGE (boot-pack body) ===
## log_window
<last ~200 lines from wfapi/describe + consoleText>

## build_meta
{job: ..., build_id: ..., result: FAILURE, url: ..., timestamp: ...}

## service.lookup(<svc>)
{aws_account: "zinka", aws_region: "ap-south-1",
 rule_arn: "arn:aws:elasticloadbalancing:...",
 target_port: 8080,
 log_path: "/var/log/blackbuck/alchemist.log",
 git_repo: "alchemist", ...}
```

**Not in boot-pack** (LLM calls tools for these):
- error_class — LLM classifies from log
- failed_stage — LLM scans log markers
- jira.tickets — `jira_get_ticket` tool
- github.commits — `github_get_commit` tool
- runbook content — `read_runbook` tool
- source.trace hints — `repo_search` tool

---

## 3. Tool catalog (19 tools, organized by domain)

```
┌─────────────────────────────────────────────────────────────────┐
│  JIRA (2)                                                       │
│   jira_get_ticket(key)                                          │
│   jira_search(jql, max=10)                                      │
├─────────────────────────────────────────────────────────────────┤
│  GITHUB (4)                                                     │
│   github_get_commit(repo, sha)                                  │
│   github_find_pr_for_commit(repo, sha)                          │
│   github_read_file(repo, path, ref, start?, end?)               │
│   github_recent_commits(repo, branch, n)                        │
├─────────────────────────────────────────────────────────────────┤
│  LOCAL REPOS (4) — jenkins_pipeline + InfraComposer             │
│   repo_read_file(repo, path, start, end)                        │
│   repo_search(repo, query)                                      │
│   repo_find_function(repo, name)  ← with vars/ convention       │
│   repo_recent_commits(repo, n)                                  │
├─────────────────────────────────────────────────────────────────┤
│  JENKINS (1)                                                    │
│   get_jenkins_job_config(job)                                   │
├─────────────────────────────────────────────────────────────────┤
│  RUNBOOKS (2)                                                   │
│   list_runbooks()                                               │
│   read_runbook(name)                                            │
├─────────────────────────────────────────────────────────────────┤
│  AWS CROSS-ACCOUNT (5) — STS AssumeRole BBCTLRcaReadOnly        │
│   aws_describe_target_health(target_group_arn)                  │
│   aws_describe_target_group(target_group_arn)                   │
│   aws_describe_instance(instance_id)                            │
│   aws_describe_listener_rule(rule_arn)                          │
│   aws_run_ssm_command(instance_id, cmd)  ← whitelisted only     │
├─────────────────────────────────────────────────────────────────┤
│  SANITY (1)                                                     │
│   code_review(diff_or_path, prompt)  ← uses gpt-4o-mini         │
└─────────────────────────────────────────────────────────────────┘
```

LLM chooses any combination, any sequence, any iter. Server executes
without commentary.

---

## 4. Mandatory pipeline cross-check (every RCA)

Hardcoded in system prompt:

```
For EVERY RCA, regardless of error type:

  1. get_jenkins_job_config(job)              → find scriptPath
  2. repo_read_file("jenkins_pipeline",
                    <scriptPath>, ...)        → see stage block
  3. (often) repo_find_function or repo_read_file on vars/<helper>
                                              → see helper impl

  Final evidence[] MUST contain ≥1 entry whose source is
  "jenkins_pipeline/<file>:<line>".
```

Enforced server-side by post-RCA validator: if no `jenkins_pipeline/...`
in `evidence[]`, validator rejects and asks LLM to re-emit.

---

## 5. Per-class drill walkthroughs

### 5a. Compliance (Jira gate rejection)

```
Log says:
  "Stage 'Jira Details' status=FAILED
   Compliance: Jira ticket MB-7545 is not READY FOR RELEASE
   (current: Done)"

ITER 0 ─ LLM identifies: Jira-related, failed stage = "Jira Details"
         tool_calls = [
           get_jenkins_job_config("create-quick-infra-devops-test"),
           list_runbooks()
         ]

ITER 0 results:
  job_config: { script_path: "create-quick-infra.groovy", ... }
  runbooks: [ compliance, java_runtime, health_check, … ]

ITER 1 ─ LLM picks compliance.md, reads pipeline source
         tool_calls = [
           read_runbook("compliance"),
           repo_read_file("jenkins_pipeline",
                          "create-quick-infra.groovy", 320, 340),
           jira_get_ticket("MB-7545")
         ]

ITER 1 results:
  compliance.md: <drill plan + 5 failure modes>
  create-quick-infra.groovy:330: JiraDetails(SERVICE,COMMIT_ID,Jira-Ticket)
  MB-7545: { status: "Done", assignee: "Yaseen N A", ... }

ITER 2 ─ LLM confirms via vars/JiraDetails.groovy
         tool_calls = [
           repo_read_file("jenkins_pipeline",
                          "vars/JiraDetails.groovy", 30, 80)
         ]

ITER 2 results:
  vars/JiraDetails.groovy:42: status check that rejected MB-7545

ITER 3 ─ Final JSON emitted (clear RCA reached)
         evidence:
           - jenkins_log
           - jenkins_pipeline/create-quick-infra.groovy:330
           - jenkins_pipeline/vars/JiraDetails.groovy:42
           - jira:MB-7545 (status=Done)
         suggested_fix:
           Finding: Yaseen's ticket MB-7545 is Done; needs READY FOR RELEASE.
           Action:  Open Jira MB-7545 → transition status.
           Verify:  re-run pipeline.

Total: 3 iters, 8 tool calls, ~$0.10, ~30s.
```

### 5b. Java runtime (code error)

```
Log says:
  "groovy.lang.MissingMethodException: No signature of method:
   JiraDetails.call() is applicable for argument types: (String)"

ITER 0 ─ LLM identifies: Groovy exception
         tool_calls = [
           get_jenkins_job_config(job),
           list_runbooks()
         ]

ITER 1 ─ LLM reads java_runtime runbook + pipeline source
         tool_calls = [
           read_runbook("java_runtime"),
           repo_read_file("jenkins_pipeline",
                          "create-quick-infra.groovy", 320, 340),
           repo_find_function("jenkins_pipeline", "JiraDetails")
         ]

ITER 2 ─ LLM reads helper impl
         tool_calls = [
           repo_read_file("jenkins_pipeline",
                          "vars/JiraDetails.groovy", 1, 30),
           repo_recent_commits("jenkins_pipeline", 10)
         ]

ITER 3 ─ Final JSON
         root_cause: "Call site at create-quick-infra.groovy:330 passes
                      1 arg; impl at vars/JiraDetails.groovy:9 requires 3."

Total: 3 iters, 6 tool calls, ~$0.13, ~35s.
```

### 5c. Health check (AWS infra)

```
Log says:
  "Health Status failed to move to healthy within the time limit.
   target_group=preprod-alchemist-tg-green instance=i-0a1b2c3d"

ITER 0 ─ LLM identifies: ALB health check fail
         tool_calls = [
           get_jenkins_job_config(job),
           list_runbooks(),
           aws_describe_target_health(<tg_arn from service.lookup>)
         ]

ITER 0 results:
  target_health: { state: "unhealthy", reason: "Target.Timeout" }

ITER 1 ─ LLM reads health_check runbook + pipeline source + AWS state
         tool_calls = [
           read_runbook("health_check"),
           repo_read_file("jenkins_pipeline",
                          "vars/nonwebdeploy.groovy", 1, 80),
           aws_describe_instance("i-0a1b2c3d"),
           aws_describe_target_group(<tg_arn>)
         ]

ITER 2 ─ LLM probes the instance via SSM
         tool_calls = [
           aws_run_ssm_command("i-0a1b2c3d", "ss -tlnp | grep 8080"),
           aws_run_ssm_command("i-0a1b2c3d",
                               "tail -n 100 /var/log/blackbuck/alchemist.log"),
           repo_read_file("jenkins_pipeline",
                          "resources/healthy.sh", 1, 60)
         ]

ITER 2 results:
  ss output: <empty>                     ← nothing listening on 8080
  log tail: "Address already in use: bind  Port 8080"

ITER 3 ─ Final JSON
         root_cause: "Service crash on bind. Port 8080 held by stale
                      process. ALB probe timed out → unhealthy."
         evidence:
           - jenkins_log
           - jenkins_pipeline/vars/nonwebdeploy.groovy:45
           - jenkins_pipeline/resources/healthy.sh:30
           - aws:target_health state=unhealthy reason=Target.Timeout
           - aws:ssm(i-0a1b2c3d, "ss") = empty
           - aws:ssm(i-0a1b2c3d, "tail") "Address already in use"
         suggested_fix:
           Finding: Port 8080 held by stale process on i-0a1b2c3d.
           Action:  `bbctl shell i-0a1b2c3d` → `sudo ss -tlnp | grep 8080`
                    → kill holding PID → re-deploy.
           Verify:  ALB target health=healthy in console.

Total: 3 iters, 10 tool calls, ~$0.20, ~50s.
```

### 5d. Unknown (no runbook matches)

```
Log says:
  "Pipeline halted: <weird-never-seen-error-string>"

ITER 0 ─ LLM scans log, no clear class signal
         tool_calls = [
           get_jenkins_job_config(job),
           list_runbooks()
         ]

ITER 1 ─ LLM reads unknown.md (generic drill plan)
         tool_calls = [
           read_runbook("unknown"),
           repo_read_file("jenkins_pipeline", <scriptPath>, ...),
           repo_recent_commits("jenkins_pipeline", 10),
           repo_recent_commits("InfraComposer", 10)
         ]

ITER 2-N ─ LLM iterates per unknown.md plan: regex log → match groups
           → call tool per match (jira_get_ticket, aws_describe_instance,
           repo_read_file). Keeps going.

ITER N ─ Final JSON emitted with either:
         - clear RCA (lucky), confidence implicit
         - needs_deeper=true + detailed "what I tried, what's missing"

Total: up to 25 tool calls / 180s / $5 (safety cap).
```

---

## 6. Stopping rules (LLM-driven, server safety nets)

| Stop condition | Who decides | What happens |
|---|---|---|
| LLM finds clear RCA | LLM | emits final JSON, loop ends |
| MAX_TOOL_CALLS = 25 | Server | force JSON response, set needs_deeper=true |
| WALL_CLOCK = 180s | Server | force JSON response, set needs_deeper=true |
| COST_HARD_KILL = $5 | Server | panic killswitch — only fires on bug, not normal |

No confidence-threshold bailing. LLM iterates until clear or safety cap hits.

---

## 7. Post-RCA validators (server-side, automatic)

After LLM emits final JSON, server runs 2 validators in sequence before
returning to Jenkins:

```
1. Evidence path existence check (pure Python)
     - For every evidence[i] with source = "<repo>/<file>:<line>":
         - confirm /opt/bbctl-rca/repos/<repo>/<file> exists
         - confirm <line> is within file's line count
     - Drop entries that fail either check
     - If all evidence dropped, append note to root_cause

2. Code-review sanity check (gpt-4o-mini)
     - For each evidence[i] with source = "<repo>/<file>:<line>":
         - read 5 lines around <line> from disk
         - ask gpt-4o-mini: "does this snippet match the claim
           in evidence[i].snippet?"
     - Drop entries where mini says "doesn't match"
     - Add validator_notes[] to final JSON
```

Both validators are pre-write — final JSON written to audit + dashboard
already has hallucinated citations filtered.

---

## 8. Output artifacts per RCA

```
/opt/bbctl-rca/audit/<job>_<build>.json
    - The validated final JSON
    - tokens_used, cost_usd, tool_call_count, iter_count
    - repos_freshness (which SHA was checked out for jenkins_pipeline/InfraComposer)
    - request_id, model_used, validator_notes

/tmp/bbctl-rca-trace-<job>-<build>.txt
    - Per-build agent transcript
    - Boot-pack + full system message
    - Every ITER REQUEST (model, tools, messages array)
    - Every ITER RESPONSE (raw model_dump)
    - Every TOOL execution (args + result)
    - Final output summary

/tmp/bbctl-rca-last-trace.txt
    - Symlink-ish: latest run only (convenience)

Dashboard (web UI):
    GET /rca/v1/dashboard            → table of recent RCAs
    GET /rca/v1/dashboard/<job>      → per-pipeline build list
    GET /rca/v1/report/<request_id>  → rendered RCA HTML
    GET /rca/v1/dashboard/<job>/<build>/trace.txt  → download trace
```

---

## 9. Cost / latency (no caps gating, LLM iterates freely)

| Class | Iters | Tool calls | Cost | Wall clock |
|---|---|---|---|---|
| compliance | 3-4 | 6-8 | $0.08-0.12 | 25-35s |
| parse_error | 2-3 | 4-6 | $0.06-0.09 | 18-25s |
| aws_limit | 2-3 | 4-6 | $0.06-0.08 | 18-25s |
| java_runtime | 3-4 | 5-8 | $0.10-0.15 | 30-40s |
| health_check | 3-5 | 8-12 | $0.15-0.22 | 40-60s |
| canary_fail | 3-5 | 7-10 | $0.12-0.18 | 35-45s |
| terraform | 3-4 | 6-9 | $0.12-0.16 | 35-45s |
| scm | 2-3 | 4-6 | $0.07-0.10 | 20-30s |
| unknown (worst) | up to 25 | up to 25 | up to $0.50 | up to 120s |

Average ~$0.12/RCA. Burst worst case ~$0.50.

---

## 10. Visibility for manager

Every RCA generates a trace file that contains:

- **REQUEST** per iter: full messages array + tool list sent to OpenAI
- **RESPONSE** per iter: raw `model_dump()` of OpenAI SDK reply
- **TOOL** per execution: args + first 8K of result + length-of-result

So manager can answer "did the LLM actually call jira_get_ticket?" by reading
one file. Trace is the audit trail.

```bash
# Pull any historical RCA's trace
scp ubuntu@host:/tmp/bbctl-rca-trace-<job>-<build>.txt .

# Or browser:
https://jenkins-rca.jinka.in/rca/v1/dashboard/<job>/<build>/trace.txt
```

---

## 11. The 8 implementation phases

(see `agent_mode_migration_plan.md` for full deliverables)

```
Phase 1 — System prompt + 10 runbook MDs + 19 tool schemas   ◄ NEXT
Phase 2 — Jira/GitHub/runbook tool implementations
Phase 3 — Route all classes to agent.py (kill one-shot path)
Phase 4 — AWS IAM (already DONE manually in console)
Phase 5 — AWS tools (boto3 + STS AssumeRole + SSM whitelist)
Phase 6 — code_review tool + post-RCA validators
Phase 7 — Tests + dashboard trace download endpoint
Phase 8 — Cleanup (delete one-shot path, refactor _build_tool_context)
```

Each phase = independent commit on `feature/bbctl-rca-agent-only`.
Each verified before next starts. Final PR after Phase 8.
