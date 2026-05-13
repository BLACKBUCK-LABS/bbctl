# Phase 1 — Flow Charts and Edge Cases

Companion to `plan.md`. Use this to drive implementation.

---

## 1. Master flow (RCA — auto webhook or manual CLI)

```
                            ┌──────────────────────────────┐
                            │           ENTRY              │
                            │                              │
                            │  A) Jenkins post-build       │
                            │     groovy: on FAILURE →     │
                            │     POST /v1/rca/webhook     │
                            │     HMAC-signed body         │
                            │                              │
                            │  B) Dev runs bbctl rca       │
                            │     POST /v1/rca             │
                            │     JWT auth                 │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 1. AUTH + DEDUP              │
                            │    - verify HMAC or JWT      │
                            │    - request_id = uuid()     │
                            │    - dedup check: same       │
                            │      (job,build,user) in     │
                            │      last 60s? → return cached│
                            │    - cost-cap check: daily   │
                            │      spend < $20? → 429      │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 2. RESOLVE BUILD             │
                            │    Jenkins MCP get_build     │
                            │    Cache: tool_cache lookup  │
                            │    (immutable when done)     │
                            └──────────────┬───────────────┘
              ┌────────────────────────────┼────────────────────────────┐
              │                            │                            │
        result=null            result=SUCCESS/ABORTED          result=FAILURE/UNSTABLE
              │                            │                            │
              ▼                            ▼                            ▼
        409 build_not_found       skip with "not failed"        ┌───────────────┐
                                  202 (audit only)              │ CONTINUE      │
                                                                └───────┬───────┘
                                                                        ▼
                            ┌──────────────────────────────┐
                            │ 3. FETCH LOG (Jenkins MCP)   │
                            │    get_log tail=2000 lines   │
                            │    Cache: tool_cache         │
                            │    If MCP 5xx → retry x3     │
                            │    If still fail → 502 +     │
                            │    audit                     │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 4. LOG WINDOW EXTRACT        │
                            │    Regex hit ERROR/FAIL/etc  │
                            │    Pull ±50 lines each       │
                            │    + last 50 lines           │
                            │    Cap 300 lines / ~2k tok   │
                            │    No hits? → use last 200   │
                            │    lines (heuristic fallback)│
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 5. SANITIZE                  │
                            │    Apply sanitize_rules.yml  │
                            │    Track redaction count     │
                            │    redaction_rate > 50%?     │
                            │    → flag "low_signal" but   │
                            │    continue                  │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 6. CLASSIFY (regex table)    │
                            │    → enum: parse_error |     │
                            │      java_runtime | ssm |    │
                            │      scm | network | etc.    │
                            │    Multiple matches? → take  │
                            │    earliest in log           │
                            │    No match → 'unknown'      │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 7. BUILD RETRIEVAL QUERY     │
                            │    Top 5 unique error lines  │
                            │    + identifiers extracted   │
                            │    via regex (file paths,    │
                            │    fn names, error strings)  │
                            │    Cap query at ~500 tokens  │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 8. EMBED QUERY               │
                            │    Gemini Embedding 001 API  │
                            │    768-dim                   │
                            │    On 429: backoff x3        │
                            │    On 5xx: fail gracefully   │
                            │    → continue with BM25 only │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ 9. HYBRID RETRIEVE (PG)      │
                            │    pgvector cosine top-30    │
                            │    + tsvector BM25 top-30    │
                            │    RRF merge k=60 → top-10   │
                            │    KB filter from classifier │
                            │    Empty result? → broaden   │
                            │    to all KBs + retry        │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │10. PROMPT ASSEMBLY           │
                            │    Block A (cached 1h TTL):  │
                            │      system + schema +       │
                            │      tools + repo manifest   │
                            │    Block B (per-query):      │
                            │      class + meta + log +    │
                            │      top-10 chunks + Q       │
                            │    Cap total input at 50k    │
                            │    If > 50k → truncate       │
                            │    oldest chunks first       │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │11. CLAUDE SONNET 4.6 + MCP   │
                            │    Tools registered:         │
                            │      jenkins.* (read-only)   │
                            │      bbctl.* (read-only)     │
                            │    Tool budget: 4 calls      │
                            │    Streaming on              │
                            │    response_format=JSON schema│
                            │    Timeout 60s per call      │
                            │    On 429: backoff x3        │
                            │    On 5xx: fail to 502       │
                            │    Tool loop count > 4 →     │
                            │    force stop with current   │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │12. PARSE JSON                │
                            │    Validate vs RCA_SCHEMA    │
                            │    Malformed? → retry x1     │
                            │    Still bad? → return raw   │
                            │    text + 'schema_invalid'   │
                            │    flag                      │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │13. CONFIDENCE GATE           │
                            │    if conf >= 0.7 AND not    │
                            │    needs_deeper → DONE       │
                            │    else if not opus_used     │
                            │    AND cost_ok →             │
                            │    re-run step 10-12 with    │
                            │    Opus 4.7, budget=8        │
                            │    Opus failed too? → return │
                            │    Sonnet result + low_conf  │
                            │    flag                      │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │14. PUBLISH (parallel)        │
                            │    a. Slack #ci-failures     │
                            │       blocks format          │
                            │       on fail: log + retry   │
                            │       to deadletter file     │
                            │    b. CLI streamed render    │
                            │       (only for /v1/rca,     │
                            │       not webhook)           │
                            │    c. S3 audit write         │
                            │       on fail: write to      │
                            │       /var/log/bbctl/audit/  │
                            │       deadletter, alarm      │
                            └──────────────┬───────────────┘
                                           ▼
                            ┌──────────────────────────────┐
                            │ RETURN 200 + RCA JSON        │
                            └──────────────────────────────┘
```

