# Job flow: stagger-prod-plus-one-frontend

## Match
- `script_path` ends with `stagger-prod-plus-one-frontend.groovy`, OR
- `inline_script` contains a stage body
  `prodPlusOneFrontend(params.SERVICE, params.COMMIT_ID)`.
This flow handles FRONTEND services. Do not confuse with the non-
frontend variant — they share many stage NAMES but route to different
wrapper helpers internally.

## Main pipeline
`jenkins_pipeline/stagger-prod-plus-one-frontend.groovy`

## Top-level stages
| Stage marker in console log | Body in main pipeline                         |
|-----------------------------|-----------------------------------------------|
| `(Load Library)`            | inline library load (config.json, aws_account.json) |
| `(Jira Details)`            | `JiraDetails(...)`                            |
| `(Build)`                   | `buildJob(...)`                               |
| `(Prod+1)`                  | `prodPlusOneFrontend(params.SERVICE, params.COMMIT_ID)` |
| `(Infra)`                   | `createGreenInfra(params.SERVICE)`            |
| `(Deploy)`                  | `deploy(params.SERVICE, "prod", params.COMMIT_ID)` |
| `(Rollout)`                 | `rollout(params.SERVICE)`                     |
| `(Destroy)`                 | `destroyBlueInfra(params.SERVICE)`            |

Helper file for each: `jenkins_pipeline/vars/<helperName>.groovy`.

## CRITICAL — Prod+1 is a wrapper

`stage('Prod+1')` only calls `prodPlusOneFrontend(...)`. That helper
declares its OWN inner stages (e.g. `Infra Prod+1`, `Deploy Prod+1`).
Console markers nested under Prod+1 originate inside
`vars/prodPlusOneFrontend.groovy`, not in the main pipeline.

This is the FRONTEND wrapper. Do NOT read `vars/prodPlusOne.groovy` for
this flow — that file belongs to the non-frontend (`main_stagger_prod_plus_one`)
flow and has different helper names inside.

## Drill procedure
1. Read main pipeline body.
2. Identify failed stage marker from log.
3. If marker is `(Prod+1)` or any nested `*Prod+1*`, read
   `vars/prodPlusOneFrontend.groovy` and find the matching inner
   stage block.
4. Drill into the named helper from that stage block.
5. Continue until reaching the line that matches the fatal error.

## Where inner helpers live
- Helper files: `jenkins_pipeline/vars/<helperName>.groovy`
- Resources / templates: `jenkins_pipeline/resources/`
- Terraform invoked by infra helpers: `InfraComposer` repo.
