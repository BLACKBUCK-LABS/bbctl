# bbctl-rca — Jenkins Pipeline Auto-RCA

Automated Root Cause Analysis service for Jenkins `stagger-prod-plus-one` pipeline failures. On every failed build, Jenkins POSTs a signed webhook to this service; the service fetches the console log via Jenkins REST API, classifies the failure, enriches with context (Jira / GitHub / NewRelic / runbook docs / repo source), calls an LLM, and returns structured RCA JSON that's printed back into the Jenkins console.

---

## Architecture

```
┌──────────────┐   POST signed webhook    ┌───────────────────────┐
│   Jenkins    ├─────────────────────────▶│  ALB                  │
│ (post.failure)│  HMAC-SHA256 sig        │  bbctl.blackbuck.com  │
└──────────────┘                          │  /rca/v1/rca/webhook  │
       ▲                                  └──────────┬────────────┘
       │ RCA JSON                                    │ :7070
       │ (printed via                                ▼
       │  renderRca)                       ┌───────────────────────┐
       └──────────────────────────────────│  bbctl-ec2:7070       │
                                          │  FastAPI/uvicorn      │
                                          │  bbctl-rca service    │
                                          └──────────┬────────────┘
                                                     │
              ┌──────────────────────────────────────┼──────────────────────────┐
              ▼                                      ▼                          ▼
    ┌──────────────────┐                  ┌─────────────────┐         ┌──────────────────┐
    │ Jenkins REST API │                  │ OpenAI / Gemini │         │ Jira / GitHub /  │
    │ (console log)    │                  │  (LLM)          │         │ NewRelic / docs  │
    └──────────────────┘                  └─────────────────┘         └──────────────────┘
```

**Pipeline flow** (server side, `bbctl_rca/main.py::_run_rca`):

1. Verify HMAC signature (`X-Bbctl-Signature: sha256=...`)
2. Fetch console log via Jenkins REST API
3. Sanitize log (regex-based redactions for secrets/credentials)
4. Classify error → one of: `compliance`, `canary_fail`, `canary_script_error`, `aws_limit`, `parse_error`, `java_runtime`, `scm`, `ssm`, `network`, `dependency`, `health_check`, `timeout`, `unknown`
5. Build tool-context (class-specific): Jira tickets, GitHub commits, NewRelic slow txns, runbook excerpts, source.trace hits, service config from `repos/jenkins_pipeline/resources/config.json`
6. Call LLM (default `gpt-4o`, JSON mode, temp 0.1)
7. Verify each evidence citation against repos on disk
8. Cache 24h in diskcache; record audit log
9. Return RCA JSON to Jenkins, which renders the boxed console block

---

## EC2 layout (bbctl-ec2 = 10.34.120.223)

**Single source of truth**: `/home/ubuntu/project/bbctl` is the git clone. `/opt/bbctl-rca` is a symlink → `/home/ubuntu/project/bbctl`.

```
/opt/bbctl-rca           → symlink → /home/ubuntu/project/bbctl
/home/ubuntu/project/bbctl/
├── bbctl_rca/           # Python service (FastAPI)
├── prompts/             # LLM system + few-shot prompts
├── docops/              # Class-specific runbook docs (loaded into prompt)
├── classifier_rules.yml # Ordered error-class regex rules
├── sanitize_rules.yml   # Log redaction patterns
├── infra/scripts/bbctl-rca-start.sh   # systemd ExecStart target (must be +x)
├── repos/               # External clones (read-only) for source.trace + config.json
│   ├── jenkins_pipeline/
│   └── InfraComposer/
├── docs/                # Project documentation (this file lives here too)
└── .venv/               # Python venv (not in git)
```

**Why the symlink**: previously the code lived in two places (laptop git repo + `/opt/bbctl-rca/` runtime copy) and drifted whenever someone forgot to manually copy. Symlinking collapses them: `git pull` is the one and only deploy step.

**Repos at `repos/`**: these are external git clones (jenkins_pipeline, InfraComposer) used by:
- `mcp_tools.service_lookup()` to read `resources/config.json` (NewRelic appName, ASG, etc.)
- `mcp_tools.repo_read_file()` for source-code citations
- Periodic refresh via cron / on-demand sync (e.g. `git fetch && git reset --hard origin/<branch>`)

`repos/*/` should be `.gitignore`d in the parent `bbctl` repo so external clone state doesn't pollute.

---

## Service operations

### systemd unit

```
/etc/systemd/system/bbctl-rca.service
User=ubuntu
ExecStart=/opt/bbctl-rca/infra/scripts/bbctl-rca-start.sh
```

The start script fetches secrets from AWS Secrets Manager (`bbctl-rca/prod` in `ap-south-1`) using the instance's IAM role, exports them as env vars, then launches uvicorn on `0.0.0.0:7070` with 2 workers.

### Common commands

```bash
# status / restart / stop
sudo systemctl status bbctl-rca --no-pager | head -10
sudo systemctl restart bbctl-rca
sudo systemctl stop bbctl-rca

# tail logs (incl. tracebacks)
sudo journalctl -u bbctl-rca -f
sudo journalctl -u bbctl-rca -n 200 --no-pager

# local health checks
curl -sf http://127.0.0.1:7070/healthz
curl -sf http://127.0.0.1:7070/rca/healthz
# public (through ALB)
curl -sf https://bbctl.blackbuck.com/rca/healthz
```

### Deploy a code change

```bash
# On laptop
cd /Users/hariharan/cost_exp_aibot/BBCTLLLM/bbctl
# ...edit code...
git commit -m "fix(rca): ..."
git push

# On bbctl-ec2
cd /home/ubuntu/project/bbctl
git pull
sudo systemctl restart bbctl-rca
sudo journalctl -u bbctl-rca -n 30 --no-pager   # confirm clean start
```