---

## 2. Docs flow (simpler)

```
bbctl docs "<question>"
   │
   ▼
1. AUTH (JWT) + cost-cap check
   │
   ▼
2. SANITIZE question (in case user pastes secret accidentally)
   │
   ▼
3. EMBED question (Gemini)
   │
   ▼
4. PG hybrid retrieve on kb='docops' only, top-5
   │
   │ empty result? → return "no relevant doc found"
   │
   ▼
5. PROMPT (cached system+manifest; uncached chunks+Q)
   │
   ▼
6. Claude Sonnet 4.6 + bbctl-mcp tools (docs.get only)
   tool budget: 2
   │
   ▼
7. PARSE + render markdown
   │
   ▼
8. Audit + return
```

Lighter than RCA — no Jenkins, no log window, no classifier, narrower toolset.

---

## 3. Ingestion flow (background)

### Repo sync (nightly 02:00 UTC)

```
systemd: bbctl-repo-sync.service
   │
   ▼
For each repo in [jenkins_pipeline, infra-compose]:
   │
   ├─ git fetch + git pull (timeout 60s)
   │    fail? → log + alarm + skip this repo
   │
   ├─ git diff HEAD~ HEAD --name-only → changed files
   │    first run? → all files
   │
   ├─ For each changed file:
   │    │
   │    ├─ Read file (skip > 1 MB)
   │    │    binary? → skip
   │    │    encoding fail? → skip + log
   │    │
   │    ├─ SHA-256 of normalized content
   │    │
   │    ├─ Doc-level dedup:
   │    │   SELECT 1 FROM documents WHERE content_hash=$1
   │    │   exists? → skip (unchanged)
   │    │
   │    ├─ Chunk (recursive 512 tok, 15% overlap)
   │    │   markdown → header-aware
   │    │   code → block-aware (basic, full tree-sitter Phase 2)
   │    │
   │    ├─ For each chunk:
   │    │   │
   │    │   ├─ SHA-256 of chunk content
   │    │   │
   │    │   ├─ Chunk-level dedup:
   │    │   │   SELECT 1 FROM chunks WHERE content_hash=$1
   │    │   │   exists? → skip
   │    │   │
   │    │   ├─ Add to embed batch (50 chunks per Gemini call)
   │    │
   │    └─ Flush remaining batch
   │
   ├─ INSERT batch into documents + chunks tables
   │
   └─ Update tsvector via trigger
       (or batch UPDATE after insert)

Refresh repo file manifest cache (for Claude prompt):
   SELECT source_path, COUNT(*) FROM chunks
   WHERE kb=$1 GROUP BY source_path
   → write to /var/lib/bbctl/manifest_{kb}.txt
```

