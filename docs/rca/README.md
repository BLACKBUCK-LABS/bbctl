# bbctl RCA — Documentation

| File | Purpose |
|---|---|
| [plan.md](plan.md) | Full Phase 1 architecture, flow, timeline, cost projection |
| [steps_cli.md](steps_cli.md) | Day-by-day setup commands log with status (bbctl-ec2 infra setup) |

## Quick orientation

**What it does:** Jenkins build fails → webhook → bbctl-rca → Claude analyzes log + pipeline code → structured RCA returned to dev + posted to Slack.

**Key components:**
- `internal/rca/` — webhook handler, log window extractor, sanitizer, classifier, prompt builder
- `internal/jenkins/` — Jenkins REST client (GET consoleText + api/json)
- `internal/mcp/` — MCP server on :7070 exposing repo/docs/service tools to Claude
- `internal/llm/` — Anthropic SDK wrapper (Gemini temp → Claude prod)
- `internal/cache/` — boltdb for dedup + tool cache
- `prompts/` — system prompt + few-shot examples (quality lever)
- `infra/` — systemd timers for nightly repo sync + S3 docs sync

**bbctl-ec2 paths:**
- `/opt/bbctl-rca/repos/` — jenkins_pipeline (master) + InfraComposer (main), nightly pull 02:00 UTC
- `/opt/bbctl-rca/docops/` — 11 org markdown docs, nightly sync 02:30 UTC
- `/etc/bbctl-rca/keys.enc.yaml` — SOPS-encrypted secrets (age key at `/etc/bbctl-rca/keys/bbctl-rca.key`)
- `/var/cache/bbctl-rca/` — boltdb cache
- `/var/log/bbctl-rca/` — app + sync logs
