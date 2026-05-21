# Job flow: create-quick-infra

## Match
- `script_path` ends with `Jenkinsfile_create_quick_infra`, OR
- `inline_script` contains stage bodies calling `QuickBuildJob(...)`
  AND `QuickDeploy(...)` (the QuickBuildJob/QuickDeploy pair is
  distinctive — no other flow uses these).

## Main pipeline
`jenkins_pipeline/Jenkinsfile_create_quick_infra`

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

## Compliance gate behavior (Jira Details stage) — DIFFERENT from other flows

`create-quick-infra` is the bootstrap job — it spins up infra for a NEW
service that, by design, does not yet exist in
`jenkins_pipeline/resources/config.json`. The compliance gate in
`vars/JiraDetails.groovy` was patched in May 2026 to source the service
identity from the **git build parameters** (`SERVICE` / `COMMIT_ID` /
repo URL passed in by the trigger) when invoked from this job, and to
treat `config.json` as an enrichment lookup only (for team, NewRelic
name, Jira board, etc.).

What this means for RCAs on the Jira Details stage of this flow:

- A `Compliance: SERVICE '<svc>' not found in config.json` failure on
  `create-quick-infra` is **not** a "missing config entry" — it is a
  gate-logic regression. The current intended behavior is to fall
  back to the build-param value. See
  `docops/runbooks/compliance.md` Mode 6 for the drill plan.

- Do NOT recommend editing `config.json` to add the new service for
  this job. That re-couples the gate to a file the patch specifically
  decoupled it from, and masks the regression.

- For OTHER flows (`main_stagger_prod_plus_one`, deploy jobs, canary
  jobs), a missing `config.json` entry IS a legitimate failure and
  the registration fix is correct — that flow's compliance gate
  *does* require `config.json` membership. The build-param fallback
  is specific to the quick-infra bootstrap case.

When the LLM is unsure whether the gate code at HEAD still has the
build-param fallback, it should:
1. `repo_recent_commits("jenkins_pipeline", 10)` — find the May-2026
   patch on `vars/JiraDetails.groovy`.
2. `repo_read_file("jenkins_pipeline", "vars/JiraDetails.groovy", ...)`
   at the service-lookup lines — verify the quick-infra branch still
   reads from build params.

If the fallback is missing, the patch was reverted or never reached
this branch — fix the gate code, do not work around in `config.json`.

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