### Docops S3 sync (S3 event → cron hourly fallback)

```
Option A — S3 event Lambda (recommend):
   S3:ObjectCreated → SNS → HTTPS POST /v1/ingest/doc
   bbctl backend fetches + ingests same as above

Option B — Cron hourly (simpler Phase 1):
   systemd: bbctl-docops-sync.service hourly
   aws s3 sync s3://docops-doc-storage/docs/ /var/cache/bbctl/docs/
   Then run repo-sync-style flow on /var/cache/bbctl/docs/
```

Phase 1 default: **Option B**.

### Build history sync (hourly)

```
systemd: bbctl-history-sync.service hourly
   │
   ▼
Query Jenkins MCP get_queue + get_builds in last hour
   filter result IN (FAILURE, UNSTABLE)
   │
   ▼
For each new failed build:
   │
   ├─ Jenkins MCP get_log full
   ├─ Sanitize
   ├─ Save to s3://docops-doc-storage/build-history/{job}/{build}.txt
   ├─ Ingest (chunk + dedup + embed + insert) into kb='build-history'
   └─ Metadata: {job, build, sha, failure_class, ingested_at}
```

---

## 4. Edge cases (comprehensive)

### 4.1 Auth + Entry

| Case | Detection | Action |
|---|---|---|
| HMAC signature mismatch on webhook | `hmac.Equal` fails | 401 + log + count metric |
| JWT expired / invalid on CLI | SDK verify fails | 401 + suggest `bbctl login` |
| Duplicate webhook (Jenkins retries) | dedup table: (job,build) in last 60s | return prior `request_id` 200 (idempotent) |
| Concurrent same (job,build) from CLI + webhook | DB unique constraint on `audit(job,build)` | second loses, returns first's result |
| Daily cost cap hit | counter > $20 today | 429 + Slack alert dev |
| Request body too large (>1 MB) | http body limit | 413 |
| Malformed JSON body | unmarshal fails | 400 |

### 4.2 Jenkins MCP

| Case | Detection | Action |
|---|---|---|
| Jenkins master down | TCP refused / 5xx | retry 3 × exp-backoff (1s, 4s, 16s), then 502 |
| MCP plugin not installed | `/mcp-server/mcp` returns 404 | 502 + clear error "MCP plugin missing" |
| `bbctl-rca-bot` user revoked | 403 from Jenkins | 502 + alarm DevOps |
| Build not found (wrong job/build) | result=null | 404 + audit only |
| Build still running | result=null AND building=true | 409 + "build in progress, retry later" |
| Build aborted (not failed) | result=ABORTED | 200 + skip RCA + audit |
| Build success (called by mistake) | result=SUCCESS | 200 + skip RCA + audit |
| Log doesn't exist (very old build) | get_log returns empty | use metadata only + flag "no_log" |
| Log truncated mid-stack-trace | unmatched braces / dangling lines | accept, classify on what we have |
| Log size huge (>10 MB) | response too big | use paginated `start=-2000` (last 2000 lines only) |

### 4.3 Log window extraction

| Case | Detection | Action |
|---|---|---|
| No regex hits | regex matches = 0 | fallback: use last 200 lines |
| Hits all in last 50 lines | dedup overlaps fully | use last 200 lines |
| ANSI escape codes everywhere | strip via `strip-ansi` Go lib | clean before window extract |
| Non-UTF8 bytes in log | scan + replace `�` | continue, log warning |
| Log lines >10k chars (no newline, e.g. single-line stack trace) | split heuristic on `at `, `: `, `Caused by:` | best-effort |
| Multiple distinct errors (jq + java OOM in one build) | classify on FIRST error, attach others as evidence | document in JSON output |
| `parse error` repeating 100x | dedupe identical lines, keep one | reduces noise |

### 4.4 Sanitize

| Case | Detection | Action |
|---|---|---|
| Sanitize redacts >50% of window | redaction_count / line_count > 0.5 | flag `low_signal=true`, continue |
| Sanitize regex catastrophic backtrack | exec time > 1s | abort that rule, log, continue with remaining |
| New secret pattern unseen | post-hoc audit script | add to `sanitize_rules.yml`, re-run on suspect audit logs |
| Account ID in legitimate context (e.g. doc text "Use account 735317561518") | over-redaction | acceptable; LLM still gets `<account>` and can reason |

