# bbctl-rca RAG — Workflow & Internals

This doc describes the Retrieval-Augmented Generation layer added to
`bbctl-rca` on the `feature/bbctl-rca-agent-RAG` branch. It is the
companion to `bbctlrca.md`, which describes the agent itself.

The agent (without RAG) re-reads the same on-disk runbooks every build
and has no memory of past incidents. RAG gives the agent two things the
non-RAG path cannot:

1. **Semantic search across all docs** — runbooks/, job_flows/, and the
   22 org docs in `docops/` — without spending a tool call per file.
2. **Past-incident memory** — every RCA written to `audit/*.json` is
   embedded and becomes retrievable. "We saw this failure last Tuesday;
   the fix was X" becomes a vector lookup.

The implementation is dormant infrastructure on this branch — the agent
loop in `agent.py` is not yet wired to call into RAG. That wiring lands
in R2 (the `rag_search` MCP tool) and R3 (auto-inject into the system
prompt). R1 (this branch) is the substrate.

---

## 1. What RAG is in 30 seconds

```
Indexing (offline, cron):
  doc / audit / log → chunked text → OpenAI embedding (1536d vector)
                                  → row in rca_chunks (Postgres + pgvector)

Query (online, per build):
  log_window → OpenAI embedding (1536d) → cosine search rca_chunks
                                       → top-k similar chunks
                                       → injected into agent prompt
```

An *embedding* is a fixed-size numeric fingerprint of a piece of text,
produced by an LLM-trained encoder. Two texts that mean similar things
land near each other in the 1536-dimensional embedding space. We
measure "near" with cosine similarity. `pgvector`'s `<=>` operator
returns cosine *distance* (0 = identical), so we convert with
`1 - distance` to get similarity (1 = identical).

---

## 2. Architecture

```
                         OpenAI Embeddings API
                         (text-embedding-3-small, 1536d, $0.02/1M tok)
                                   ▲
                                   │ embed(text)
                                   │
   ┌───────────────────────────────┴────────────────────────────────┐
   │                            rag.py                              │
   │                                                                │
   │   embed()                — with query_emb_cache (24h TTL)      │
   │   upsert(rows)           — content_hash dedupe                 │
   │   search(q, k, filters)  — with retrieval_cache (2h TTL)       │
   │                                                                │
   │   index_docops()         — walk bbctl/docops/*.md              │
   │   index_audits(dir)      — walk /var/log/bbctl-rca/audit/*.json│
   │   index_log_window(...)  — per-build error window embed        │
   └────────────────┬───────────────────────────────────────────────┘
                    │
                    ▼
            Postgres 16 + pgvector 0.8
            ┌────────────────────────────────────┐
            │ rca_chunks                         │
            │   id, source_type, source_id,      │
            │   chunk_idx, chunk_text,           │
            │   embedding VECTOR(1536),          │
            │   meta JSONB, content_hash         │
            │   + HNSW(embedding) + GIN(meta)    │
            │   + GIN(to_tsvector(chunk_text))   │
            │                                    │
            │ query_emb_cache  (24h)             │
            │ retrieval_cache  (2h)              │
            └────────────────────────────────────┘
```

