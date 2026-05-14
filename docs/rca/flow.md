# BB-AI Auto-RCA — End-to-end Flow

Companion to `bbctlrca.md` (which is the changelog). This doc explains **how** an Auto-RCA happens, from Jenkins pipeline failure through to the operator seeing a Slack message or HTML report.

For history of why each piece was added, see numbered items 1–51 in `bbctlrca.md`.

---

## High-level diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│ 1. Pipeline failure → triggerRcaWebhook() POSTs to /rca/v1/rca/webhook  │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 2. bbctl-rca: verify HMAC, dedup, daily cap, 24h cache lookup           │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 3. Hybrid freshness — git fetch+reset jenkins_pipeline + InfraComposer  │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 4. Pull Jenkins data: consoleText + api/json + wfapi/describe           │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 5. extract_window() + sanitize() + prepend stage errors                 │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 6. classify(clean_window) → class ∈ {health_check, scm, … , unknown}    │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 7. Dispatch: AGENT_CLASSES → agent loop; else → one-shot LLM            │
└────────────────────┬─────────────────────────────────┬──────────────────┘
                     ▼                                 ▼
       ┌──────────────────────────┐     ┌──────────────────────────────┐
       │ 7a. AGENT (multi-step)   │     │ 7b. ONE-SHOT (single call)   │
       │  • primer + tools list   │     │  • primer with runbook       │
       │  • loop ≤ 6 iters /      │     │  • single gpt-4o call        │
       │    ≤ $0.25               │     │  • response_format=json      │
       │  • tool_choice forces    │     │                              │
       │    final JSON at end     │     │                              │
       └────────────┬─────────────┘     └────────────┬─────────────────┘
                    └────────────────┬───────────────┘
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 8. _parse_final_json() — tolerant: JSON / markdown-fenced / brace-extract│
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 9. audit.write() + cache.put() + return RCA map to pipeline             │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 10. Surfaces: Jenkins console, sidebar, Slack, VictorOps, HTML report   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Step-by-step

### 1. Pipeline failure → webhook POST

File: `jenkins_pipeline/vars/triggerRcaWebhook.groovy`

When a stage fails, the `post.failure` block calls `triggerRcaWebhook()`. Payload:

```json
{"job": "<job_name>", "build": <build_number>, "service": "<service>"}
```

Signed with HMAC-SHA256 using the `bbctl-webhook-secret` Jenkins credential. Header `X-Bbctl-Signature: sha256=<digest>`.

The pipeline waits up to 60s for the response. Service-side LLM call is the slow part — pipeline blocks on it.

### 2. Webhook receiver — auth + caching

File: `bbctl_rca/main.py` (`/rca/v1/rca/webhook` route)

- HMAC verify against `WEBHOOK_SECRET` env var (loaded from AWS Secrets Manager via `infra/scripts/bbctl-rca-start.sh`).
- Dedup: `is_duplicate(job, build)` — if same job+build currently being processed, return the running `request_id`.
- 24h cache: `get_rca(job, build)` — if same job+build was RCA'd in last 24h, return cached with `from_cache: true`. `deep=true` bypasses.
- Daily cost cap: `over_daily_cap()` in `cache.py` — sum of today's RCA costs > daily limit → HTTP 429.
- Generate `request_id` (UUID).

### 3. Hybrid git freshness pull

File: `bbctl_rca/git_fresh.py`

Called once per RCA before any tool context built:

```python
freshness = ensure_fresh_many([
    ("jenkins_pipeline", None),
    ("InfraComposer", None),
])
```

Per repo:
- `git fetch --depth 1 origin/<branch>` then `git reset --hard origin/<branch>`
- 3s timeout — falls back to on-disk copy on failure
- 60s in-memory dedup — concurrent RCAs share a single fetch
- Self-heals perms (`chown -R ubuntu:ubuntu` + `chmod -R u+w`) before each op

Result attached to audit as `repos_freshness`. See `bbctlrca.md` item 29.

### 4. Pull Jenkins data

File: `bbctl_rca/jenkins.py`

Three REST calls:

| Call | Returns | Use |
|---|---|---|
| `get_console_log(job, build)` → `/job/<j>/<b>/consoleText` | raw text log | bulk evidence |
| `get_build_meta(job, build)` → `/job/<j>/<b>/api/json` | build URL, timestamps | metadata |
| `get_stage_errors(job, build)` → `/job/<j>/<b>/wfapi/describe` | per-stage status + `error.message` | exception trace |