### Refresh external repos under `repos/`

```bash
cd /opt/bbctl-rca/repos/jenkins_pipeline
sudo git fetch && sudo git reset --hard origin/master   # or relevant branch
```

### Cache & dedup

- 24h RCA result cache (diskcache) lives in `/var/cache/bbctl-rca/`. Same `(job, build)` returns prior RCA without re-calling LLM. Marked `from_cache: true` in response.
- 60s dedup cache for in-flight requests.
- To force fresh RCA: call `/v1/rca` directly with `{"deep": true}`.

### Force fresh RCA — full cache wipe

When testing prompt/classifier/sanitizer changes against a *previously-analyzed* build, the 24h cache returns the stale RCA even with `deep:true` in some paths (deep bypasses `get_rca` but not all `is_duplicate` short-circuits). For a guaranteed clean run:

```bash
# Clean restart with cache wipe
sudo systemctl stop bbctl-rca && \
  sudo rm -rf /var/cache/bbctl-rca/* && \
  sudo systemctl start bbctl-rca
sleep 2
curl -sf http://127.0.0.1:7070/healthz && echo " OK"

# Payload-file pattern — easier to edit/reuse than inline -d
echo '{"job":"stagger-prod-plus-devops-test","build":25,"deep":true}' > /tmp/payload_dt25.json
curl -X POST http://localhost:7070/v1/rca \
  -H 'Content-Type: application/json' \
  -d @/tmp/payload_dt25.json | jq
```

Typical latency: 30-60s (Jenkins log fetch + sanitize + LLM call). Typical cost: $0.04-0.06 with `gpt-4o` (bumped from `gpt-4o-mini` for stronger reasoning on multi-step compliance / canary cases).

To inspect specific fields without scrolling the whole JSON:
```bash
curl ... | jq '.evidence, .root_cause'
curl ... | jq '.suggested_fix, .suggested_commands'
curl ... | jq '.error_class, .failed_stage, .confidence, .tokens_used, .cost_usd'
```

---

## Jenkins integration

### Pipeline wiring (`jenkins_pipeline_master/main_stagger_prod_plus_one.groovy`)

In the `post.failure` block, after `rollbackMain(...)`:

```groovy
script {
    rollbackMain("Single Job Rollback", params.SERVICE)

    // ============ BB-AI auto-RCA (Phase A — console + build description only) ============
    // Non-fatal: any error here must not affect rollback or VictorOps alert below.
    try {
        triggerRcaWebhook()
    } catch (Exception e) {
        echo "[BB-AI] non-fatal error: ${e.message}"
    }
    // ========================================================================================

    // ... existing VictorOps alert block continues unchanged ...
}
```

### Shared library (`vars/triggerRcaWebhook.groovy`)

Posts the signed webhook using raw `HttpURLConnection` (no `httpRequest` plugin dependency, since the HTTP Request plugin is **not** installed in the Jenkins controller). Helpers:

- `triggerRcaWebhook()` — entry point called from `post.failure`; returns parsed RCA Map for Phase B/C reuse
- `postWebhook(url, payload, sig)` — `@NonCPS`, pure-Java POST; throws on transport error
- `parseJson(text)` — `@NonCPS` wraps `JsonSlurper.parseText`
- `renderRca(rca)` — prints compact console block + sets rich `currentBuild.description` with link to HTML report
- `rcaReportUrl(requestId)` — canonical URL builder (`https://bbctl.blackbuck.com/rca/v1/report/<uuid>`); override base via `BBCTL_RCA_REPORT_BASE_URL` env
- `hmacSha256(secret, body)` — `@NonCPS` HMAC-SHA256 for request signing
- `buildAlertMessage(rca)` — one-paragraph summary for VictorOps + Slack enrichment

### Notification helper (`src/com/blackbuck/utils/Notification.groovy`)

New `rcaAlert(script, service, branch, slack_channel, rca)` static method posts the BB-AI RCA summary to the per-service Slack channel. Reuses the existing `slack-stagger-bot` Jenkins credential — no new Slack app or webhook URL needed. Orange color (`#ff8c00`) differentiates from red `Notification.failure` alerts.

### Credentials in Jenkins

- **Secret text** with ID `bbctl-webhook-secret` matching `WEBHOOK_SECRET` in AWS Secrets Manager `bbctl-rca/prod`.

### What the operator sees in the build console (current — compact)

```
╔══════════════════════════════════════════════════════════════════╗
║               Jenkins Build RCA — Powered by BB-AI               ║
╚══════════════════════════════════════════════════════════════════╝
  class:        compliance
  failed_stage: Jira Details
  summary:      Jira ticket PEB-7 is missing the 'Signed Off Commit ID'...

  Full RCA report: https://bbctl.blackbuck.com/rca/v1/report/<uuid>
  request_id:      <uuid>
```

Full RCA (Root cause, Suggested fix, Commands, Evidence, Metadata) lives at the HTML report URL — operator clicks through. Keeps Jenkins console scrollable.

Build description (sidebar) shows two compact lines:
```
BB-AI: <code>class</code> · <code>stage</code>
<trimmed summary>… Open RCA →   ← clickable link to full HTML report
```

### HTML report (`/rca/v1/report/<request_id>`)

Polished, self-contained dark-theme HTML page served by FastAPI. Same URL appears in Jenkins console, sidebar description, Slack message, and VictorOps `details`. Loaded from `bbctl_rca/templates/rca_report.html`.

