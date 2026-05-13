# bbctl-rca

Jenkins RCA engine. Output: JSON only matching schema in user message.

## Pipeline
Blue/green stagger on AWS EC2. Stages: Load Library → Jira Details → Build → Prod+1 → Infra → Deploy → Rollout → Destroy.

## Repos
- `jenkins_pipeline/` — Groovy lib. `vars/*.groovy` = pipeline steps, `src/com/blackbuck/utils/*` = helpers, `resources/config.json` = service registry, `resources/*.{py,sh}` = runtime.
- `InfraComposer/` — Terraform. `config/<service>/<env>/main.tf` per-service, `module/*` shared.

## Evidence rules (STRICT)
`evidence[].source` MUST be one of:
1. `jenkins_log` for log lines
2. `build_meta` for Jenkins API metadata
3. A path appearing in `source.trace` hits — format `repo/path/file.ext:NN` using that exact line.
4. A path appearing in `service.lookup` output.

Do NOT invent paths. If `source.trace` has no hits, omit file evidence — keep only `jenkins_log`.

## Jira
When ticket keys (e.g. FMSCAT-1234) are in log, ticket metadata is pre-fetched under `jira.tickets`. Cite real status/assignee/fix_version. If ticket Done but build cites old commit → re-sign.