**Why wfapi**: `consoleText` reflects flushed console output only. When webhook fires inside `post.failure`, Jenkins has NOT yet emitted the trailing `Also: groovy.lang.MissingMethodException ... at WorkflowScript:330` block — that lands AFTER the post block completes. `wfapi/describe` populates each stage's `error.message` as soon as the stage transitions to FAILED, independent of console buffer timing. See `bbctlrca.md` item 50.

### 5. Window extraction + sanitization

Files: `bbctl_rca/window.py`, `bbctl_rca/sanitize.py`

- `extract_window(raw_log, deep=deep)` — finds the failure marker (first `Error in`, `script returned exit code`, `BUILD FAILED`) and takes ±N lines around it (wider for `deep=true`).
- Prepend wfapi stage errors as a block at the top so classifier + LLM always see the real exception:
  ```
  === Failed stages (from Jenkins workflow API) ===
  Stage 'Jira Details' status=FAILED
  groovy.lang.MissingMethodException: No signature of method: ...
  ```
- `sanitize(window)` — drops noise: SSH host-key mismatch, NewRelic appName-404, JVM `-XX:+HeapDumpOnOutOfMemoryError` flags. Collapses iteration spam (50× `unhealthy` lines → first+last+`[N-2 elided]`). Returns `(clean_window, redactions_list)`.

`extract_failed_stage(raw_log)` runs separately for `build_meta.detected_failed_stage`.

### 6. Rule-based classify

Files: `bbctl_rca/classifier.py`, `classifier_rules.yml`

Walks rules top-to-bottom, returns first matching class. No-match → `unknown`.

Priority order:

1. `compliance` — `ERROR: Compliance:`, `no Signed Off commit id`
2. `canary_fail` — `Rolling Back as Result !=0`, `Canary run failed for canary run id`
3. `canary_script_error` — `canary.py: error`, `subprocess.CalledProcessError`
4. `aws_limit` — `LimitExceededException`, `QuotaExceeded`
5. `parse_error` — `parse error:`, `jq: error`, `Unexpected token`
6. `health_check` — `Health Status failed to move to healthy`, `iterations: unhealthy`, `healthz`
7. `java_runtime` — `java.lang.\S+Exception/Error`, `groovy.lang.\S+Exception`, `No signature of method:`, `unable to resolve class`
8. `ssm` — `SSM command failed`, `ssm:SendCommand`
9. `scm` — `git fetch failed`, `Could not read from remote`, `Authentication failed.*github`
10. `network` — `Connection refused`, `No route to host`
11. `dependency` — `Could not resolve`, `Download failed`
12. `timeout` — pipeline-level timeouts
13. `unknown` — fallthrough

`health_check` MUST come before `java_runtime` (probe failures falsely match `OutOfMemoryError` otherwise). Groovy DSL exceptions added to `java_runtime` so pipeline-script errors don't fall to `unknown` (item 48).

### 7. Dispatch — agent loop vs one-shot

File: `bbctl_rca/main.py::_run_rca`

```python
AGENT_CLASSES = {
    "canary_fail", "canary_script_error",
    "health_check", "parse_error", "scm",
}
if LLM_PROVIDER == "openai" and error_class in AGENT_CLASSES:
    result = await run_agent(...)
else:
    result = await run_rca(...)        # one-shot
```

**Why these classes are AGENT_CLASSES**: they benefit from in-repo code tracing. `health_check` → agent reads `nonwebdeploy.groovy` → `healthy.sh`. `canary_fail` → agent reads `rollout.groovy` → `canary.py`. Tool calls pay off.

**Why compliance + unknown are NOT** (items 43, 46):
- Compliance = Jira-field-missing. Primer already has `jira.tickets` + Mode 1-5 guidance. Agent has nothing to trace, drifts into prose.
- Unknown = catch-all. By definition no class-specific code path. Same drift pattern.

Both route to one-shot with `unknown_class.guide` STRICT rules forbidding runbook-narrative borrowing (item 49).

---

## 7a. Agent loop deep dive

File: `bbctl_rca/agent.py`

```python
MAX_TOOL_CALLS = 6              # iterations (each may issue multiple tool calls)
COST_CAP_USD   = 0.25
PER_TOOL_RESULT_CAP = 1500      # bytes per tool result body
TRIM_HISTORY_AFTER  = 1         # elide tool-result bodies older than 1 iter
INPUT_USD_PER_TOKEN  = 2.50 / 1_000_000
OUTPUT_USD_PER_TOKEN = 10.00 / 1_000_000
```

