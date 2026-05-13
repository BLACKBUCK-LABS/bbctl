# bbctl RCA + Docs — Phase 1 Plan

**Status:** Locked, awaiting implementation.
**Owner:** Hariharan
**Date:** 2026-05-12
**Target backend instance:** `i-0ca911dd5fdd22584` (Prod-bbctl-backend, zinka 735317561518, ap-south-1c) — **stay on current t3a.medium**; bump only if Phase 1 saturates RAM/CPU.

---

## 1. Executive Summary

Extend `bbctl` with two capabilities:

1. **`bbctl rca <job> <build>`** — auto RCA on Jenkins build failures. Triggered by Jenkins post-failure webhook OR manually by dev. Returns structured root cause + suggested fix with file:line citations.
2. **`bbctl docs "<question>"`** — Q&A over org docs in `s3://docops-doc-storage/`.

**Stack: agentic Claude (no RAG Phase 1).** Claude Sonnet 4.6 reasons over Jenkins log + reads pipeline source + docs on demand via MCP tools. Opus 4.7 escalates hard cases. Phase 2 adds app-log access via existing bbctl gated SSM and auto-PR creation.

**Cost Phase 1: ~$190/mo** for 5-20 RCAs/day.

---

## 2. Use Cases

### A. Pipeline-side RCA (Phase 1 primary)

Real example: `jenkins_example_output_stagger_prodplusone.txt`, 8859 lines.
- Failure: `parse error: Invalid numeric literal at line 74, column 401` at line 8791
- Stages skipped: Infra, Deploy, Rollout, Destroy
- Exit code 4

Without tool: dev greps log, opens jenkins_pipeline + InfraComposer, traces ~30 min.
With tool: `bbctl rca stagger-prod-plus-one 12345` → structured RCA in ~5-15s with citations.

### B. Doc Q&A

`bbctl docs "how do I take a heap dump on prod?"` → answer grounded in `ssm-java-heap-dump.md` with citation.

### C. App-side RCA (Phase 2)

Health-check fails in canary stage → Claude calls `service.lookup` → `instance.tail_log` (via existing bbctl gated SSM) → diagnoses app-side error.

### D. Auto-PR (Phase 2)

For `jenkins_pipeline` or `InfraComposer` issues, Claude proposes patch → opens PR via `git.create_pr` against feature branch → dev reviews + merges.

---

## 3. Infrastructure Topology

Three separate boxes (existing). Integration plan below.

### 3.1 Existing topology

```
┌─────────────────────────────────────────────────────────────────┐
│ Jenkins master (existing)                                       │
│   - Hosts UI on :8080                                           │
│   - Runs declarative pipelines (main_stagger_prod_plus_one,     │
│     stagger-nonweb, stagger-prod-plus-one-frontend,             │
│     hotfix-noncanary, create-quick-infra, QA-Automation)        │
│   - Dispatches steps to slave-1, slave-2                        │
│   - post-failure block sends VictorOps alert (existing)         │
└──────────────┬──────────────────────────────┬───────────────────┘
               │ JNLP 50000                   │ JNLP 50000
               ▼                              ▼
       ┌───────────────┐              ┌───────────────┐
       │ slave-1       │              │ slave-2       │
       │ (build agents)│              │               │
       └───────────────┘              └───────────────┘


┌─────────────────────────────────────────────────────────────────┐
│ Prod-bbctl-backend (i-0ca911dd5fdd22584)                        │
│   - bbctl Go backend :8080                                      │
│   - Handles /v1/commands, /v1/instances, /v1/upload, etc.       │
│   - IAM role bbctl-backend-service (SSM cross-account)          │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 Target topology (Phase 1)

Add MCP plugin on Jenkins master + new endpoints on bbctl backend + new bbctl-mcp Go process on bbctl box. **No changes to slaves.**

```
┌─────────────────────────────────────────────────────────────────┐
│ Jenkins master                                                  │
│   - existing pipelines                                          │
│   - INSTALL: Jenkins MCP plugin → /mcp-server/mcp               │
│   - INSTALL: bbctl-rca-bot user (read-only ACL: Job/Read,       │
│              Run/Read, Workspace/Read)                          │
│   - INSTALL: BBCTL_WEBHOOK_SECRET in Jenkins credentials        │
│   - UPDATE main_stagger_prod_plus_one.groovy + 6 other          │
│     pipelines: in post.failure block, after VictorOps POST,     │
│     add second POST to bbctl /v1/rca/webhook (HMAC-signed)      │
└──────────────────────────────┬──────────────────────────────────┘
                               │ HTTPS to bbctl-ec2:8080
                               │ (webhook on failure)
                               │
                               │ HTTPS from bbctl-ec2 (Jenkins MCP plugin)
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│ Prod-bbctl-backend (t3a.medium, 4 GB, 30 GB gp3) — current      │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ bbctl backend Go :8080                                    │  │
│  │   existing endpoints + NEW:                               │  │
│  │     POST /v1/rca           (CLI + JWT)                    │  │
│  │     POST /v1/rca/webhook   (Jenkins + HMAC)               │  │
│  │     POST /v1/docs          (CLI + JWT)                    │  │
│  │     POST /v1/ingest/doc    (S3 event optional)            │  │
│  │   internal: sanitize, classify, Claude SDK, audit         │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ bbctl-mcp Go :7070  (same binary, MCP transport mode)     │  │
│  │   Phase 1 tools to Claude:                                │  │
│  │     - repo.search(repo, query, max_results)               │  │
│  │     - repo.read_file(repo, path, line_start?, line_end?)  │  │
│  │     - docs.list()                                         │  │
│  │     - docs.get(name)                                      │  │
│  │     - service.lookup(service_name)                        │  │
│  │     - sanitize.check(text)                                │  │
│  │   Phase 2 additions:                                      │  │
│  │     - instance.list_for_service(service)                  │  │
│  │     - instance.tail_log(instance_id, log_path, lines)     │  │
│  │     - instance.curl_health(instance_id, port, path)       │  │
│  │     - git.create_pr(repo, branch, patch, title, body)     │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  /var/cache/bbctl/repos/   (read-only mount for bbctl-mcp)      │
│    - jenkins_pipeline    (nightly git pull, master branch)      │
│    - InfraComposer       (nightly git pull, master branch)      │
│                                                                 │
│  /var/cache/bbctl/docops/  (hourly aws s3 sync from docops S3)  │
│                                                                 │
│  Secrets via SOPS + age:                                        │
│    keys.enc.yaml:                                               │
│      ANTHROPIC_API_KEY                                          │
│      JENKINS_API_TOKEN                  (bbctl-rca-bot)         │
│      BBCTL_WEBHOOK_SECRET               (HMAC shared)           │
│      SLACK_WEBHOOK_URL_CI_FAILURES                              │
│      GITHUB_PAT                         (for nightly git pull)  │
│                                                                 │
│  systemd timers:                                                │
│    bbctl-repo-sync.timer    02:00 UTC                           │
│    bbctl-docops-sync.timer  hourly                              │
│    bbctl-audit-rotate.timer weekly                              │
└─────────────────────────────────────────────────────────────────┘
            │
            ├─ outbound HTTPS api.anthropic.com (Claude)
            ├─ outbound HTTPS api.github.com (git pull)
            ├─ outbound HTTPS s3.ap-south-1.amazonaws.com (docops + audit)
            ├─ outbound HTTPS hooks.slack.com (per-service Slack channels)
            └─ outbound HTTPS jenkins-master:8080 (MCP plugin)