**Sections (top → bottom):**
- **Sticky topbar** — title + clickable build link + anchor nav (Summary / Root cause / Fix / Commands / Evidence)
- **Hero card** — class pill (color-coded per `error_class`), stage pill, `needs_deeper` pill if set, service code chip, action pills linking to Jenkins build / Console log / Raw JSON
- **Summary** — one-line LLM-generated summary
- **Two-column grid** — Root cause | Suggested fix (Map form splits into Finding / Action / Verify dt-dd rows)
- **Suggested commands** — dark terminal-style blocks with tier pill (`safe` green / `restricted` amber), one-line rationale, syntax-highlighted command, and a Copy button (clipboard)
- **Evidence** — colored ✓/✗/? badges with source label + snippet
- **Metadata** — request_id, provider, redactions, log_window_chars, recorded_at
- **Footer** — `BB-AI · powered by bbctl-rca` + `Built by Hariharan G, DevOps`

**Design choices:**
- Dark navy palette (`#0a0f1c` bg) with subtle dot pattern, calm low-light feel
- Translucent class-colored pills with matching borders → glow effect on dark
- Inter-style system-font stack (`-apple-system`, `Segoe UI`, etc.) — zero CDN deps (works in restricted networks)
- Self-contained CSS; no Tailwind / external fonts
- Sticky frosted-glass topbar

**Endpoints:**
- `GET /rca/v1/report/<request_id>` — HTML page
- `GET /rca/v1/report/<request_id>.json` — raw audit JSON (for scripts/debugging)

---

## Configuration

### Environment variables (set by `infra/scripts/bbctl-rca-start.sh` from Secrets Manager)

| Var                     | Purpose                                              |
| ----------------------- | ---------------------------------------------------- |
| `BBCTL_JENKINS_URL`     | Jenkins controller URL                               |
| `BBCTL_JENKINS_USER`    | Jenkins API user                                     |
| `BBCTL_JENKINS_TOKEN`   | Jenkins API token                                    |
| `BBCTL_WEBHOOK_SECRET`  | HMAC secret shared with Jenkins                      |
| `BBCTL_LLM_API_KEY`     | OpenAI / Gemini API key                              |
| `BBCTL_LLM_PROVIDER`    | `openai` (default) or `gemini`                       |
| `BBCTL_RCA_URL`         | (Jenkins-side env override) full webhook URL         |
| `AWS_REGION`            | `ap-south-1`                                         |
| `BBCTL_SECRET_ID`       | `bbctl-rca/prod`                                     |

### ALB routing

- Listener: HTTPS:443 on `app/stagger-FE/...`
- Rule: host `bbctl.blackbuck.com` + path `/rca/*` → target group `bbctl-rca-tg` → bbctl-ec2:7070
- FastAPI mounts the same `APIRouter` at both `/` and `/rca/` so direct-port access (`:7070/healthz`) and ALB-routed (`/rca/healthz`) both work.

### Cost guardrails

- Per-call cost estimated from token counts (`gpt-4o`: $2.50/1M input, $10.00/1M output)
- Daily spend cap enforced via `cache.over_daily_cap()` → HTTP 429
- Cached responses (24h) skip the LLM call entirely

---

## Repo / sync history (the migration that got us here)

Before: `/opt/bbctl-rca/` was a flat copy of the Python service, manually rsynced from laptop. `/home/ubuntu/project/bbctl/` was a separate git clone used only for reading source via `repo_read_file`. Two copies drift; one fix lands in git but not in `/opt`, and a redeploy quietly breaks.

After (current state):

1. Stopped service.
2. Moved real venv from `/opt/bbctl-rca/.venv` → `/home/ubuntu/project/bbctl/.venv` (the empty `/home` venv was deleted first; venv shebangs still point to `/opt/bbctl-rca/.venv/bin/python3`, which resolves through the symlink).
3. Renamed old `/opt/bbctl-rca` → `/opt/bbctl-rca.bak.YYYYMMDD` as rollback.
4. `ln -s /home/ubuntu/project/bbctl /opt/bbctl-rca`.
5. Migrated `repos/` and `docops/` from the backup into the git repo (these are not git-tracked — `repos/` is gitignored external clones, `docops/` is doc snapshots).
6. Verified `.venv` works through the symlink (`import fastapi, openai, anthropic`).
7. Fixed `infra/scripts/bbctl-rca-start.sh` filesystem exec bit (git index was `100755` already, only filesystem perm was wrong).
8. Restarted service. Health OK.
9. Triggered test build. End-to-end Auto-RCA block rendered in Jenkins console.

Rollback (still available until backup deleted):

```bash
sudo systemctl stop bbctl-rca
sudo rm /opt/bbctl-rca
sudo mv /opt/bbctl-rca.bak.YYYYMMDD /opt/bbctl-rca
# move venv back
sudo mv /home/ubuntu/project/bbctl/.venv /opt/bbctl-rca/.venv
sudo systemctl start bbctl-rca
```

Once stable for a few days:

```bash
sudo rm -rf /opt/bbctl-rca.bak.*
```

---

## Troubleshooting

### Pipeline aborts with `NoSuchMethodError: No such DSL method 'httpRequest'`
HTTP Request plugin is not installed on Jenkins. The shared library was already migrated to `HttpURLConnection`; ensure `vars/triggerRcaWebhook.groovy` matches `bbctl/infra/jenkins/post_failure_rca.groovy` (no `httpRequest(...)` call).

### Service exits with `status=203/EXEC`
Missing exec bit on `infra/scripts/bbctl-rca-start.sh`. Fix:
```bash
chmod +x /opt/bbctl-rca/infra/scripts/bbctl-rca-start.sh
sudo systemctl restart bbctl-rca
```
Git tracks the mode (`100755`) but a fresh clone on a system where `core.filemode=false` may drop it.