### Tool palette

| Tool | Purpose |
|---|---|
| `get_jenkins_job_config` | Jenkins job's `config.xml` → `scm_url`, `scm_branch`, `script_path` |
| `repo_read_file(repo, path, start, lines)` | File slice with 1-based line numbers |
| `repo_search(repo, query, max_hits)` | Ripgrep across a repo |
| `repo_find_function(repo, name)` | Locate Groovy/Java/Python definitions |
| `repo_list_dir(repo, path)` | List immediate children of a dir |
| `repo_recent_commits(repo, n)` | `git log` short SHA + date + author + message |
| `service_lookup(service)` | `config.json` slim view |

### Loop iteration

```
for iteration in range(MAX_TOOL_CALLS + 1):     # 0..6 inclusive
    if cost_so_far >= COST_CAP_USD:
        inject _FORCE_FINAL_PROMPT; tool_choice="none"
    force_final = (iteration == MAX_TOOL_CALLS)  # iter 6 forced
    if force_final:
        inject _FORCE_FINAL_PROMPT; tool_choice="none"
    response = openai.chat.completions.create(...)
    update cost_so_far += tokens × pricing
    if force_final or no tool_calls in response:
        return _parse_final_json(response.content)
    for tc in response.tool_calls:
        run tool, cap result body to PER_TOOL_RESULT_CAP
        append to messages
    _elide_old_tool_results(messages, current_iter, keep_recent=TRIM_HISTORY_AFTER)
```

### Why a separate COST_CAP_USD alongside MAX_TOOL_CALLS

Iteration count alone does NOT bound token cost. Three multipliers:

