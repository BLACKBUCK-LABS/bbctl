# Job flow: stagger-nonweb

## Match
Job whose Jenkins config `script_path` is `stagger-nonweb.groovy` OR
whose `SERVICE` parameter resolves to a non-web service (typically
suffixed `-nonweb`, `-non-web`, `-nw-*`, or otherwise marked
`is_non_web=true` in `service.lookup`).

## Main pipeline
`jenkins_pipeline/stagger-nonweb.groovy`

## Top-level stages
| Stage marker in console log | Body in main pipeline               |
|-----------------------------|-------------------------------------|
| `(Load Library)`            | inline library load                 |
| `(Jira Details)`            | `JiraDetails(...)`                  |
| `(Build)`                   | `buildJob(...)`                     |
| `(Infra)`                   | `createGreenInfra(...)`             |
| `(Deploy)`                  | `deploy(..., "prod")`               |
| `(Rollout)`                 | `rollout(...)`                      |
| `(Destroy)`                 | `destroyBlueInfra(...)`             |
| post-failure                | `rollbackMain("non_web_rollback",...)` |

Helper file for each: `jenkins_pipeline/vars/<helperName>.groovy`.

## Notes specific to non-web

- This flow has NO `Prod+1` stage. There is no `prodPlusOne` wrapper.
  Console markers like `(Infra Prod+1)` do NOT appear in this flow.
- `deploy(...)` branches inside its body based on service type
  (`isNonWeb` check). Drill into `vars/deploy.groovy` to see the
  branch the failing service takes.
- `rollout(...)` likewise branches; non-web services route through
  `nonwebRollout(...)`. Drill into `vars/rollout.groovy` to verify.

## Drill procedure
1. Read main pipeline body to confirm the table above is current.
2. Pick failed stage marker from log.
3. Read the helper file from the matching row.
4. If the helper branches on `service_type` / `isNonWeb`, follow the
   non-web branch.
5. Drill inner calls until reaching the file/line that matches the
   fatal error.

## Resources used by this flow
- Shell scripts: `jenkins_pipeline/resources/scripts/*.sh`
- Service config: `jenkins_pipeline/resources/config.json`
- Templates: `jenkins_pipeline/resources/{filebeat.yml,supervisor.conf,fluent-bit.conf,parsers.conf,fluent-bit-config.json}`
- Terraform for infra stages: `InfraComposer` repo.
