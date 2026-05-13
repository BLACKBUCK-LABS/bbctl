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
6. Call LLM (default `gpt-4o-mini`, JSON mode, temp 0.1)
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

- 24h RCA result cache: same `(job, build)` returns prior RCA without re-calling LLM. Marked `from_cache: true` in response.
- 60s dedup cache for in-flight requests.
- To force fresh RCA: call `/v1/rca` directly with `{"deep": true}`.

---

## Jenkins integration

### Pipeline wiring (`jenkins_pipeline_master/main_stagger_prod_plus_one.groovy`)

In the `post.failure` block, after `rollbackMain(...)`:

```groovy
script {
    rollbackMain("Single Job Rollback", params.SERVICE)

    // ============ bbctl-rca auto-RCA (Phase A — console + build description only) ============
    // Non-fatal: any error here must not affect rollback or VictorOps alert below.
    try {
        triggerRcaWebhook()
    } catch (Exception e) {
        echo "[bbctl-rca] non-fatal error: ${e.message}"
    }
    // ========================================================================================

    // ... existing VictorOps alert block continues unchanged ...
}
```

### Shared library (`vars/triggerRcaWebhook.groovy`)

Posts the signed webhook using raw `HttpURLConnection` (no `httpRequest` plugin dependency, since the HTTP Request plugin is **not** installed in the Jenkins controller). Helpers:

- `triggerRcaWebhook()` — entry point called from `post.failure`
- `postWebhook(url, payload, sig)` — `@NonCPS`, pure-Java POST; throws on transport error
- `parseJson(text)` — `@NonCPS` wraps `JsonSlurper.parseText`
- `renderRca(rca)` — pretty-prints boxed RCA block + sets `currentBuild.description`
- `hmacSha256(secret, body)` — `@NonCPS` HMAC-SHA256 for request signing
- `buildAlertMessage(rca)` — one-paragraph summary for Phase B (VictorOps/Slack enrichment)

### Credentials in Jenkins

- **Secret text** with ID `bbctl-webhook-secret` matching `WEBHOOK_SECRET` in AWS Secrets Manager `bbctl-rca/prod`.

### What the operator sees in the build console

```
╔══════════════════════════════════════════════════════════════════╗
║                      bbctl-rca — Auto RCA                        ║
╚══════════════════════════════════════════════════════════════════╝
  class:       compliance
  failed_stage:Build
  confidence:  0.9

  Summary:    <one-line>
  Root cause: <para with file:line citations>
  Suggested fix:
    [Action]  / [Finding] / [Verify]
  Commands:   [safe|restricted] cmd → rationale
  Evidence:   [✓/✗] source: snippet
  request_id: <uuid>
  full audit: /var/log/bbctl-rca/<uuid>.json
```

Build description (sidebar) also shows a one-line `RCA: <summary>` so the failure is triagable from the job list.

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

- Per-call cost estimated from token counts (`gpt-4o-mini`: $0.15/1M input, $0.60/1M output)
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

## Phase roadmap

- **Phase A (LIVE)** — Auto-RCA prints to console + build description on failure. Webhook is non-fatal; nothing about the existing alert flow changed.
- **Phase B (planned, after ~10 clean Phase A runs)** — enrich the existing VictorOps incident `message` field with `buildAlertMessage(rca)` so on-call sees the RCA summary inside the page itself.
- **Phase C (later)** — Slack notification with RCA + suggested commands; "deep" mode triggered from a Slack button.
- **Future** — fetch real Kayenta canary scores via Kayenta API for `canary_fail` class instead of inferring from build log alone.

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