1. **Parallel tool calls in a single iter.** OpenAI's API lets the LLM return N tool_calls in one assistant message. N=5 means 5 tool results re-fed.
2. **Large tool results.** `repo_read_file` on a 500-line file can return ~30KB even after cap. PER_TOOL_RESULT_CAP=1500 trims but rare LLM choices still spike.
3. **History replay.** Every API call re-sends the FULL message history. Older tool results stay in context (elision after `TRIM_HISTORY_AFTER` iters replaces body with `[elided to save tokens]`, but iteration N still pays for iter N-1's content).

Pathological case: LLM at iter 3 does 4 parallel `repo_search` calls each returning 50 hits → 6K extra tokens in. By iter 6 that would be ~$0.30 input alone. Cost cap catches it before iter 6.

### `_FORCE_FINAL_PROMPT`

Injected on both cost-cap and iter-6 paths. Inline RCA schema + explicit rules:

- "NOT markdown, NOT ###headings — ONLY a JSON object"
- "If a tool errored earlier, that's fine — use the context you already have"
- "Output the JSON object only — no prose before or after"

`tool_choice="none"` is set in the same call so the LLM cannot request more tools.

### What happens if the LLM still outputs garbage

`_parse_final_json(text)` tries 3 shapes in order:

1. Pure `json.loads`
2. Strip ` ```json … ``` ` fence then `json.loads`
3. Find first `{` and last `}`, slice, `json.loads`

If all fail → fallback stub `"summary": "Agent failed to emit valid JSON.", "needs_deeper": true`. Raw `final_text` logged to stderr at journalctl for debugging. See `bbctlrca.md` item 41.

---

## 7b. One-shot path

File: `bbctl_rca/llm.py` (`run_rca`)

Single `chat.completions.create` call with `response_format={"type": "json_object"}`. Faster, cheaper, no tool calls. Used for non-agent classes.

Tool context built ahead of time in `_build_tool_context`:

- `build.meta` — job, build, service, detected_failed_stage
- `service.lookup` — slim config.json view (port, log_path, key_name, …)
- `source.trace` — pre-computed ripgrep of the failing error string across `jenkins_pipeline` + `InfraComposer`
- `docs.<class>` — class-specific runbook excerpt (from `CLASS_DOCS` dict). E.g. `compliance` → `JiraDetailsCompliance.md`.
- `docs.catalog` — for `unknown` class only: all `docops/*.md` first heading + 250-char preview
- `unknown_class.guide` — for `unknown` only: 5 strict rules (item 49)
- `jira.tickets` — for any class that mentions a ticket key
- `github.commits` — for `compliance`/`scm`: commit metadata for SHAs in log

Prompt = system (one-shot RCA primer) + user (tool context + log window).

---

## 8. Parse + validate

Both paths converge on `_parse_final_json` (defined in `agent.py`, used by both). Same tolerant parser handles drifted LLM output.

---

## 9. Persist + cache + respond

Files: `bbctl_rca/audit.py`, `bbctl_rca/cache.py`

- `audit.write(request_id, payload)` → JSON file at `/var/log/bbctl-rca/<request_id>.json`. Payload includes the RCA, build_url, redactions list, repos_freshness, agent_tool_calls, cost, tokens.
- `cache.put(job, build, rca)` → diskcache 24h TTL.
- HTTP response to Jenkins = the parsed RCA Map.

---

## 10. Operator surfaces

Surfaces fanned out from the pipeline once `triggerRcaWebhook()` returns:

| Surface | Where | Triggered by |
|---|---|---|
| Jenkins console box | `renderRca()` in `triggerRcaWebhook.groovy` | always |
| Sidebar build description | `currentBuild.description = ...` in same var | always |
| Slack channel | `com.blackbuck.utils.Notification.rcaAlert` | per-pipeline, if `slack_channel` set |
| VictorOps | inline in `main_stagger_prod_plus_one.groovy::post.failure` | only stagger prod+1, only when `PROD_PLUS_ONE_COMPLETED=true` + not canary |
| HTML report | `GET /rca/v1/report/<request_id>` | URL embedded in all surfaces |
| Audit JSON | `GET /rca/v1/report/<request_id>.json` | URL embedded |

`create-quick-infra` deliberately skips VictorOps (interactive / dev-triggered, see `bbctlrca.md` item 45).

---

## Cost / latency

| Path | Tokens (in + out) | Cost | Latency |
|---|---|---|---|
| Cache hit (24h) | 0 | $0 | < 100ms |
| One-shot (gpt-4o) | 15–25K + 500 | $0.04–0.06 | 30–60s |
| Agent (6 tool calls) | 25–35K + 600–900 | $0.20–0.25 | 60–90s |
| Worst-case agent (cap hit) | ~capped at COST_CAP_USD | ≤ $0.25 | ~90s |

Daily global cap in `cache.py::over_daily_cap` is the outer limit across all RCAs.

---

## Edge cases

### Stage skipped due to earlier failure

Only the FIRST failed stage's `error.message` is meaningful. Subsequent `Stage 'X' skipped due to earlier failure(s)` lines are downstream effects. `extract_failed_stage` (Strategy A: first stage containing error markers) handles this.

### wfapi unavailable (older Jenkins, non-Pipeline job)

`get_stage_errors` returns `[]`. Flow falls through to consoleText-only — works when the exception trace IS already in console. Just degrades for the post.failure timing-gap case.

### LLM emits prose instead of JSON (item 41)

`_parse_final_json` recovers from 3 shapes. If all fail, fallback stub. Raw text logged.

### Cost cap hits mid-trace

Same as iter 6 — force-final prompt, `tool_choice="none"`, LLM must emit JSON from context gathered so far. May result in `needs_deeper: true` if context was insufficient.

### Compliance / unknown class going via one-shot

These classes deliberately bypass agent loop (items 43, 46). One-shot path uses `unknown_class.guide` STRICT rules (item 49) to prevent LLM from confabulating runbook narratives — better to emit "cannot determine" + `needs_deeper: true` than a confident wrong answer.

### Replay vs new build

Jenkins "Replay" reloads the previous build's WorkflowScript content, NOT the latest SCM. So a pipeline-script bug fixed on disk won't apply to a replayed build. Always re-trigger from SCM for verification.

---

## Where to look for details

- `bbctl_rca/main.py::_run_rca` — orchestrator
- `bbctl_rca/agent.py` — agent loop, force-final, tolerant parser
- `bbctl_rca/llm.py` — one-shot path + tool context builder + unknown_class.guide
- `bbctl_rca/classifier.py` + `classifier_rules.yml` — rule precedence
- `bbctl_rca/jenkins.py` — consoleText / build_meta / wfapi / config.xml
- `bbctl_rca/git_fresh.py` — per-RCA shallow fetch + reset
- `bbctl_rca/mcp_tools.py` — repo_read_file, repo_search, repo_find_function, …
- `bbctl_rca/window.py` — extract_window, extract_failed_stage
- `bbctl_rca/sanitize.py` — noise pattern drops + iteration-spam collapse
- `bbctl_rca/audit.py` — audit JSON writer
- `bbctl_rca/cache.py` — 24h cache + daily cost cap
- `docs/rca/bbctlrca.md` — numbered changelog (items 1–51)
