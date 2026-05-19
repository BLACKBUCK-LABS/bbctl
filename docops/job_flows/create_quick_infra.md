# Job flow: create-quick-infra

## Match
- `script_path` ends with `create-quick-infra.groovy`, OR
- `inline_script` contains stage bodies calling `QuickBuildJob(...)`
  AND `QuickDeploy(...)` (the QuickBuildJob/QuickDeploy pair is
  distinctive — no other flow uses these).

## Main pipeline
`jenkins_pipeline/create-quick-infra.groovy`

## Top-level stages
| Stage marker in console log | Body in main pipeline                |
|-----------------------------|--------------------------------------|
| `(Load Library)`            | inline library load                  |
| `(Jira Details)`            | `JiraDetails(...)`                   |
| `(Resolve Parameters)`      | resolves SERVICE/COMMIT_ID/JFROG_BUILD into `effectiveParams` |
| `(Input Validation)`        | enforces COMMIT_ID vs JFROG_BUILD constraints |
| `(Build)`                   | `QuickBuildJob(effectiveParams.SERVICE, effectiveParams.COMMIT_ID, effectiveParams)` |
| `(Build Frontend)`          | `buildJobFrontend(effectiveParams.SERVICE, effectiveParams.COMMIT_ID, effectiveParams)` |
| `(Infra)`                   | infra step (read body to identify exact helper for current pipeline version) |
| `(Deploy)`                  | `QuickDeploy(effectiveParams.SERVICE, "prod", effectiveParams + [INSTANCE_IDS: ...])` |
| `(Deploy Frontend)`         | `QuickDeployFrontend(effectiveParams.SERVICE, "prod", effectiveParams + [INSTANCE_IDS: ...])` |
| post                        | `UpdateJiraStatus(params['Jira-Ticket'])` |

Helper files: `jenkins_pipeline/vars/<helperName>.groovy`. The exact
helper invoked from the `Infra` stage varies between
`CreateQuickInfra(...)` and other variants — read the main pipeline
body for the current call before drilling.

## Notes specific to this flow

- Has BOTH backend and frontend tracks (`Build` + `Build Frontend`,
  `Deploy` + `Deploy Frontend`). A failed `(Deploy Frontend)` is in
  the frontend track; do not confuse with `(Deploy)`.
- Uses `effectiveParams` (a merged dict) rather than `params` directly.
  Helpers receive the merged dict — read the helper signature to see
  which keys it expects.
- Service parameter resolves to a "quick infra" test service, often a
  `-devops-test` suffixed name. This flow does NOT use the green/blue
  cutover that `main_stagger_prod_plus_one` uses; there is no
  `Rollout` or `Destroy` stage at the main level.

## Drill procedure
1. Read main pipeline body to confirm exact helper for the failed stage.
2. Read `vars/<helperName>.groovy`.
3. Drill inner calls or `libraryResource`-loaded scripts.
4. Stop at the line matching the fatal error in log.

## Resources used
- Shell scripts: `jenkins_pipeline/resources/scripts/`
- Service config: `jenkins_pipeline/resources/config.json`
- Templates: `jenkins_pipeline/resources/`
- Terraform: `InfraComposer` repo for infra stages.
