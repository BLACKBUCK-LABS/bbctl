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

## CRITICAL — Prod+1 is a WRAPPER. Apply this rule BEFORE reading any helper.

**Rule (deterministic):**
- If the failed stage marker is `(Prod+1)` exactly → leaf stage; read
  the helper `stage('Prod+1')` calls (`prodPlusOneFrontend`).
- If the marker is `(Infra Prod+1)`, `(Deploy Prod+1)`, or any other
  marker containing `Prod+1` but not literally equal to `(Prod+1)` →
  NESTED stage inside the FRONTEND wrapper. You MUST read
  `vars/prodPlusOneFrontend.groovy` FIRST.

**Do NOT use `vars/prodPlusOne.groovy` for this flow.** That file
belongs to the non-frontend flow (`main_stagger_prod_plus_one`) and
declares different inner stages / helper names. Wrong file → wrong
evidence.

**Chain for any `*Prod+1*` marker (except literal `(Prod+1)`):**
1. `repo_read_file("jenkins_pipeline", "vars/prodPlusOneFrontend.groovy", 1, 80)`
2. Find the `stage("<marker>")` block inside its body.
3. Read the helper name invoked on the next line.
4. Drill until you reach the line matching the fatal error.

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