### 4.5 Classifier

| Case | Detection | Action |
|---|---|---|
| No regex match | enum=unknown | broaden KB filter to ALL, set conf threshold lower |
| Multiple matches | pick earliest in log (line number) | log all matches in audit |
| Ambiguous (matches both `java_runtime` and `dependency`) | precedence rules in YAML | YAML order = priority |

### 4.6 Embedding

| Case | Detection | Action |
|---|---|---|
| Gemini API 429 | resp 429 | retry x3 exp-backoff (1s, 4s, 16s) |
| Gemini API 5xx | resp 5xx | retry x3, then fall back to BM25-only retrieval |
| Gemini API key invalid | 401 | alarm, return 502 |
| Query > 2048 tok | check len before send | truncate to 2000 tok (keep most important error line) |
| Query empty (all whitespace) | strip + check | return 400 "no actionable error" |
| Network timeout (30s) | context cancel | retry once, then BM25-only |

### 4.7 Retrieval

| Case | Detection | Action |
|---|---|---|
| Postgres down | conn fail | retry x2 with 200ms gap, then 503 |
| Empty result (no chunks in KB filter) | 0 rows | broaden filter to all KBs + retry once; if still empty → return "no context found" + run Claude with metadata only |
| pgvector HNSW index missing | query error "no index" | log critical alarm, fallback to seq scan with `ORDER BY embedding <=> $1 LIMIT 30` (slow but works) |
| RRF merge yields top-10 all same source | dedup by source_path → keep top from each | diversity heuristic |
| Vector score and BM25 score wildly different scales | use RRF (rank-based), not raw scores | already in design |
| KB filter typo (KB doesn't exist) | 0 chunks for that KB | log + continue |

### 4.8 Prompt assembly

| Case | Detection | Action |
|---|---|---|
| Input total > 50k tokens | tokenizer count | trim oldest chunks first; if still >50k, trim log window middle (keep head+tail); if still >50k, 413 |
| Repo manifest > 5k tokens | size check | summarize: only top-level dirs + recently-changed files |
| System prompt < 2048 tokens (Anthropic cache minimum for Sonnet) | check | pad with stable boilerplate (e.g. tool examples) |
| 4 cache_control breakpoints exceeded | enforce limit | merge stable blocks into single cache block |

### 4.9 Claude API

| Case | Detection | Action |
|---|---|---|
| 429 rate limit | resp 429 | retry x3 exp-backoff (2s, 8s, 32s) |
| 5xx Anthropic outage | resp 5xx | retry x3, then 502 with `provider_down=anthropic` (Phase 2 would fallback to Gemini/OpenAI) |
| Network timeout 60s | context cancel | abort + 504 |
| Tool call loop > 4 iterations | counter | inject final "summarize what you have" turn, force JSON output |
| Tool call to unknown tool name | Claude hallucinates a tool | return tool error "unknown tool" to Claude → it adjusts |
| MCP tool call timeout (30s) | tool wrapper timeout | return error to Claude → retry or skip |
| Claude refuses (content policy) | empty content + stop_reason="refusal" | return `confidence=0`, summary="LLM refused", escalate to human |
| Output truncated (max_tokens hit) | stop_reason="max_tokens" | parse partial JSON, accept if root_cause + summary present, flag |
| Stream disconnect mid-response | EOF on SSE | accept partial, retry full once |
| Cache miss on expected hit | log `cache_read_tokens=0` when expected >0 | log + investigate, no retry |
| Tool result > 100 KB | size check before send | truncate to 100 KB, append `... [truncated]` |

### 4.10 JSON parsing

| Case | Detection | Action |
|---|---|---|
| Malformed JSON | json.Unmarshal err | retry Claude with "your last output was malformed JSON, return only valid JSON matching schema X" (1 retry) |
| Missing required fields | schema validate | same retry |
| `confidence` field missing | absent | default to 0.5, mark `confidence_inferred=true` |
| `confidence` out of [0,1] | bounds check | clamp + log |
| Extra unknown fields | strict mode catches | log + accept (forward-compat) |
| Schema violations after retry | still bad | return wrapper with raw text + flag `schema_invalid`, surface to dev |

### 4.11 Confidence gate

| Case | Detection | Action |
|---|---|---|
| conf < 0.7 AND opus_used | already escalated | accept low conf, mark `low_confidence=true` |
| conf < 0.7 AND cost cap hit | can't afford Opus | skip Opus, mark `low_confidence_no_escalate` |
| conf exactly 0.7 boundary | use `>=` not `>` | accept (per design) |
| LLM returns conf=1.0 always | suspicious calibration | log for monitoring; periodic manual eval |

### 4.12 Publish

| Case | Detection | Action |
|---|---|---|
| Slack webhook fails | non-200 resp | retry x2, then write to `/var/log/bbctl/slack-deadletter/` + alarm |
| Slack message too long (>3000 chars per block) | size check | truncate root_cause + suggested_fix, link to S3 audit for full |
| S3 audit write fails | AWS err | retry x2, then write to `/var/lib/bbctl/audit-deadletter/`, alarm; nightly cron retries deadletter |
| CLI client disconnected mid-stream | broken pipe | continue work, store result, dev re-fetches via `bbctl rca --request-id <uuid>` |
| Audit S3 bucket permission denied | 403 | alarm critical (compliance gap) |

### 4.13 Concurrency

| Case | Detection | Action |
|---|---|---|
| 2 webhooks for same (job,build) within 60s | dedup table | second returns first's `request_id` |
| 10 concurrent RCA requests | rate limit per-user | 5 concurrent per-user, queue rest |
| Ingestion cron + RCA query racing on same chunk row | row-level lock in Postgres | reads tolerate writes (MVCC) |
| Postgres connection pool exhausted | pool wait > 5s | 503 + alarm |

### 4.14 Ingestion edge cases

| Case | Detection | Action |
|---|---|---|
| Git pull merge conflict (rare on read-only mirror) | git error | hard reset to origin/main + log |
| File too big (>1 MB) | stat check | skip + log |
| Binary file mistaken for text | UTF-8 validation | skip if invalid |
| File deleted between list and read | open fail | skip + log |
| Chunk content all whitespace | trim + check | skip embed |
| Chunk hash collision (same content_hash, different content — practically impossible SHA-256) | sanity check on insert | log + skip |
| Embedding 768-dim mismatch (Gemini returns 3072) | dim assert | truncate first 768 dims (Matryoshka representation) OR fail-fast |
| Postgres disk full | write fails | alarm + halt ingest |
| HNSW build slow on >100k chunks | takes minutes | accept; serve queries from existing index meanwhile |

### 4.15 Operational

| Case | Detection | Action |
|---|---|---|
| Instance reboot during RCA | systemd service restart | in-flight requests lost; CLI retries with same request_id is idempotent via dedup |
| Disk fills up (logs / Postgres) | CW alarm at 75% | alert ops; cleanup script |
| Daily Anthropic cost > $20 | metering | switch all queries to deny + Slack alert; reset 00:00 UTC |
| Anthropic API key rotation needed | manual or scheduled | SOPS update + bbctl reload (graceful) |
| SOPS decrypt fails on boot | age key missing | service fails to start; alarm |
| Postgres backup needed | weekly cron `pg_dump` | store in S3 audit bucket |

### 4.16 Security warnings (auto-clarity zone — read carefully)

> **Suggested-command execution boundary:**
> LLM output `suggested_commands` is **never auto-executed**. CLI always presents to dev for explicit choice. Once chosen, command flows through existing `/v1/commands` gated pipeline (safe / restricted / denied tiers + Jira approval for restricted).
>
> Webhook path **never** executes commands at all. Only the interactive CLI path allows command selection.
>
> If LLM suggests destructive command (rm/dd/etc.), classifier in `/v1/commands` denies; no override exists in this loop.

> **Audit write failures must alarm:**
> S3 audit is compliance-critical (13-month retention). Local deadletter is acceptable temporary; nightly cron must drain deadletter to S3 before next business day. Failure to drain after 24h = SEV alert.

Caveman resume.

---

## 5. Algorithm pseudocode (Go-ish, edge-aware)

```go
// /v1/rca handler
func handleRCA(ctx context.Context, req RCARequest) (*RCAResponse, error) {
    requestID := uuid.New().String()
    log := log.With("request_id", requestID, "user", req.User)

    // 1. AUTH + DEDUP
    if !verifyAuth(req) { return nil, ErrUnauthorized }
    if prior := dedup.Check(req.Job, req.Build, 60*time.Second); prior != nil {
        log.Info("dedup hit", "prior_id", prior.RequestID)
        return prior, nil
    }
    if !costcap.Allow(req.User) {
        slack.Alert("daily cost cap hit")
        return nil, ErrCostCapExceeded
    }

    // 2-3. METADATA + LOG (with retry + cache)
    meta, err := jenkinsClient.GetBuildCached(ctx, req.Job, req.Build)
    if err != nil { return nil, mapJenkinsErr(err) }
    if meta.Result == "SUCCESS" || meta.Result == "ABORTED" {
        return auditAndReturn(requestID, "skipped_non_failure"), nil
    }
    if meta.Result == "" && meta.Building {
        return nil, ErrBuildInProgress
    }
    logTail, err := jenkinsClient.GetLogCached(ctx, req.Job, req.Build, 2000)
    if err != nil { return nil, mapJenkinsErr(err) }

    // 4. LOG WINDOW
    window, hits := extractFailureWindow(logTail, 50, 300)
    if len(hits) == 0 {
        log.Warn("no error markers found, using log tail")
        window = lastN(logTail, 200)
    }

    // 5. SANITIZE
    cleanWindow, redactions := sanitizer.Scrub(window)
    redRate := float64(len(redactions)) / float64(countLines(window))
    if redRate > 0.5 {
        log.Warn("high redaction rate", "rate", redRate)
    }

    // 6. CLASSIFY
    class := classifier.Classify(cleanWindow)
    kbFilter := classKBs[class]
    if class == "unknown" {
        kbFilter = AllKBs
    }

    // 7. QUERY
    query := buildQueryFromErrors(cleanWindow, hits)
    if strings.TrimSpace(query) == "" {
        return nil, ErrNoActionableError
    }

    // 8. EMBED (with fallback)
    qVec, err := geminiEmbed.Encode(ctx, query)
    bm25Only := false
    if err != nil {
        log.Warn("embed failed, falling back to BM25-only", "err", err)
        bm25Only = true
    }

    // 9. RETRIEVE
    chunks, err := vec.HybridSearch(ctx, kbFilter, query, qVec, bm25Only, 10)
    if err != nil { return nil, err }
    if len(chunks) == 0 && len(kbFilter) < len(AllKBs) {
        log.Info("empty result, broadening filter")
        chunks, err = vec.HybridSearch(ctx, AllKBs, query, qVec, bm25Only, 10)
    }
    // accept empty chunks — LLM still runs with metadata + window

    // 10. PROMPT
    promptIn := buildPromptCacheAware(meta, class, cleanWindow, chunks, req.Question)
    if estTokens(promptIn) > 50000 {
        promptIn = trimChunksOldestFirst(promptIn, 50000)
    }

    // 11. CLAUDE (with tool budget)
    rsp, err := claude.ToolUse(ctx, claude.Request{
        Model: ifFlag(req.Deep, "claude-opus-4-7", "claude-sonnet-4-6"),
        SystemBlocks: promptIn.System, // with cache_control
        Messages:     promptIn.Messages,
        Tools:        []claude.MCPServer{jenkinsMCP, bbctlMCP},
        MaxToolUse:   ifFlag(req.Deep, 8, 4),
        ResponseSchema: rcaSchema,
        Timeout:      60 * time.Second,
    })
    if err != nil { return nil, mapClaudeErr(err) }

    // 12. PARSE
    rca, err := parseRCA(rsp.Content)
    if err != nil {
        rsp2, err2 := claude.ToolUse(ctx, withMessage(promptIn, "your last output was malformed JSON, return valid JSON"))
        if err2 != nil { return nil, err2 }
        rca, err = parseRCA(rsp2.Content)
        if err != nil {
            log.Error("schema_invalid after retry")
            rca = &RCA{SummaryRaw: rsp2.Content, SchemaInvalid: true, Confidence: 0}
        }
    }

    // 13. CONFIDENCE GATE
    if (rca.Confidence < 0.7 || rca.NeedsDeeper) && !req.Deep && costcap.Allow(req.User) {
        // re-run with Opus
        opusReq := req
        opusReq.Deep = true
        return handleRCA(ctx, opusReq) // tail-recursive, dedup will key on (job,build)
    }

    // 14. PUBLISH (parallel best-effort)
    audit.WriteAsync(ctx, requestID, req, rca)
    if !req.SilentMode { slack.PostAsync(rca) }

    return &RCAResponse{RequestID: requestID, RCA: rca}, nil
}
```

---

## 6. State diagram — Jenkins build state interpretation

```
   ┌──────────┐
   │ unknown  │  (Jenkins MCP unreachable)
   └────┬─────┘
        │ retry x3
        ▼
   ┌────────────┐   ┌─────────┐
   │ result=null├──▶│ pending │  (still building)
   └────┬───────┘   └────┬────┘
        │                │ wait/poll
        ▼                ▼
   ┌──────────┐    ┌─────────┐
   │ FAILURE  │    │ SUCCESS │
   │ UNSTABLE │    │ ABORTED │
   └────┬─────┘    └────┬────┘
        │               │
        ▼               ▼
    RUN RCA       audit + skip
```

---

## 7. Test plan (Phase 1 acceptance)

### Unit tests

- `sanitizer.Scrub` — 20 cases (each pattern + composites)
- `classifier.Classify` — 12 fixture logs from prior failures
- `extractFailureWindow` — empty / single hit / multi hit / no hit / huge log
- `vec.HybridSearch` — empty / vec-only / bm25-only / both / no KB match
- `parseRCA` — valid / malformed / partial / wrong schema

### Integration tests (against staging Jenkins + test Postgres)

- happy path: known failing fixture → expected RCA structure
- Jenkins down: 502 returned cleanly
- Postgres down: 503 returned cleanly
- Anthropic 429 simulated: backoff respected
- Webhook HMAC mismatch: 401
- Duplicate webhook: dedup returns prior

### End-to-end shadow mode

Run for 3 days with `silent=true` (no Slack post). Compare:
- LLM root_cause vs human ground-truth on 20 archived failures
- Token cost vs estimate
- Latency p50/p95

Promote to live after 60%+ match on test set.

---

## 8. Metrics to emit (Prometheus-friendly, even Phase 1)

```
bbctl_rca_total{result="success|skip|error", class="parse_error|..."}
bbctl_rca_duration_seconds{quantile="0.5|0.95|0.99"}
bbctl_rca_tokens_total{provider="claude", model="sonnet|opus", kind="input|output|cache_read|cache_write"}
bbctl_rca_cost_usd_total{provider, model}
bbctl_rca_confidence_bucket{le="0.5|0.7|0.9"}
bbctl_rca_tool_calls_total{tool="jenkins.get_log|..."}
bbctl_rca_cache_hits_total{cache="L2|L4|L5"}
bbctl_rca_redaction_rate_bucket
bbctl_rca_provider_errors_total{provider, code}
bbctl_ingestion_chunks_total{kb}
bbctl_ingestion_duplicates_skipped_total{kb}
```

Phase 1: log JSON lines to journald + grep. Phase 2: scrape with Prometheus.

---

## 9. Rollout safety

1. Build + deploy to t3a.large new instance
2. Shadow mode (`SILENT_MODE=true`) — webhook logs result but doesn't post Slack
3. 3-day soak; human reviews 20 RCAs vs ground truth
4. Flip `SILENT_MODE=false` for one job (e.g. `stagger-prod-plus-one`)
5. 7-day soak on single job, monitor cost + Slack signal:noise
6. Roll out to all jobs

Rollback: flip ENV `BBCTL_RCA_ENABLED=false` → backend returns 503 on `/v1/rca/*`; CLI shows graceful disable message.

---

## 10. What's locked / Next step

**Locked:** flow + edge cases + algorithm + test plan above.

**Next:** invoke writing-plans skill to produce `docs/superpowers/specs/2026-05-11-bbctl-rca-phase1-design.md` (detailed implementation plan with files, code skeletons, tasks, owners). Then start coding Week 1 Day 1 tasks (instance migration).
