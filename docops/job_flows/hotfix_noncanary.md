# Job flow: hotfix-noncanary

## Match
- `script_path` ends with `hotfix-noncanary.groovy`, OR
- `inline_script` contains stage bodies calling `pre_deployment(...)`,
  `instance_provisioning(...)`, `artifact_deployment(...)`,
  `health_validation(...)`, `cutover_cleanup(...)`, with
  `hotfix_rollback()` in the `post { failure { ... } }` block.

## Main pipeline
`jenkins_pipeline/hotfix-noncanary.groovy`

## Top-level stages
| Stage marker in console log | Body in main pipeline             |
|-----------------------------|-----------------------------------|
| `(Load Library)`            | inline library load               |
| `(Jira Details)`            | `JiraDetails(...)`                |
| `(Input Validation)`        | enforces COMMIT_ID vs JFROG_BUILD |
| `(Build Artifact)`          | builds artifact (read body)       |
| `(Pre-Deployment)`          | `pre_deployment(...)`             |
| `(Instance Provisioning)`   | `instance_provisioning(...)`      |
| `(Artifact Deployment)`     | `artifact_deployment(...)`        |
| `(Health Validation)`       | `health_validation(...)`          |
| `(Cutover & Cleanup)`       | `cutover_cleanup(...)`            |
| post                        | `UpdateJiraStatus(...)` then `hotfix_rollback()` on failure |

Helper files: `jenkins_pipeline/vars/<helperName>.groovy`.

## Notes specific to hotfix flow
- NO green/blue cutover at main level; deployment is in-place via
  `artifact_deployment` + `cutover_cleanup`.
- `hotfix_rollback()` runs ON FAILURE in the `post` block — read its
  body if the failure happened during rollback rather than during the
  primary stage.
- This flow is for emergency patches without canary roll-out.

## Drill procedure
1. Read main pipeline body to verify the stage table.
2. Pick failed stage marker from log.
3. Read `vars/<helperName>.groovy` for the matching row.
4. If the helper calls shell scripts (via `libraryResource` or `sh`),
   drill into the script in `resources/scripts/`.
5. Stop at the line that matches the fatal error.

## Resources
- Shell scripts: `jenkins_pipeline/resources/scripts/`
- Templates / config: `jenkins_pipeline/resources/`
- No Terraform in main hotfix path (no green infra creation).