`source_type` is one of `runbook | doc | job_flow | audit | log`. The
indexer functions emit rows with the right `source_type` so the query
side can filter (e.g. "search only past audits with error_class =
aws_limit").

---

## 3. Codebase changes on this branch

```
bbctl/
├── bbctl_rca/
│   ├── rag.py                  NEW — ~400 LOC, embed/upsert/search + CLI indexers
│   ├── rag_schema.sql          NEW — rca_chunks + caches + HNSW/GIN indexes
│   └── requirements.txt        MOD — adds psycopg[binary] + pgvector deps
└── infra/scripts/
    └── rag-postgres-install.sh NEW — install PG 16 + pgvector, create DB, stash secret
```

Nothing else on this branch touches the existing agent / classifier /
prompt code paths. That separation is intentional — R1 is dormant
until R2+R3 wire it into `agent.py`. Today, RAG is a CLI tool and a
Python module; the bbctl-rca FastAPI service does not import it yet.

### 3a. `rag.py` — public surface

```python
from bbctl_rca import rag

# Indexing (offline, run by cron)
rag.index_docops()                              # -> {"files": 34, "written": ~150}
rag.index_audits()                              # -> {"files": 170, "written": 170}
rag.index_log_window(job, build, log, eclass)   # -> {"written": 1}

# Query (online, per build — once R2/R3 land)
rag.search(
    query="ALB TooMany unique target groups limit",
    k=5,
    source_types=["runbook", "doc"],            # optional filter
    error_class="aws_limit",                    # optional filter
)
# -> [{"id", "source_type", "source_id", "chunk_text", "meta", "score"}, …]
```

### 3b. `rag_schema.sql` — schema highlights

* `rca_chunks (source_type, source_id, chunk_idx)` is the natural key;
  re-indexing the same doc updates in place via `ON CONFLICT`.
* `content_hash` is sha256 of normalized chunk_text — `ON CONFLICT DO
  UPDATE WHERE content_hash IS DISTINCT FROM EXCLUDED.content_hash`
  makes re-runs cheap. Identical chunks do not get re-embedded.
* `HNSW(embedding vector_cosine_ops)` index with `m=16,
  ef_construction=64`. HNSW recall is excellent at our scale (low
  thousands of chunks) and avoids the IVFFlat `lists` tuning headache.
* `GIN(meta)` for cheap pre-filtering by `error_class` /
  `source_type` / `job` before vector rank.
* `GIN(to_tsvector(chunk_text))` lets future R2 do hybrid search
  (vector + BM25 fusion) without a schema migration.
* Two cache tables: `query_emb_cache` (avoid re-paying for repeat log
  embeddings) and `retrieval_cache` (skip the pgvector scan when we
  just answered the same query). Both TTL-bounded.

### 3c. `rag-postgres-install.sh` — what it does

Idempotent install for the EC2 host that runs `bbctl-rca`:

1. Apt-installs Postgres 16 from PGDG + dev headers + `build-essential`.
2. Clones pgvector v0.8.0 and builds against the PG 16 `pg_config`.
   (Building from source is the reliable way to land a matching
   binary; the PGDG apt package occasionally lags.)
3. Creates database `bbctl_rca` and role `bbctl_rca` with a
   freshly-generated password (`openssl rand -hex 24`).
4. Enables `vector` + `pg_trgm` extensions in that DB, applies
   `rag_schema.sql`, grants on schema/tables/sequences to the role.
5. Stashes `pg_password`, `pg_host`, `pg_db`, `pg_user`, `pg_port`
   keys into the existing `bbctl-rca/prod` AWS Secrets Manager secret
   (merging with whatever is already there). The bbctl-rca service
   already reads this secret on boot via `bbctl_rca/secrets.py` and
   exports each key as `BBCTL_<KEY>`, so `rag.py` sees
   `BBCTL_PG_PASSWORD` etc. with no code change in the service.

Operator must run this with an AWS profile that has
`secretsmanager:PutSecretValue` permission. The default
`zinkareadonly` profile cannot. Pass `AWS_PROFILE=<write-profile>`.

---

## 4. The two pipelines

### 4a. Indexing pipeline (cron-driven)

```
            ┌──────────────────────┐
            │  bbctl-rca-sync.sh   │   (existing cron, every 2h)
            │  git pull docops/    │
            └──────────┬───────────┘
                       │ on success
                       ▼
            ┌──────────────────────┐
            │  python -m bbctl_rca │
            │   .rag index-docops  │
            └──────────┬───────────┘
                       │
                       ▼
      For each .md in docops/:
        1. classify source_type (runbook | job_flow | doc) by subdir
        2. chunk on `## ` H2 boundaries, with a 2000-char ceiling
           and 200-char overlap between adjacent chunks
        3. for each chunk: SHA256 it; embed via OpenAI
        4. UPSERT into rca_chunks; content_hash skips unchanged rows

Separate cron (post-RCA hook, R4): index_audits() embeds each
new audit JSON. Each audit becomes one chunk that includes summary,
root_cause, and the suggested_commands list — so retrieval matches
both "what broke" and "what fix worked".
```

### 4b. Query pipeline (per build, future R2/R3)

```
  Jenkins build fails → POST /v1/rca → agent.run_agent()
                                        │
                                        ├── classifier → error_class
                                        ├── build_initial_tool_ctx()  [existing]
                                        │
                                        ├── NEW (R3): rag.search(
                                        │      query=log_window,
                                        │      k=3, error_class=<class>)
                                        │   → top-3 chunks injected as
                                        │     "## retrieved context" block
                                        │
                                        ├── LLM iter loop (OpenAI gpt-4o tool calls)
                                        │   NEW (R2): tool `rag_search`
                                        │     so the LLM can pull more
                                        │     chunks mid-investigation
                                        │
                                        └── validator + JSON output
```

Two ways the agent gets retrieval value:

* **Auto-inject (R3)** — the orchestrator embeds the log window once and
  pastes the top-k chunks into the system prompt before the first LLM
  call. Cheap, deterministic, no tool-call round-trip.
* **Tool-call (R2)** — the LLM decides when it needs more context and
  calls `rag_search("similar past TooMany ALB failures")` itself.
  Useful when iter-0 chunks were not enough and the LLM is mid-trace.

Both use the same `rag.search()` Python function.

---

## 5. Caches — why two layers

| Layer | Key | Skips | TTL | Hit-rate (est) |
|---|---|---|---|---|
| `query_emb_cache` | sha256(normalized log_window) | OpenAI embedding API call | 24h | ~30% (flapping builds reuse log fingerprint) |
| `retrieval_cache` | sha256(query + filters + k) | pgvector HNSW scan | 2h | ~50% (same build family re-runs) |

Even without caches the cost is negligible (~$0.001 per build for the
query embedding), but the caches mostly buy *latency* — a cache hit
returns in <5ms versus ~80ms for an API + vector scan round-trip.

The 2h TTL on the retrieval cache aligns with the docops sync cron, so
a freshly-indexed runbook becomes retrievable on the next cache miss
without manual invalidation.

---

## 6. Chunking strategy

```python
TARGET_CHARS  = 2000     # ~500 tokens
OVERLAP_CHARS = 200      # ~50 tokens
```

* Split on `## ` H2 headers first, keeping the heading inside each
  section so the chunk is self-contained when shown out of order.
* If a section exceeds 2000 chars, slide a fixed-size window across it
  with 200-char overlap. Overlap prevents context loss at the boundary.
* `terraform.md` (8884 chars) becomes 12 chunks; small runbooks become
  a single chunk. The verifier in `rag.py` test (`_chunk_markdown`)
  shows the boundaries align cleanly to section headings.

Why this size: text-embedding-3-small accepts up to 8192 tokens per
input but loses retrieval precision at the high end. ~500 tokens is
the empirically reliable sweet spot — small enough that a single
chunk is a focused topic, large enough that the heading + a few
paragraphs of body fit together.

---

## 7. Cost model

| Workload | Volume | Cost |
|---|---|---|
| One-shot full index of `docops/` (~34 files) | ~50K tokens total | ~$0.001 |
| One-shot full index of `audit/` (~170 RCAs) | ~50K tokens | ~$0.001 |
| Per-build query embedding (with cache hits) | 1 call, ~500 tok | ~$0.00001 |
| Storage in PG | ~5MB for the chunks + indexes | free |
| Embed re-runs (content_hash skip) | ~0 for unchanged files | $0 |

Hundred builds a day with no cache hits ≈ $0.001/day on embeddings.
Storage and PG sit on the existing EC2 disk. The dominant cost
remains the agent loop itself ($0.30/build); RAG is rounding error.

---

## 8. Deploy steps (EC2)

```bash
# 1. Install PG + pgvector + DB + secret (needs WRITE profile, not zinkareadonly)
sudo AWS_PROFILE=<write-profile> \
  bash bbctl/infra/scripts/rag-postgres-install.sh

# 2. New Python deps inside the service venv
source /opt/bbctl-rca/venv/bin/activate
pip install -r bbctl/bbctl_rca/requirements.txt

# 3. Restart so the service loads BBCTL_PG_* from the refreshed secret
sudo systemctl restart bbctl-rca

# 4. Smoke: embed something
python -m bbctl_rca.rag embed "hello world"
# → dim=1536 first5=[...]

# 5. Index docops (one-shot; later wire into cron post-pull hook)
python -m bbctl_rca.rag index-docops

# 6. Index past audits
python -m bbctl_rca.rag index-audits

# 7. Smoke: semantic search
python -m bbctl_rca.rag search "ALB unique target groups limit reached" 3
```

PG status sanity:

```bash
sudo -u postgres psql -d bbctl_rca -c '\dx'        # extensions
sudo -u postgres psql -d bbctl_rca -c '\dt'        # tables
sudo -u postgres psql -d bbctl_rca -c '
  SELECT source_type, count(*)
  FROM rca_chunks
  GROUP BY source_type ORDER BY 1;'
```

---

## 9. Roadmap

| Phase | Scope | Status |
|---|---|---|
| **R1** | PG + pgvector install, schema, `rag.py`, indexer CLI | ✅ shipped |
| **R2** | `rag_search` MCP tool wired into agent's tool_schemas/dispatch | ✅ shipped |
| **R3** | Auto-inject top-k into agent system prompt every build | ✅ shipped |
| **R3.1** | Selectivity tuning — audit-only for known class, Error: line anchor as query | ✅ shipped |
| **R4** | Audit indexer hook — `index_audits()` runs on each RCA write | next |
| **R5** | Log-window embed per build + nearest-past-build lookup tool | next |
| **R6** | Operator feedback (thumbs up/down) → meta.operator_verdict → re-rank | next |

R2+R3 unlock the visible quality win — until then RAG is a CLI you can
poke at, not something the agent uses. R4 turns past RCAs into a
growing knowledge base. R5 catches "we saw this exact log last week"
patterns. R6 closes the feedback loop so wrong RCAs get downweighted.

---

## 10. Things that would change if we evolved this

* **Embedding model swap** — `text-embedding-3-large` (3072d) buys
  modest recall at 6× cost. Hold off unless eval shows recall@5 < 0.8.
* **Local embeddings** — `bge-small-en` runs on CPU for free; a good
  hedge against OpenAI outages but requires a quantization decision.
* **RDS/Aurora migration** — only if we need multi-instance bbctl-rca
  fan-out. Single EC2 PG is fine for current volume.
* **Hybrid retrieval (vector + BM25)** — schema already has the GIN
  full-text index. R2 can opt into RRF fusion with no migration.
* **Re-ranker** — a cross-encoder pass over top-25 candidates to pick
  top-5 raises precision noticeably; cohere-rerank or a local
  `bge-reranker-base` are the usual choices. Worth doing once R6's
  operator feedback shows where the current ranker is failing.

---

## 11. Lessons learned from first deployment

### 11.1 First R3 cut was redundant for known classes

The initial R3 implementation injected top-4 chunks from `{runbook, doc, audit}`
on every build. For a known `error_class`, the per-class runbook is already
pre-loaded as the `## runbooks.<class>` block (the 5c pre-fetch in
`_build_tool_context`). RAG kept returning slices of the same runbook the
model already had — `~1K tokens wasted per build`, zero new signal.

Build 5177 (aws_limit) post-R3, pre-R3.1:
```
cost      $0.473
input     182K tokens
tool_calls 9
retrieved.rag → 4× chunks all from runbooks/aws_limit.md (same as ## runbooks.aws_limit)
```

### 11.2 Full log_window as query was too noisy

`text-embedding-3-small` precision drops fast on noisy queries. The Jenkins
log window includes NewRelic agent chatter, terraform plan output, unzip
listings, build artefacts — all irrelevant. The fatal `Error:` / `Exception:`
/ `FAIL:` line is the strongest semantic signal in the window.

CLI smoke proved this:
```
Query "TooManyUniqueTargetGroupsPerLoadBalancer ALB orphan target group"
  → top hit at score 0.608 (specific build-5177 chunk)

Query (full log_window, ~6KB of mixed signal)
  → top hit at score 0.384 (generic Action template chunk)
```

### 11.3 R3.1 fix — selectivity + sharper query

`bbctl/bbctl_rca/llm.py` was changed to:

1. **Anchor query on the fatal log line.** Extract the last `Error:` /
   `Exception:` / `FAIL:` / `FAILURE:` / `Caused by:` line + 500 char suffix.
   Fall back to `log_window[:6000]` if no anchor matches.

2. **Restrict source_types by class.** Known class → `["audit"]` only
   (past-incident memory; runbook is already in the prompt via 5c).
   Unknown class → `["runbook", "doc", "audit"]` (full corpus; no
   class-specific runbook to lean on).

3. **k lowered 4 → 3.** Tighter scope; less prompt bloat.

4. **Distinct header text** — `## retrieved.rag (top-k past-incident matches)`
   for known class vs `(top-k semantic matches)` for unknown — so the LLM
   knows what it's looking at.

Build 5177 post-R3.1:
```
cost       $0.356        (-25% vs R3)
input      134K tokens   (-26% vs R3)
tool_calls  7            (-2 vs R3)
retrieved.rag → 1× past-incident audit chunk (class=aws_limit, score 0.281)
```

Quality preserved (real ALB ARN, real evidence cites, validator caught
`<orphan_arn>`). Token+cost reduction came purely from killing redundancy.

### 11.4 LLM rarely calls `rag_search` even though it's available

R2 exposed `rag_search` as a function tool. Across multiple build-5177
runs, the agent never invoked it on its own — it uses `read_runbook`
and `repo_search` instead. R3.1 auto-inject covers the main case, so
the tool is currently a fallback for edge cases (unknown class with
deep drill needs, follow-up queries mid-investigation). Don't remove
the tool; do soften expectations about LLM-driven usage.

---

## 12. Optimization paths (concrete next moves)

Sorted by leverage. (R4–R6 are roadmap items; the rest are sub-tunings
worth queuing alongside.)

### High leverage — ship next

| ID | Idea | Effort | Expected win |
|---|---|---|---|
| **R4** | Index each new RCA on write path (`audit.write()` → `rag.index_audits()`) | 30 min | Memory grows passively. Today: 12 audits. After 2 weeks: ~100+. |
| **R5** | Per-build log-window embed + `nearest_past_build(log)` tool | 1 hr | "We saw this exact log signature before" — works even before a runbook exists |
| **eval-harness** | Save (log_window, expected_chunk_ids) pairs; nightly recall@5 metric | 2 hr | Stop guessing whether tuning helped; quantify it |

### Mid leverage — soak first, decide later

| ID | Idea | Effort | Expected win |
|---|---|---|---|
| **R6** | `/v1/rca/feedback` endpoint → `meta.operator_verdict` → boost/penalize at rank time | 2 hr | Close the loop. Validated chunks rank higher; bad ones decay |
| **hybrid-retrieval** | Vector + BM25 (`to_tsvector`) fused via RRF | 1 hr | Catches exact-token matches (quota codes like `L-417A185B`) that pure vector misses |
| **re-ranker** | Cross-encoder pass over top-25 → return top-5 by precision | 4 hr | Bigger recall gain than embedding model swap, smaller cost than `-large` |
| **smarter chunking** | Semantic chunking (sentence-window) instead of fixed-size + H2 split | 2 hr | Higher precision on tight queries; lower recall on broad ones — needs eval |

### Low leverage — defer until pain shows

| ID | Idea | Effort | Expected win |
|---|---|---|---|
| `text-embedding-3-large` | 6× cost, modest recall lift | 5 min config | Worth only if eval shows recall@5 < 0.8 |
| Local embeddings (`bge-small-en`) | CPU-only, free, slower | 2 hr | Outage hedge; not a quality win |
| RDS migration | Managed PG, multi-az | 4 hr | Only when bbctl-rca fans out across instances |
| Vector dimensionality reduction | PCA to 512d for storage savings | 2 hr | Premature — storage is free here |

### Why this ordering

* **R4 first** because RAG without growing memory plateaus immediately.
  12 frozen audits = a snapshot, not a memory.
* **R5 next** because it unlocks the "I've seen this exact failure" case
  that's invisible to the current class-tagged retrieval.
* **eval-harness** so we stop arguing about whether R3.1 / R6 / re-ranker
  helped — measure recall@5 over a frozen test set of 20 historical
  RCAs, run the metric nightly.
* **R6 after eval** because R6 changes ranking — without eval we won't
  know if it's actually a win.
* **Hybrid retrieval** is cheap and the GIN index already exists; ship
  alongside R4/R5 once we have eval evidence pure-vector is missing
  cases.
* **Re-ranker** is the biggest leverage but worst ROI without a baseline
  metric — its whole pitch is "we picked the wrong top-5"; if you can't
  measure that, you can't tell whether the re-ranker helped.

---

## 13. How RAG is actually wired (end-to-end, current state after R3.1)

```
                             ╔═══════════════════════════════════════╗
                             ║   bbctl-rca service on EC2            ║
                             ║                                       ║
                             ║  Jenkins POST /v1/rca                 ║
                             ║       │                               ║
                             ║       ▼                               ║
                             ║  classifier(log_window) → error_class ║
                             ║       │                               ║
                             ║       ▼                               ║
                             ║  AGENT_CLASSES?                       ║
                             ║       │  yes                          ║
                             ║       ▼                               ║
                             ║  build_initial_tool_ctx(...)          ║
                             ║       ├── service.lookup              ║
                             ║       ├── source.trace                ║
                             ║       ├── docs.<CLASS_DOCS>           ║
                             ║       ├── runbooks.<class> (5c)       ║
                             ║       └── retrieved.rag (R3.1)  ◀──── ║ ◀── extract Error: line
                             ║                                       ║         ▼
                             ║                                       ║     rag.search(query, k=3,
                             ║                                       ║       source_types=["audit"]   ◀── known class
                             ║                                       ║         or full corpus,        ◀── unknown
                             ║                                       ║       error_class=<class>)
                             ║                                       ║         ▼
                             ║                                       ║     query_emb_cache → hit? skip embed
                             ║                                       ║         ▼
                             ║                                       ║     OpenAI embed(query)
                             ║                                       ║         ▼
                             ║                                       ║     retrieval_cache → hit? return ids
                             ║                                       ║         ▼
                             ║                                       ║     pgvector HNSW search
                             ║                                       ║         ▼
                             ║                                       ║     top-3 chunks
                             ║       │                               ║
                             ║       ▼                               ║
                             ║  run_agent(initial_ctx, ...)          ║
                             ║       │ iter loop (max ~8 iters)      ║
                             ║       ▼                               ║
                             ║  validator (Phase-10 annotate-only)   ║
                             ║       │                               ║
                             ║       ▼                               ║
                             ║  audit/<request_id>.json              ║  ◀── R4 will hook here
                             ║       │                               ║
                             ║       ▼                               ║
                             ║  return JSON to Jenkins               ║
                             ╚═══════════════════════════════════════╝
```

The `retrieved.rag` block is the only RAG-specific touch in the request
path. Everything else is the existing agent architecture.

---

## 14. References

* [pgvector](https://github.com/pgvector/pgvector) — extension + HNSW + IVFFlat
* [OpenAI Embeddings](https://platform.openai.com/docs/guides/embeddings) — model + pricing
* `bbctl/docs/rca/bbctlrca.md` — main bbctl-rca service doc; this is the companion
* `bbctl/bbctl_rca/llm.py` — `_build_tool_context` is where the R3 / R3.1 inject lives
* `bbctl/bbctl_rca/rag.py` — public surface: `embed`, `upsert`, `search`, `index_docops`, `index_audits`