```

**Slaves are not touched.** All RCA logic lives between bbctl-ec2 and Jenkins master.

### 3.3 Network requirements

Inventory of new flows needed:

| Direction | From | To | Port | Purpose |
|---|---|---|---|---|
| ① | Jenkins master | bbctl-ec2 | TCP 8080 (HTTPS preferred) | post-failure webhook |
| ② | bbctl-ec2 | Jenkins master | TCP 8080 | Jenkins MCP plugin: get_log, get_build, get_change_sets |
| ③ | bbctl-ec2 | api.anthropic.com | TCP 443 | Claude API |
| ④ | bbctl-ec2 | hooks.slack.com | TCP 443 | Slack post |
| ⑤ | bbctl-ec2 | api.github.com | TCP 443 | git pull |
| ⑥ | bbctl-ec2 | s3.ap-south-1.amazonaws.com | TCP 443 | docops + audit |
| ⑦ | dev workstations | bbctl-ec2 | TCP 8080 | CLI calls (existing) |

**Already open in current SG (zinka-mumbai-prod, sg-09b48a8be5de6aeb6):**
- TCP 8080 from `10.34.0.0/16` (covers dev workstations and Jenkins master if Jenkins is in same VPC)
- All-traffic from sibling Blackbuck CIDRs (Divum, Singapore, Frankfurt)

**Confirmed:** Jenkins master in **same VPC** as bbctl-ec2 (`vpc-0a22ad559772a470f`, `10.34.0.0/16`).

- Existing SG `zinka-mumbai-prod` already allows TCP 8080 from `10.34.0.0/16` → **no new ingress rule needed** for either direction within VPC.
- bbctl-ec2 private IP `10.34.120.223` reachable from Jenkins master directly.
- Use Jenkins master private IP/internal DNS in webhook URL (not public).

### 3.4 IAM

`bbctl-backend-service` instance profile needs additions:

| Permission | Why |
|---|---|
| `s3:GetObject` on `arn:aws:s3:::docops-doc-storage/*` | docs sync |
| `s3:ListBucket` on `arn:aws:s3:::docops-doc-storage` | docs list |
| `s3:PutObject` on `arn:aws:s3:::docops-doc-storage/audit/*` | audit log writes |
| `s3:PutObject` on `arn:aws:s3:::docops-doc-storage/build-history/*` | Phase 2 archive |
| (existing) `ssm:SendCommand` etc. | unchanged |

Anthropic + Gemini + Slack + GitHub keys live in **SOPS-encrypted file in bbctl repo** (not IAM).

### 3.5 Why this topology is good

- **Zero slave changes** — slaves only execute builds, don't touch RCA path
- **One Jenkins master plugin install** — MCP plugin handles all read access
- **One Jenkins user creation** — `bbctl-rca-bot` read-only token gates everything
- **Single bbctl box** — no new infra to provision
- **Repos cached locally** — no per-query GitHub round-trip; nightly pull keeps fresh
- **MCP plugin auth on master ACL** — write tools (trigger_build, replay) blocked by Jenkins permissions even if Claude tries

---

## 4. Tech Stack (Phase 1 locked)

| # | Component | Role | Licence | Notes |
|---|---|---|---|---|
| 1 | **bbctl backend** Go (extend existing) | new endpoints + agent loop | internal | +/v1/rca, /v1/rca/webhook, /v1/docs |
| 2 | **bbctl-mcp** Go (same binary, MCP mode) | tools for Claude | internal | ~500 LoC new |
| 3 | **Anthropic SDK (Go)** | Claude API + native MCP | MIT | direct, no proxy |
| 4 | **Jenkins MCP plugin** | get_log/get_build/test_results/change_sets | MIT | install on master |
| 5 | **Sanitize regex** (Go stdlib) | strip secrets | BSD-3 | YAML rules file |
| 6 | **Classifier regex** (Go stdlib) | error → enum | BSD-3 | YAML rules file |
| 7 | **systemd timers** | nightly git + hourly docops + weekly audit rotate | LGPL | replaces cron |
| 8 | **SOPS + age** | secrets (encrypted in repo) | MPL-2.0 / BSD | git-stored |

**Phase 1 supporting tools (no service):** `git`, `ripgrep`, Go stdlib regex.

**Phase 1 has NO:** Postgres, pgvector, embeddings, vector cache, RAGFlow, LiteLLM, reranker, fallback chain. All deferred to Phase 2 with explicit triggers.

### LLM strategy

| Tier | Model | When |
|---|---|---|
| Default | `claude-sonnet-4-6` | All RCAs + docs |
| Escalate | `claude-opus-4-7` | `confidence < 0.7` OR `bbctl rca --deep` |
| Phase 2 fallback 1 | Gemini 3 Pro | Anthropic 429/5xx |
| Phase 2 fallback 2 | GPT-5 | both above fail |

---

## 5. Flow (RCA query)

```
1. TRIGGER
   - Jenkins master post.failure block → POST /v1/rca/webhook (HMAC)
   - OR dev: bbctl rca <job> <build> → POST /v1/rca (JWT)

2. AUTH + DEDUP
   - HMAC or JWT verify
   - dedup on (job, build) in last 60s → return prior result
   - daily cost cap check ($20/day) → 429 if exceeded

3. FETCH BUILD CONTEXT (Jenkins MCP plugin)
   - get_build(job, build) → {result, duration, params, urls, sha}
   - get_change_sets(job, build) → commits in this build
   - get_log(job, build, last=2000) → tail
   - All calls go through Jenkins MCP plugin on master, authed with bbctl-rca-bot token

4. LOG WINDOW EXTRACT
   - Regex scan for: error, ERROR:, Exception, FAILURE, FATAL, parse error,
     Caused by, stack trace, exit code, Result !=0
   - Extract ±50 lines around each hit + last 50 lines
   - Cap 300 lines / ~2k tokens
   - Strip ANSI escape codes
   - No hits → fallback to last 200 lines

5. SANITIZE
   - Apply sanitize_rules.yml (account IDs, AKIA/ASIA keys, GOCSPX secrets,
     Bearer tokens, presigned URL signatures, non-blackbuck PII emails)

6. CLASSIFY (regex table → enum)
   parse_error | java_runtime | ssm | scm | network | dependency |
   health_check | canary_fail | timeout | unknown

7. PROMPT ASSEMBLY (cache-aware order)

   Block A — CACHED 1h TTL (cache_control: ephemeral, ttl=1h):
     - System prompt (caveman-compressed, ~600 tok)
     - Few-shot examples (3-5 worked RCAs, ~2500 tok)
     - RCA JSON schema (~400 tok)
     - Tool schemas: jenkins.* + bbctl.* (~1500 tok)
     - Pipeline file manifest (~3000 tok, refreshed nightly)
     ≈ 8000 tok cached → $0.30/MTok read = $0.0024

   Block B — UNCACHED (per-query):
     - error_class + build metadata + change_sets
     - log window (sanitized)
     - user question (if from CLI)
     ≈ 3000 tok → $3/MTok = $0.009

8. CLAUDE SONNET 4.6 + MCP TOOL USE
   Tools registered:
     - jenkins.* (read-only, via Jenkins MCP plugin)
     - bbctl.* (via bbctl-mcp at localhost:7070)
   Tool budget: 6 calls (more than Phase 1 RAG path since no pre-retrieval)
   Streaming on
   response_format: RCA_SCHEMA (JSON)

9. CONFIDENCE GATE
   if conf >= 0.7 AND not needs_deeper → DONE
   else → re-run with Opus 4.7, budget=10
   (skip Opus if daily cost cap hit)

10. PUBLISH (parallel)
    - Slack:
        - central #ci-failures (always)
        - per-service slack_channel from config.json[service] (if available)
    - CLI streamed markdown (only for /v1/rca, not webhook)
    - S3 audit at s3://docops-doc-storage/audit/{YYYY-MM}/{request_id}.json
    - Optional: present suggested_commands to dev → existing /v1/commands gate
```

Phase 2 adds steps between 7 and 8: if `error_class` indicates app-side (health_check, canary_fail), Claude calls `instance.tail_log` to pull live app log before reasoning.

---

## 6. Jenkins Integration Detail

### 6.1 What we install on Jenkins master

- **Plugin:** `mcp-server` (https://plugins.jenkins.io/mcp-server/)
- **Endpoint:** `/mcp-server/mcp` (Streamable HTTP transport)
- **User:** `bbctl-rca-bot`
  - Authentication: API token, generated once, stored in bbctl SOPS as `JENKINS_API_TOKEN`
  - Authorization (role-strategy plugin or matrix-auth):
    - Job/Read, Job/Workspace, Run/Read on all jobs
    - **No** Job/Build, Job/Configure, Run/Replay, Run/Update
  - Defense-in-depth: even if Claude calls write tools, Jenkins returns 403
- **Credential:** `BBCTL_WEBHOOK_SECRET` (random 32-byte hex)
  - Used by post-failure block to sign HMAC; matches secret in bbctl SOPS

### 6.2 Pipeline change (per main pipeline file)

Files to edit (7 total):
1. `main_stagger_prod_plus_one.groovy`
2. `stagger-prod-plus-one-frontend.groovy`
3. `stagger-nonweb.groovy`
4. `hotfix-noncanary.groovy`
5. `create-quick-infra.groovy`
6. `QA-Automation.groovy`
7. `lib/Jenkinsfile.shared` (if used)

In each `post.failure` block, after existing VictorOps logic:

```groovy
// bbctl-rca trigger (Phase 1)
withCredentials([string(credentialsId: 'BBCTL_WEBHOOK_SECRET', variable: 'SECRET')]) {
    try {
        def payload = JsonOutput.toJson([
            job:        env.JOB_NAME,
            build:      env.BUILD_NUMBER.toInteger(),
            service:    params.SERVICE ?: env.JOB_BASE_NAME,
            commit:     params.COMMIT_ID ?: '',
            buildUrl:   env.BUILD_URL,
            consoleUrl: "${env.BUILD_URL}console"
        ])
        def sig = sha256Hmac(payload, SECRET)
        def conn = (HttpURLConnection) new URL('https://bbctl.blackbuck.com/v1/rca/webhook').openConnection()
        conn.setRequestMethod('POST')
        conn.setDoOutput(true)
        conn.setRequestProperty('Content-Type', 'application/json')
        conn.setRequestProperty('X-BBCTL-Signature', "sha256=${sig}")
        conn.outputStream.write(payload.bytes)
        conn.outputStream.close()
        def code = conn.responseCode
        echo "✅ bbctl-rca webhook: ${code}"
        conn.disconnect()
    } catch (Exception e) {
        echo "⚠️ bbctl-rca webhook failed: ${e.message}"
    }
}
```

`sha256Hmac` is a small groovy helper added to `lib/` (or use `org.apache.commons.codec.digest.HmacUtils`).

### 6.3 Where in post-failure block to add

Add **after** existing VictorOps POST (preserves existing behaviour). Place outside VictorOps try/catch so a VictorOps failure doesn't block bbctl trigger.

### 6.4 Manual fallback

Dev can always run `bbctl rca <job> <build>` from CLI for:
- Builds that failed before the change rollout
- Re-runs (cache invalidation)
- `--deep` flag for Opus escalation

---

## 7. RCA Output Schema (JSON, strict)

```json
{
  "summary": "string, 1-2 sentence English",
  "failed_stage": "string, e.g. 'Infra', 'Deploy', 'Rollout'",
  "error_class": "parse_error|java_runtime|ssm|scm|network|dependency|health_check|canary_fail|timeout|unknown",
  "root_cause": "string, caveman style, 2-4 sentences with file:line",
  "evidence": [
    {"source": "jenkins_log:line_8791", "snippet": "parse error: Invalid numeric literal..."},
    {"source": "InfraComposer/services/gps.json:74", "snippet": "..."}
  ],
  "suggested_fix": "string, caveman style, actionable steps",
  "suggested_commands": [
    {"cmd": "supervisorctl status gps", "tier": "safe", "rationale": "verify svc up"}
  ],
  "references": [
    {"doc": "jenkins-stagger-backend-onboarding.md", "section": "§3"}
  ],
  "confidence": 0.85,
  "needs_deeper": false,
  "tokens_used": {"input": 8500, "output": 1200, "cache_read": 6000}
}
```

---

## 8. Few-Shot Examples (quality lever)

Goal: 15-25% quality lift on structured output.

File: `bbctl/prompts/rca_examples.md` (committed to bbctl repo, baked into system prompt at startup, cached in Claude).

Phase 1 starter examples (I will draft from your real logs):

1. **`parse error` jq case** — from `jenkins_example_output_stagger_prodplusone.txt`. JSON config malformed in InfraComposer. Fix = edit `services/<svc>.json` line 74.
2. **`Rollout back as Canary failed`** — canary stage health check failed. Fix = check `bbctl rca --deep` + `bbctl run i-... -- tail -n 200 /var/log/blackbuck/<svc>.log`.
3. **`git fetch failed`** — SCM auth issue. Fix = rotate `jenkins-git-bb` PAT.
4. **`Result != 0` in canary** — post-deploy health check returned non-zero. Fix = inspect service supervisor status + recent app log.
5. **ALB rule conflict in Infra stage** — `rule_arn` already in use. Fix = `config.json[service].rule_arn` mismatch with stack state.

Each example has: input log window + classifier output + expected RCA JSON. ~500 tokens each, ~2.5k total, cached.

---

## 9. Caches (Phase 1)

Three free caches. Skip the paid/complex ones until Phase 2.

| Layer | Phase 1? | Tool | How |
|---|---|---|---|
| L2 Claude prompt cache | **YES** | Anthropic native, 1-hr TTL | `cache_control: {type: ephemeral, ttl: "1h"}` on system + few-shot + tools + manifest |
| L5 Jenkins tool-call cache | **YES** | local Postgres OR file cache | key = `tool_name + canonical_args`; immutable for completed builds |
| L_docs cache | **YES** | file cache in `/var/cache/bbctl/docops/` | hourly aws s3 sync; bbctl-mcp reads from local |
| L1 semantic answer cache | NO | (Phase 2 LiteLLM) | defer |
| L3 retrieval cache | NO | (Phase 2 with RAG) | defer |
| L4 embedding cache | NO | (Phase 2 with RAG) | defer |
| L6 reranker cache | NO | (Phase 2 with reranker) | defer |

Phase 1 doesn't need Postgres for caches yet — can use boltdb or even a single JSON file per cache. Postgres comes in Phase 2 if we add RAG.

> **Update from earlier plan:** since Phase 1 has no RAG, **drop Postgres entirely**. Use boltdb embedded KV (BSD-3, single file, zero ops) for `tool_cache` + `audit_index`. Audit bodies still go to S3.

---

## 10. Cost Projection (Phase 1)

Assumes 15 RCAs + 30 doc queries per day.

| Item | $/mo |
|---|---|
| t3a.medium EC2 (unchanged) | $24 |
| 30 GB gp3 (unchanged) | $3 |
| Detailed monitoring (optional, recommend on) | $2 |
| Claude Sonnet 4.6 (45 q/day × $0.15 with cache) | $200 |
| Claude Opus 4.7 escalate (~1/day) | $30 |
| **Total Phase 1** | **~$260/mo** |

Higher than earlier estimate because agentic Claude reads more files per query (no pre-retrieval). Acceptable for Phase 1 simplicity. Phase 2 RAG drops this ~40%.

**Instance bump triggers** (Phase 2 if any hit):
- RAM > 80% sustained 1h
- CPU > 70% sustained 1h
- Disk > 80% (repo cache + boltdb growing)
- p95 latency > 15s

---

## 11. Phase 2 Triggers

| Trigger | Action |
|---|---|
| RAM > 80% sustained 1h on bbctl-ec2 | Bump instance to t3a.large (in-place, 5 min downtime) |
| CPU > 70% sustained 1h | Bump instance OR move heavy work async |
| Disk > 80% | Expand gp3 (online resize) OR add EBS for repo cache |
| Daily Claude spend > $15/day sustained | Add RAG (Postgres+pgvector+embeddings) to cut per-query tokens |
| Anthropic 429s sustained | Add LiteLLM + Gemini/OpenAI fallback chain |
| App-side errors common (health_check class >30% of RCAs) | Wire `instance.tail_log` + `instance.curl_health` MCP tools |
| Repeat fixes in `jenkins_pipeline`/`InfraComposer` | Wire `git.create_pr` MCP tool, dev reviews + merges |
| >100 RCAs/day | Add L1 semantic cache |
| Code retrieval quality low | Add LlamaIndex code chunker sidecar with tree-sitter |
| Compliance audit on EBS encryption | Blue/green migrate with encrypted EBS (scheduled window) |

---

## 12. Timeline (3 weeks)

### Week 1 — Infra prep (no migration needed)

**Day 1: Secrets + cache dirs on existing t3a.medium**
- [x] SOPS + age install — `sops 3.9.4`, `age 1.0.0` on bbctl-ec2
- [x] Generate age key — `/etc/bbctl-rca/keys/bbctl-rca.key` (chmod 600, ubuntu-owned)
  - Public key: `age1m7rfcvvzpe5fhxpjwgagfw5naahd4j7fscm266cj283hymrkgd8s7t2mcy`
- [x] Encrypt `/etc/bbctl-rca/keys.enc.yaml` with: `llm_api_key` (Gemini, temp), `llm_provider: gemini`, `jenkins_token`, `jenkins_url`, `webhook_secret`, `github_pat`, `slack_webhook_url`
  - Note: `ANTHROPIC_API_KEY` → `llm_api_key` (Gemini for now, swap to Claude post-testing)
- [x] Create cache dirs: `/var/cache/bbctl-rca`, `/var/log/bbctl-rca`, `/opt/bbctl-rca/repos` (ubuntu-owned)
  - Note: actual paths use `-rca` suffix, differ from plan (plan used `/var/cache/bbctl/`)
- [ ] CloudWatch alarm: disk > 75% on root volume — ⏳ needs SNS ARN
- [ ] Enable detailed monitoring — ⏳ not yet done

**Day 2: Hardening (low-risk, no downtime)**
- [x] IMDSv2 enforce — `HttpTokens: required` confirmed via `describe-instance-metadata-options`
- [x] bbctl backend healthy after IMDSv2 flip (no reported issues)
- [ ] (Defer encrypted-EBS swap until Phase 2 migration window)

**Day 3-4: Jenkins master setup**
- [x] Jenkins MCP plugin installed via UI (`Manage Jenkins → Plugins → Available → "MCP Server"`)
  - ⚠️ NOT yet active — restart pending (jobs running; midnight restart scheduled)
  - Workaround: **using Jenkins REST API direct** (`/job/{job}/{build}/consoleText`, `/api/json`) until plugin confirmed active
- [x] `BBCTL_WEBHOOK_SECRET` credential added in Jenkins (Secret text)
- [ ] `bbctl-rca-bot` user — **N/A**: Jenkins uses Google SSO (CloudFlare), no local users. Using `g.hariharan@blackbuck.com` API token stored in `keys.enc.yaml → jenkins_token`
- [ ] Test MCP endpoint after midnight restart: `curl -u "g.hariharan@blackbuck.com:<TOKEN>" "http://10.34.42.254:8080/mcp-health"` → expect 200
- [x] Network bbctl-ec2 ↔ Jenkins master verified: same VPC `10.34.0.0/16`, existing SG allows TCP 8080

**Day 5: Initial repo clone + S3 docops sync**
- [x] `git clone BLACKBUCK-LABS/jenkins_pipeline → /opt/bbctl-rca/repos/jenkins_pipeline` (97 MB, 24912 objects)
- [x] `git clone BLACKBUCK-LABS/InfraComposer → /opt/bbctl-rca/repos/InfraComposer` (3.6 MB, 5897 objects)
- [x] `chmod -R a-w /opt/bbctl-rca/repos/` — both dirs `dr-xr-xr-x`, read-only ✅
- [x] Disk check: 127 MB total (well under 500 MB)
- [x] `aws s3 sync s3://docops-doc-storage/docs/ /opt/bbctl-rca/docops/` — 11 files, 88K ✅
  - Needed `bbctl-rca-docops-read` inline policy on `bbctl-backend-service` role (`s3:GetObject` + `s3:ListBucket`)
  - Files: jenkins-stagger-backend-onboarding, jenkins-stagger-frontend-onboarding, jenkins-stress-environment-onboarding, ssm-file-transfer, ssm-java-heap-dump, ssm-java-thread-dump, ssm-list-directory-files, ssm-output-script-setup, ssm-permanent-access-guide, ssm-secure-api-caller, ssm-temporary-access-jenkins

### Week 2 — bbctl + bbctl-mcp

**Day 6-7: bbctl-mcp Go binary**
- [ ] Add MCP server mode to existing bbctl binary (`bbctl mcp-serve --port 7070`)
- [ ] Implement Phase 1 tools:
  - `repo.search(repo, query)` — ripgrep over `/var/cache/bbctl/repos/{repo}`
  - `repo.read_file(repo, path, lines)` — read with line range
  - `docs.list()` — list `/var/cache/bbctl/docops/*.md`
  - `docs.get(name)` — read doc
  - `service.lookup(service)` — parse `jenkins_pipeline/resources/config.json[service]`
  - `sanitize.check(text)` — apply rules, return {clean, redactions[]}

**Day 8: Ingest scripts**
- [ ] systemd `bbctl-repo-sync.service` + `.timer` (nightly 02:00 UTC)
  - clone/pull jenkins_pipeline + InfraComposer to `/var/cache/bbctl/repos/`
- [ ] systemd `bbctl-docops-sync.service` + `.timer` (hourly)
  - `aws s3 sync s3://docops-doc-storage/docs/ /var/cache/bbctl/docops/`
- [ ] Refresh file manifest cache: `bbctl manifest-refresh` runs as part of repo-sync, writes `/var/lib/bbctl/manifest_{repo}.txt`

**Day 9-10: bbctl backend handlers**
- [ ] `/v1/rca` (JWT) + `/v1/rca/webhook` (HMAC) handlers
- [ ] `/v1/docs` (JWT)
- [ ] Sanitizer + classifier (regex YAML rules)
- [ ] Log window extractor
- [ ] Prompt builder with cache_control breakpoints
- [ ] Anthropic SDK wrapper with MCP servers (Jenkins MCP + bbctl-mcp localhost)
- [ ] JSON schema parser + retry on malformed
- [ ] Confidence gate + Opus escalation
- [ ] Dedup table (boltdb)
- [ ] Tool-call cache (boltdb)

### Week 3 — Glue + UX + soak

**Day 11: System prompt + few-shots**
- [ ] Author `bbctl/prompts/rca_system.md` (caveman compressed)
- [ ] Author `bbctl/prompts/rca_examples.md` (5 worked examples)
- [ ] Author `bbctl/prompts/docs_system.md`
- [ ] Test prompt cache hit rate ≥ 70%

**Day 12: CLI**
- [ ] `bbctl rca <job> <build>` (cobra)
- [ ] `bbctl rca --latest <job>`
- [ ] `bbctl rca --deep <job> <build>`
- [ ] `bbctl rca --request-id <uuid>` (re-fetch prior result)
- [ ] `bbctl docs "<question>"`
- [ ] `bbctl docs --list`

**Day 13: Slack + audit**
- [ ] Slack block formatter for RCA
- [ ] Post to #ci-failures + per-service channel from config.json
- [ ] S3 audit writer with deadletter fallback to `/var/lib/bbctl/audit-deadletter/`
- [ ] Nightly audit deadletter retry

**Day 14: Jenkins pipeline updates**
- [ ] Add `sha256Hmac` helper to `lib/`
- [ ] Edit 7 pipeline groovy files: add bbctl-rca POST after VictorOps in `post.failure`
- [ ] Test on staging job with intentional failure

**Day 15: Soak + cutover**
- [ ] Shadow mode: `SILENT_MODE=true` (don't post Slack, only log)
- [ ] 3-day soak on `main_stagger_prod_plus_one` only
- [ ] Manual eval: 10 RCAs vs ground truth, target ≥60% correct
- [ ] Flip `SILENT_MODE=false` for that one pipeline
- [ ] 4-day soak, monitor cost + signal:noise in Slack
- [ ] Roll out to other 6 pipelines
- [ ] Announce in #infra-devops

---

## 13. Sanitization Rules (`bbctl/sanitize_rules.yml`)

SOPS-encrypted, committed to bbctl repo:

```yaml
rules:
  - name: aws_account_id
    pattern: '\b(735317561518|597070799581|075903075452|476114138058)\b'
    replace: '<account>'
  - name: aws_access_key
    pattern: '\bA(K|S)IA[0-9A-Z]{16}\b'
    replace: '<aws_key>'
  - name: google_oauth_secret
    pattern: 'GOCSPX-[A-Za-z0-9_-]{28}'
    replace: '<gcp_secret>'
  - name: anthropic_key
    pattern: 'sk-ant-[A-Za-z0-9_-]+'
    replace: '<anthropic_key>'
  - name: bearer_token
    pattern: 'Bearer\s+[A-Za-z0-9._~+/-]+=*'
    replace: 'Bearer <redacted>'
  - name: presigned_signature
    pattern: 'X-Amz-Signature=[A-Fa-f0-9]+'
    replace: 'X-Amz-Signature=<redacted>'
  - name: pii_email
    pattern: '\b[A-Za-z0-9._%+-]+@(?!blackbuck\.com\b)[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b'
    replace: '<external_email>'
  - name: ssh_private_key
    pattern: '-----BEGIN [A-Z]+ PRIVATE KEY-----[\s\S]+?-----END [A-Z]+ PRIVATE KEY-----'
    replace: '<private_key>'
```

Applied:
- Before sending log/context to Claude
- Before posting Slack
- Before writing S3 audit
- `bbctl-mcp.sanitize.check` exposes as tool too

---

## 14. Security and Blast Radius

### MCP tool gates

| Tool | Read/Write | Defense |
|---|---|---|
| `jenkins.get_*` | read | Jenkins ACL on `bbctl-rca-bot` |
| `jenkins.trigger_build`, `replay_pipeline` | write | DISABLED by ACL + omitted from tools list to Claude |
| `bbctl.repo.search` / `read_file` | read | filesystem ACL: `/var/cache/bbctl/repos/` read-only |
| `bbctl.docs.get` | read | filesystem read; S3 cached locally |
| `bbctl.service.lookup` | read | reads cached config.json from local clone |
| `bbctl.sanitize.check` | read | pure function |
| `bbctl.instance.tail_log` (Phase 2) | read | flows through existing `/v1/commands` safe tier (no Jira needed) |
| `bbctl.git.create_pr` (Phase 2) | write | scoped: only `jenkins_pipeline` / `InfraComposer`; PR not merge; bbctl-rca-bot GitHub user has write to feature branch only |

### Suggested-command execution

> **Security boundary:** LLM never auto-executes. CLI presents `suggested_commands` to dev for explicit pick. Once chosen, command goes through existing `/v1/commands` gated pipeline (safe / restricted / denied tiers + Jira approval for restricted). Webhook path **never** executes.

### Audit

Every RCA + docs query writes to `s3://docops-doc-storage/audit/{YYYY-MM}/{request_id}.json`:
- user, timestamp, job/build
- input log hash + token counts
- full sanitized prompt + response
- tool calls + results
- provider + model
- cost in tokens + USD

Retention: 13 months (matches existing bbctl policy).

Failure to write → local deadletter → nightly retry. >24h drain failure = SEV alert.

---

## 15. Success Criteria

- [ ] `bbctl rca` p95 < 12s
- [ ] ≥60% RCAs correctly identify failing stage + likely cause (manual eval on 20 past failures)
- [ ] `bbctl docs` answers grounded in ≥1 citation 95% of the time
- [ ] Claude prompt-cache read ratio ≥ 70%
- [ ] Zero secrets leaked to Anthropic (audit-log inspection on 100 sample queries)
- [ ] Phase 1 cost ≤ $350/mo first month
- [ ] 7-day production soak with no SEV requiring rollback

---

## 16. Repos in Scope

| Repo | Phase | Mount path |
|---|---|---|
| `BLACKBUCK-LABS/jenkins_pipeline` (a.k.a. `jenkins_pipeline_master` locally) | 1 | `/var/cache/bbctl/repos/jenkins_pipeline/` |
| `BLACKBUCK-LABS/InfraComposer` — Terraform IaC, `config/<service>/` per-service + `module/` shared modules (this is where jq parse-errors live) | 1 | `/var/cache/bbctl/repos/InfraComposer/` |
| Service repos (~70: fms-gps, fmspayments, demand, …) | 2 | cloned on-demand from Phase 2 |
| `docops-doc-storage` S3 bucket (org docs) | 1 | `/var/cache/bbctl/docops/` |

---

## 17. Files / Artefacts to Produce

| Path | Purpose |
|---|---|
| `bbctl/commands/rca.go` | `bbctl rca` cobra command |
| `bbctl/commands/docs.go` | `bbctl docs` cobra command |
| `bbctl/commands/mcp_serve.go` | `bbctl mcp-serve` (MCP transport mode) |
| `bbctl/internal/rca/sanitize.go` | regex sanitizer |
| `bbctl/internal/rca/classifier.go` | error class regex table |
| `bbctl/internal/rca/window.go` | log window extractor |
| `bbctl/internal/rca/prompt.go` | prompt builder w/ cache markers |
| `bbctl/internal/rca/handler.go` | /v1/rca + /v1/rca/webhook |
| `bbctl/internal/docs/handler.go` | /v1/docs |
| `bbctl/internal/jenkins/mcp_client.go` | MCP HTTP client to Jenkins plugin |
| `bbctl/internal/llm/claude.go` | Anthropic SDK wrapper with MCP servers + cache config |
| `bbctl/internal/mcp/server.go` | bbctl-mcp server (MCP transport on :7070) |
| `bbctl/internal/mcp/tools.go` | tool implementations |
| `bbctl/internal/cache/bolt.go` | boltdb dedup + tool cache |
| `bbctl/sanitize_rules.yml` (SOPS) | regex rules |
| `bbctl/classifier_rules.yml` | error class rules |
| `bbctl/prompts/rca_system.md` | caveman system prompt |
| `bbctl/prompts/rca_examples.md` | 5 few-shot RCA examples |
| `bbctl/prompts/docs_system.md` | docs Q&A system prompt |
| `infra/systemd/bbctl-repo-sync.service` + `.timer` | nightly git pull |
| `infra/systemd/bbctl-docops-sync.service` + `.timer` | hourly S3 sync |
| `infra/systemd/bbctl-audit-rotate.service` + `.timer` | weekly audit rotation |
| `jenkins/post_failure_hook.groovy` | snippet to paste into 7 pipeline files |
| `jenkins/lib/HmacUtils.groovy` | sha256Hmac helper |

---

## 18. Open Items (resolved)

| Q | Answer |
|---|---|
| Webhook auth | HMAC shared secret (`BBCTL_WEBHOOK_SECRET`) |
| Slack routing | Both: #ci-failures + per-service from config.json |
| config.json access | tool (`service.lookup`), not full-load |
| Few-shot examples source | drafted by tool author from real logs, dev reviews |
| Auto-PR scope (Phase 2) | feature branch, dev merges, never auto-merge |
| Daily cost cap | $20/day global, alert at $15 |
| Confidence threshold for Opus | 0.7 |
| Repo storage | local clone in `/var/cache/bbctl/repos/`, nightly git pull |
| Postgres | **dropped from Phase 1** (no RAG); boltdb for dedup + tool cache |
| Instance type | t3a.medium (current), bump only on saturation |
| IaC repo name | `BLACKBUCK-LABS/InfraComposer` (Terraform: `config/<service>/` + `module/`) |
| GitHub PAT | reuse existing `Jenkins-git-bb` |
| Jenkins master location | same prod VPC `vpc-0a22ad559772a470f` `10.34.0.0/16` — no SG ingress changes |

---

## 19. References

- **bbctl codebase analysis:** `analyse.md` (this repo)
- **Real Jenkins failure logs:** `jenkins_example_output_stagger_prodplusone.txt`, `jenkins_example_output_stagger_prodplusone_ex1.txt`
- **Real pipeline:** `jenkins_pipeline_master/main_stagger_prod_plus_one.groovy`
- **Org docs corpus:** `s3://docops-doc-storage/docs/` (11 markdown files)
- **Jenkins MCP plugin:** https://plugins.jenkins.io/mcp-server/
- **Anthropic prompt caching:** https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- **Anthropic Go SDK:** https://github.com/anthropics/anthropic-sdk-go
- **MCP spec:** https://modelcontextprotocol.io
- **Phase 2 references (deferred):**
  - https://github.com/infiniflow/ragflow
  - https://github.com/pgvector/pgvector
  - https://github.com/huggingface/text-embeddings-inference
  - https://github.com/paradedb/paradedb
  - https://docs.litellm.ai/
  - https://medium.com/oracledevs/fast-ai-search-with-graalvm-spring-boot-and-oracle-database-4e8ba46c9a74
  - https://github.com/oracle-devrel/oracle-ai-developer-hub/tree/main/apps/oracle-database-vector-search
- **Prior art:** https://www.jenkins.io/blog/2025/08/03/chirag-gupta-gsoc-community-bonding-blog-post/

---

**Phase 1 lock date: 2026-05-12. Ship target: 2026-06-02.**