### `FileNotFoundError: .../repos/jenkins_pipeline/resources/config.json`
External clones missing under `repos/`. Re-clone:
```bash
cd /opt/bbctl-rca/repos
sudo git clone <jenkins_pipeline_url> jenkins_pipeline
sudo chown -R ubuntu:ubuntu jenkins_pipeline
```

### `pydantic ValidationError: buildUrl / consoleUrl Field required`
Old `WebhookPayload` schema. Pull latest — fields are now `Optional` (default `""`), so the Jenkins groovy payload (`job/build/service` only) validates.

### `HTTP 500` from webhook
Tail logs for traceback:
```bash
sudo journalctl -u bbctl-rca -f
```
Common causes: missing/expired Jenkins API token, OpenAI quota, malformed log window, source-trace repo not cloned.

### Public health check fails but local works
ALB target group health, security group ingress, or HTTPS cert. Quick checks:
```bash
curl -sf http://127.0.0.1:7070/healthz                # service alive
curl -sf https://bbctl.blackbuck.com/rca/healthz      # ALB path works
# AWS console: target group bbctl-rca-tg → Targets → status
```

### Force re-analysis (skip 24h cache)
```bash
curl -X POST http://127.0.0.1:7070/v1/rca \
  -H 'Content-Type: application/json' \
  -d '{"job": "stagger-prod-plus-one", "build": 12345, "deep": true}'
```

---

## Error classes — current behavior

| Class | Trigger pattern | Tool context fetched | Runbook |
| --- | --- | --- | --- |
| `compliance` | `Signed Off commit id` / `Compliance:` / `COMMIT_ID does not match` | Jira ticket (incl. `customfield_10973` Signed Off Commit ID) + GitHub commits for both SHAs | `JiraDetailsCompliance.md` |
| `canary_script_error` | `Traceback...canary.py` / `TypeError ... round ... NoneType` | `canary.py:LINE±10` from deepest traceback frame + NewRelic-data hint | `StaggerProdPlusOneDeploy.md` |
| `canary_fail` | `Rollout back as Canary failed` / `Rolling Back as Result !=0` / `canary_run_status: "Fail"` | canary stage-by-stage analysis (5/20/50/100% pass/fail) + canary.groovy + judge logic + NR slow tx | `StaggerProdPlusOneDeploy.md` |
| `health_check` | `Health Status failed to move to healthy` / `iterations: unhealthy` / `Error in Deploy_i-` | Parsed TG ARN/name + instance ID + region from `healthy.sh` line + service `log_path`/port/health endpoint + NR slow tx (if any) | `HealthCheckFailure.md` |
| `aws_limit` | `TooMany*` / `LimitExceeded` / `QuotaExceeded` | — | `AwsLimitTroubleshoot.md` |
| `parse_error` | `parse error:` / `jq: error` / `Invalid numeric literal` | `createGreenInfra.groovy:330-345` | `ConfigJsonParseError.md` |
| `java_runtime` | `java.lang.*Exception/Error` (must have full FQN — bare `OutOfMemoryError` no longer matches JVM startup flags) | source.trace hits | — |
| `scm` | `git fetch failed` / `Authentication failed.*github` / `fatal: repository` | GitHub commits | `SCMTroubleshoot.md` |
| `health_check`, `network`, `ssm`, `dependency`, `timeout`, `unknown` | various | source.trace + jira (if ticket keys in log) | — |

Classifier rule order matters — first match wins. `health_check` is above `java_runtime` so ALB-probe failures aren't masked by stray Java class references.

### `health_check` class specifics

**Org access pattern**: instance access goes through `bbctl` (org-standard CLI), NOT raw `ssh`. RCA action items are templated to use:
- `bbctl shell <instance-id>` — interactive shell on the failing instance
- `bbctl run <instance-id> -- '<cmd>'` — one-shot command (preferred for `suggested_commands` array)

The LLM is instructed to substitute the real instance_id from `health_check.target` and never emit `<instance-id>` placeholders. SSM and raw ssh are mentioned only as fallbacks.

When Jenkins `Deploy` stage runs `healthy.sh <tg-arn> <region> <instance-id> <env>` and the ALB target group probe stays unhealthy for the full poll window (typically 50 × ~6s = 5 min), pipeline aborts with:

```
Health Status failed to move to healthy within the time limit
Error in Deploy_i-<instance-id>: script returned exit code 1
```

Tool context auto-populated for the LLM:
- `health_check.target`: `target_group_name`, `target_group_arn`, `instance_id`, `region`, `env`, `failed_iterations`
- `health_check.service_config`: `log_path`, `service_port` / `port`, `health_check_path`, `health_check_port` from `config.json`
- `newrelic.slow_transactions`: NR app-name candidates for the deploy window (if empty → service never reported a single txn → likely never started)
- `health_check.guide`: 6 ordered likely causes (service didn't start / port mismatch / health endpoint 5xx / SG block / slow boot vs threshold / dependency unreachable)
- `docs.HealthCheckFailure.md` runbook content

LLM is instructed to **never** cite SSH host-key warnings or NewRelic `Application X does not exist` as root cause — both are non-fatal upstream noise the pipeline tolerates via SSM fallback / unregistered apps.

---

## Phase roadmap

- **Phase A (LIVE)** — Auto-RCA prints to console + build description on failure. Webhook is non-fatal; nothing about the existing alert flow changed.
- **Phase B (LIVE)** — VictorOps incident `message` field now includes `buildAlertMessage(rca)` so on-call sees the RCA summary inside the page itself. RCA fields (`rcaErrorClass`, `rcaFailedStage`, `rcaConfidence`, `rcaSummary`, `rcaRequestId`) also injected into the VictorOps `details` panel for structured access. Base message still leads — existing VictorOps filters / dashboards keep working.
- **Phase C (LIVE)** — Per-service Slack channel now receives a `BB-AI Auto-RCA` summary message on every failed pipeline. Uses the org's existing `slack-stagger-bot` Jenkins credential (same one as `Notification.failure`); channel routed via the per-service `config.slack_channel`. **No new infra, no new webhook URL, no Secrets Manager change.**
- **Phase D (later)** — Slack interactive button "🔍 Deep analyze" → POSTs to a new `/v1/rca/deep` endpoint with `deep:true`, replies into the same thread. Requires Slack app with `interactivity` enabled + a public bbctl-rca endpoint (already covered by ALB).
- **Phase E (LIVE)** — Hybrid freshness + agent-mode RCA. Repos pulled per-RCA + agent iteratively reads code to trace the failure backwards from the Jenkins job config to the function that threw. See "Agent mode" below.
- **Future** — fetch real Kayenta canary scores via Kayenta API for `canary_fail` class instead of inferring from build log alone.

---

## Agent mode (Phase E)

For "deep" error classes — `compliance`, `canary_fail`, `canary_script_error`, `health_check`, `parse_error`, `scm`, `unknown` — the RCA is produced by an **OpenAI function-calling agent** instead of a single one-shot LLM call. Other classes (timeout, network, ssm, dependency, java_runtime with a clean stack trace) still use the cheaper one-shot path.

**Architecture**

```
_run_rca()
  ├─► git_fresh.ensure_fresh_many([jenkins_pipeline, InfraComposer])
  │     └─ shallow fetch, 3s timeout/repo, 60s dedup, falls back to local
  ├─► Jenkins API: log + build_meta
  ├─► classify(log) → error_class
  └─► IF error_class ∈ AGENT_CLASSES and provider=openai:
        agent.run_agent(...)
            ├─ Initial primer (one-shot tool context: service.lookup,
            │   source.trace, docs.<class>.md, jira.tickets, github.commits…)
            └─ Tool-use loop (max 8 calls, $0.25 cap):
                 - get_jenkins_job_config(job)        ← almost always first
                 - repo_read_file(...)                ← read entrypoint groovy
                 - repo_find_function(...)            ← locate called helpers
                 - repo_search(...)                   ← grep for error strings
                 - repo_list_dir(...) | repo_recent_commits(...) | service_lookup(...)
      ELSE:
        run_rca(...)  ← one-shot path (cheap classes)
```

**Hybrid freshness model**

- `bbctl_rca/git_fresh.py` runs at the start of every `_run_rca` call. Performs `git fetch --depth 1 && git reset --hard origin/<branch>` on both repos in parallel. Self-heals perms (`chmod -R u+w`) so a `chmod -R a-w` from elsewhere can't permanently break sync. In-memory dedup window of 60s prevents back-to-back fetches when concurrent webhooks fire.
- The `/etc/cron.d/bbctl-rca-sync` cron is now a backstop (frequency can be relaxed from every 2h to every 6h since the per-RCA path keeps repos hot for any active build).
- If a per-RCA fetch fails (GitHub down, network blip, timeout), we silently fall back to whatever's already on disk. The `repos_freshness` block is included in the audit JSON so the operator can see whether the agent saw the latest commit.

**Tool palette exposed to the agent** (defined in `bbctl_rca/agent.py::TOOLS`)

| Tool | Backed by | What it does |
| --- | --- | --- |
| `get_jenkins_job_config(job)` | `jenkins.get_job_config` | Fetch Jenkins job's `config.xml`; surface `scm_url`, `scm_branch`, `scriptPath`. Almost always the agent's first tool call. |
| `repo_read_file(repo, path, start, end)` | `mcp_tools.repo_read_file` | Read a slice of a file. Returns real line numbers (1-based) so the agent can cite them in `evidence`. |
| `repo_search(repo, query, max_results)` | `mcp_tools.repo_search` | ripgrep across a repo for a literal string. |
| `repo_list_dir(repo, path)` | `mcp_tools.repo_list_dir` | List immediate children of a directory. |
| `repo_find_function(repo, name)` | `mcp_tools.repo_find_function` | Find where a Groovy/Java/Python function is *defined* (definition site, not call sites). |
| `repo_recent_commits(repo, n)` | `mcp_tools.repo_recent_commits` | Last N commits with author, date, short message — quickly answers "what changed?" |
| `service_lookup(service)` | `mcp_tools.service_lookup` | Slim view of `config.json` entry for a service. |

**Guards**

- **Iteration cap**: 8 tool calls max per RCA. On the 9th iteration the agent is forced into JSON-only mode (no more tools).
- **Cost cap**: $0.25 per RCA. When the running token spend hits this, the agent gets a "cost cap reached, emit JSON now" message.
- **Per-tool truncation**: any tool result over 8K chars is sliced (prevents a runaway grep from blowing the context window).
- **Logging**: every tool call is printed to stderr as `[agent] iter N tool#M: <name>({args})` — visible via `journalctl -u bbctl-rca -f`.

**Cost expectation**

Typical agent run: 4-6 tool calls, ~25-35K input tokens, ~600-900 output tokens → ~$0.07-0.10 per RCA (vs ~$0.05 for one-shot). Worst-case at the cap: ~$0.25.

**Evidence quality**

Because the agent reads real source, `evidence[].source` for agent-mode RCAs includes paths like `jenkins_pipeline/vars/canary.groovy:47` (with the actual line number from the tool call). This is more grounded than the one-shot path where citations come from `source.trace` hits only.

**Falling back to one-shot**

If `LLM_PROVIDER != "openai"` (e.g. running on Gemini) OR if `error_class` is one of the cheap classes, the dispatcher uses `run_rca(...)` exactly as before. No regression for those paths.

### Files touched by Phase E

| File | Purpose |
| --- | --- |
| `bbctl_rca/git_fresh.py` | NEW. Per-RCA shallow fetch + reset, 3s timeout, 60s dedup, fallback to local clone. |
| `bbctl_rca/jenkins.py` | NEW `get_job_config(job)` — fetch + parse Jenkins `config.xml`. |
| `bbctl_rca/mcp_tools.py` | NEW `repo_list_dir`, `repo_find_function`, `repo_recent_commits`; tightened `repo_read_file` to return real line numbers. |
| `bbctl_rca/agent.py` | NEW. OpenAI function-calling loop with iteration + cost cap. |
| `bbctl_rca/llm.py` | NEW public alias `build_initial_tool_ctx(...)` so the agent can reuse the one-shot primer. |
| `bbctl_rca/main.py` | Dispatcher: `ensure_fresh_many` at the top of `_run_rca`; agent vs one-shot routing on `error_class`. |
| `prompts/rca_agent_system.md` | NEW. Agent system prompt with the trace method, evidence rules, action rules. |

### Phase C — Slack message shape

Triggered from `main_stagger_prod_plus_one.groovy` post.failure block via:
```groovy
com.blackbuck.utils.Notification.rcaAlert(this, params.SERVICE, branchVal, slackCh, rca)
```

Method lives at `src/com/blackbuck/utils/Notification.groovy::rcaAlert(...)`. Uses `slackSend tokenCredentialId: 'slack-stagger-bot'`. Posts to `env["${SERVICE}:slack_channel"]` (the same channel that already receives the `Notification.failure` alert).

```
Build#1234 Test-Supply-Wrapper-Nonweb — BB-AI Auto-RCA  🤖
------------------------------------------------------
Class: health_check   Stage: Deploy   Confidence: 0.85

Summary: Deploy stage failed due to health check failure...

Finding: <if Map-shaped suggested_fix>
Action:  <if Map-shaped; else first 500 chars of fix string>

Commands:
• `[safe] bbctl shell i-02fc813e939bb2b39`
• `[safe] bbctl run i-02fc813e939bb2b39 -- 'sudo ss -tlnp | grep 7005'`
• `[safe] bbctl run i-02fc813e939bb2b39 -- 'curl -i http://localhost:7005/admin/version'`

Job:       <blue-ocean-link>
Branch:    <COMMIT_ID or tag>
Console:   <build_url>/console
RCA id:    <uuid>
Timestamp: <IST>
```

Orange color (`#ff8c00`) distinguishes the RCA message from the red `FAILURE` alert. Both messages land in the same channel so the team sees the failure AND the diagnosis side-by-side.

**Behavior:**
- Non-fatal: any error in `rcaAlert` is caught + echoed; never breaks rollback or VictorOps flow.
- Skipped silently if `rca == null` (webhook failed) or no `slack_channel` configured for the service.
- Skipped for canary failures by the surrounding `if (PROD_PLUS_ONE_COMPLETED && !isCanaryFailure)` guard — same logic that already gates VictorOps.

### Phase B — VictorOps message shape

```
Production pipeline failed for <service> after Prod+1 validation passed.
Rollback initiated. here is the jenkins link : <build_url>console

🤖 *BB-AI RCA* (class: health_check, stage: Deploy, conf: 0.85)
Summary: Deploy stage failed due to health check failure; ALB target group
         probe remained unhealthy for 50 iterations.
Finding: <first line of suggested_fix, if Map-shaped>
Action:  <truncated to 400 chars>
request_id: <uuid>
```

If `suggested_fix` is a single String (some classes use this shape), only the first ~400 chars appear under a `Fix:` label. For full detail, the on-call clicks through to the Jenkins console where `renderRca()` printed the full boxed block.

---

## Recent improvements (May 2026)

1. **Rebrand to BB-AI** — operator-visible heading changed from `bbctl-rca — Auto RCA` to `Jenkins Build RCA — Powered by BB-AI`. Log prefixes `[bbctl-rca]` → `[BB-AI]`. Internal infra names (URL, credential ID, secret ID) unchanged.
2. **`health_check` error class added** — ALB target-group probe failures (`healthy.sh` 50-iteration loop) now classified correctly instead of falling through to `java_runtime` via the `OutOfMemoryError` flag false-match.
3. **Sanitizer: drop SSH host-key + NewRelic appName-404 + JVM flags** — these noise blocks no longer reach the LLM, so RCAs don't incorrectly cite "SSH key mismatch" as the root cause when SSM fallback is present.
4. **Iteration-spam collapse** — runs of `Health Status for  after N iterations: unhealthy` collapse to first + last + `[N-2 more iterations elided, all unhealthy]`. Cuts ~50 nearly-identical lines per failed deploy.
5. **Stage extractor rewrite** — Strategy A (first stage containing `Error in` / `script returned exit code` / `BUILD FAILED`) with Strategy B fallback (last non-skipped stage). Fixes the misclassification where `Stage "Rollout" skipped due to earlier failure` led the extractor to report `Rollout` instead of the real failed stage `Deploy`.
6. **`HealthCheckFailure.md` runbook + wiring** — new docops/ runbook with 6 ordered likely causes + verify commands; wired into `CLASS_DOCS["health_check"]` so the LLM gets it in the prompt automatically.
7. **`log_path` / `service_port` / `health_check_port` surfaced** — `_SLIM_FIELDS` in `mcp_tools.py` now exposes these so the LLM can give the operator EXACT instance-side paths/ports to check.
8. **Live verification** — build 25 (`stagger-prod-plus-devops-test`) re-RCA'd cleanly: `error_class=health_check`, `failed_stage=Deploy`, cites instance `i-02fc813e939bb2b39` + 50 iterations + concrete `ssh ... tail /var/log/blackbuck/<svc>.log` / `ss -tlnp | grep <port>` / `curl /admin/version` commands. Cost: $0.003 / 18K input tokens / 60s latency.
9. **Phase B shipped** — VictorOps incident `message` now carries `buildAlertMessage(rca)` (class/stage/confidence + summary + Finding/Action), and `details` panel adds structured `rcaErrorClass / rcaFailedStage / rcaConfidence / rcaSummary / rcaRequestId`. On-call sees the RCA inside the page itself — no need to click through to Jenkins console to know what failed.
10. **`buildAlertMessage` hardened** — handles both `suggested_fix` shapes (Map with Finding/Action keys, or plain String). Previously String-shaped fixes produced an empty alert body.
11. **Real config field resolution for health_check** — first live RCA emitted `<your-key.pem>`, `<instance-ip>`, `<log_path>`, `<health_check_port>` placeholders because `config.json` for `test-supply-wrapper-nonweb` had every canonical field (`log_path`, `service_port`, `health_check_port`) set to `null`. Root cause: the org uses different field names (`target_port`, `filebeat_log_path`, `key_name`, `server_command`). Fixed by:
    - `_SLIM_FIELDS` extended with the real-world names so `service_lookup` surfaces them.
    - `llm.py` `health_check.service_config` block resolves canonical → actual (`port` ← `target_port`; `log_path` ← `filebeat_log_path`; etc.), parses `-Dlog.dir=` out of `server_command` as a log-location hint, and derives `pem_path_hint = /var/lib/jenkins/.ssh/<key_name>.pem` from `key_name`. Any unresolved field shows as `NOT_IN_CONFIG` so the LLM SEES the absence rather than fabricating.
    - `prompts/rca_system.md` adds STRICT rule: NEVER emit `<placeholder>` strings; if `NOT_IN_CONFIG`, write a concrete discovery command (`ls /var/log/blackbuck/`, `ss -tlnp | grep java`, `aws elbv2 describe-target-groups`, `aws ssm start-session ...`) instead.
12. **BBCTL is the org-standard instance access tool** — RCA action items now use `bbctl shell <instance-id>` (interactive) and `bbctl run <instance-id> -- '<cmd>'` (one-shot, preferred for `suggested_commands`) instead of raw `ssh -i <key>.pem ubuntu@<ip>`. Wired into:
    - `prompts/rca_system.md` — STRICT BBCTL command rules (substitute real `instance_id` from `health_check.target`, never emit `<instance-id>` placeholders; `bbctl run` for one-shots, `bbctl shell` for interactive; SSM and raw ssh = fallback only).
    - `bbctl_rca/llm.py` — `health_check.guide` injects the org access pattern into the LLM prompt at runtime.
    - `docops/HealthCheckFailure.md` — new "Access pattern — use BBCTL" lead section + all verify commands rewritten to use `bbctl run`.
13. **Live verification (round 2)** — same build 25 re-RCA'd cleanly. Output now contains real values everywhere: `bbctl shell i-02fc813e939bb2b39`, port `7005` (resolved from `target_port`), log path `/var/log/blackbuck/test-supply-wrapper-nonweb.log` (org-standard pattern), health endpoint `/admin/version`. All `suggested_commands` tier `safe`. No raw `ssh`, no `<placeholder>`. Cost: $0.003 / 19K input tokens / 62s latency.
14. **Phase C wired (Slack)** — `Notification.rcaAlert(...)` static method added to `com.blackbuck.utils.Notification`; called from `main_stagger_prod_plus_one.groovy` post.failure right after the RCA webhook returns. Uses existing `slack-stagger-bot` Jenkins credential + per-service `config.slack_channel` (via `env["${SERVICE}:slack_channel"]`). No new Secrets Manager entry, no new Slack app, no webhook URL change — fully reuses existing org Slack infra. Orange-colored message lands in the same channel as the red `Notification.failure` so team sees failure + diagnosis side-by-side.
15. **HTML report endpoint** — new `GET /rca/v1/report/<request_id>` route in FastAPI; renders the stored audit JSON as a polished HTML page. `audit.read_by_request_id(uuid)` added (with uuid regex validation as path-traversal defence). `build_url` now captured in the audit record so the report can link directly. Same URL appears in Jenkins console / sidebar / Slack / VictorOps `details` (key `rcaReportUrl`) — one canonical, shareable surface across all channels.
16. **Compact Jenkins console** — `renderRca()` no longer dumps the full RCA into Jenkins console (~40 lines). New output is ~10 lines: header box, class / stage / one-line summary, full report URL, request_id. Rationale: operators don't have to scroll through a wall of text; the HTML report is one click away. Sidebar build description got a rich 2-line card with `Open RCA →` link.
17. **Dark-theme HTML report** — Polished UI: navy `#0a0f1c` background with dot pattern, sticky frosted-glass topbar, hero card with gradient, per-class colored pills (translucent with matching borders), dark code blocks for commands with Copy button, colored ✓/✗/? evidence badges, two-column grid for root cause + suggested fix. Self-contained CSS (no Tailwind / Inter CDN) so it renders correctly in restricted corporate networks. Header has no bot emoji — clean professional brand. Footer: `Built by Hariharan G, DevOps`.
18. **Confidence + cost + tokens hidden from operator UI** — `confidence` was a self-reported LLM score with no automation gates, so it was cosmetic. Removed from console box, sidebar description, Slack message, VictorOps details, and HTML report header. Still stored in audit JSON for retro analysis. Cost / token usage similarly hidden from the report header (operators don't care; finance can see in audit JSON).
19. **BBCTL scoped to instance-access classes only** — earlier prompt iteration was suggesting `bbctl run i-... -- 'git fetch'` for compliance failures (wrong tool — compliance is fixed in Jira, not on an instance). Prompt now restricts `bbctl shell` / `bbctl run` to: `health_check` (always); `java_runtime` / `network` / `ssm` (when stack trace points at an instance). Forbidden for: `compliance`, `scm`, `aws_limit`, `parse_error`, `canary_*` — those are operator-action failures (Jira UI / GitHub / AWS console / config edits).
20. **Compliance split into 5 distinct modes** — `prompts/rca_system.md` now has explicit Mode 1-5 guidance reading directly from `jira.tickets[].custom_fields["Signed Off Commit ID"]`:
    - **Mode 1** — missing Signed Off Commit ID (most common; matches `ERROR: Compliance: ... has no Signed Off commit id`)
    - **Mode 2** — commit-mismatch (uses the existing Option A / Option B template)
    - **Mode 3** — Jira ticket status not in allowed list
    - **Mode 4** — clone-of-clone chain detected
    - **Mode 5** — merged PR title missing the Jira ticket ID
    Prevents the previous "clone detection failed" hallucinations on logs where clone-detection actually passed.
21. **Jira REST API curl suggestion banned** — operator edits the Signed Off Commit ID field in the Jira UI (custom-field PUT via REST often requires special perms; UI is org-standard path). Prompt explicitly forbids `curl -X PUT 'https://blackbuck.atlassian.net/rest/api/2/issue/...'` in `suggested_commands` or prose.
22. **`SSH` / `ssh` wording banned from prose** — earlier output mixed `ssh -i ...` into Action prose even when commands used `bbctl run`. Prompt rule now: NEVER use `SSH` / `ssh` in prose; write `Use bbctl shell <instance_id>` or `Run bbctl run <instance_id> -- '<cmd>'`. `ssh ...` allowed only as a one-line fallback clause if BBCTL unavailable.
23. **Real config field resolution** — `_SLIM_FIELDS` extended to surface this org's actual config.json field names (`target_port`, `filebeat_log_path`, `key_name`, `server_command`, `aws_region`, `service_identifier`, `service_type`). `llm.py` `health_check.service_config` block now resolves canonical → actual names (e.g. `port` ← `target_port`, `log_path` ← `filebeat_log_path`), parses `-Dlog.dir=` out of `server_command` as a fallback log-location hint, and derives `pem_path_hint` from `key_name`. Unresolvable fields shown as `NOT_IN_CONFIG` so LLM sees the gap rather than fabricating `<placeholder>` strings.
24. **Unknown-class deep dive** — when classifier returns `unknown`, expand context: wider `source.trace` sweep (10 queries × 16 hits), full `docs.catalog` block listing every docops/*.md with first heading + 250-char preview, plus a 4-step `unknown_class.guide` telling the LLM to self-classify from source evidence + runbook previews. Marks `needs_deeper: true` when no fit is found.
25. **Model bumped: gpt-4o-mini → gpt-4o** — `bbctl_rca/llm.py` `run_rca_openai` now uses `gpt-4o` (full model). Reasoning quality on multi-step compliance / canary cases is markedly better. Cost calc in `main.py` updated to `$2.50/1M input + $10.00/1M output` (gpt-4o pricing). Typical RCA: ~$0.04-0.06 vs $0.003 before. Daily spend cap in `cache.py::over_daily_cap` still enforces.
26. **Live verification (compliance class, build 35)** — re-RCA cleanly cites Mode 1 ("Jira ticket PEB-7 is missing the 'Signed Off Commit ID' custom field"), Action template tells operator to edit the field in Jira UI (not REST API), Evidence includes both the `ERROR: Compliance: ... has no Signed Off commit id` log line AND the `jira.tickets` block confirming the missing custom field. No BBCTL commands. No clone-detection hallucination. No `<placeholder>` strings.

---

## File pointers

| What                              | Where                                                            |
| --------------------------------- | ---------------------------------------------------------------- |
| FastAPI entrypoint                | `bbctl_rca/main.py`                                              |
| LLM dispatch & tool-context build | `bbctl_rca/llm.py`                                               |
| Error classifier (ordered rules)  | `bbctl_rca/classifier.py` + `classifier_rules.yml`               |
| Log window extraction             | `bbctl_rca/window.py`                                            |
| Per-canary-stage pass/fail        | `bbctl_rca/canary_analyzer.py`                                   |
| Jira fetch (incl. `customfield_10973` Signed Off Commit ID) | `bbctl_rca/jira.py` |
| GitHub commit lookup              | `bbctl_rca/github.py`                                            |
| NewRelic slow-txn query           | `bbctl_rca/newrelic.py`                                          |
| Runbook section extractor         | `bbctl_rca/runbook.py`                                           |
| 24h diskcache + daily cap         | `bbctl_rca/cache.py`                                             |
| Audit log writer                  | `bbctl_rca/audit.py`                                             |
| systemd start script              | `infra/scripts/bbctl-rca-start.sh`                               |
| Jenkins post-failure groovy lib   | `infra/jenkins/post_failure_rca.groovy` (mirrored to `vars/triggerRcaWebhook.groovy` in jenkins_pipeline) |
| Pipeline wiring                   | `jenkins_pipeline_master/main_stagger_prod_plus_one.groovy` (post.failure block) |
| LLM prompts                       | `prompts/rca_system.md`, `prompts/rca_examples.md`               |
| Per-class runbooks                | `docops/StaggerProdPlusOneDeploy.md`, `docops/JiraDetailsCompliance.md`, etc. |
