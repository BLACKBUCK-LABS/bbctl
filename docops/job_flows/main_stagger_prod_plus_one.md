# Job flow: main_stagger_prod_plus_one

## Match
- `script_path` (from `get_jenkins_job_config`) ends with
  `main_stagger_prod_plus_one.groovy`, OR
- `inline_script` (when `script_path` is null) contains a stage body
  `prodPlusOne(params.SERVICE)` AND does NOT contain
  `prodPlusOneFrontend(...)`.
The Jenkins display name is irrelevant — match on script_path or
inline_script content only.

## Main pipeline
`jenkins_pipeline/main_stagger_prod_plus_one.groovy`

## Top-level stages (read main pipeline body to verify)
The main pipeline runs the following stages in order. Each stage's body
calls a single helper step:

| Stage marker in console log | Body in main pipeline                 |
|-----------------------------|---------------------------------------|
| `(Load Library)`            | inline library load                   |
| `(Jira Details)`            | `JiraDetails(...)`                    |
| `(Build)`                   | `buildJob(...)`                       |
| `(Prod+1)`                  | `prodPlusOne(params.SERVICE)`         |
| `(Infra)`                   | `createGreenInfra(params.SERVICE)`    |
| `(Deploy)`                  | `deploy(params.SERVICE, "prod")`      |
| `(Rollout)`                 | `rollout(params.SERVICE)`             |
| `(Destroy)`                 | `destroyBlueInfra(params.SERVICE)`    |
| post-failure                | `rollbackMain(...)` then UpdateJira   |

The helper file for each row is `jenkins_pipeline/vars/<helperName>.groovy`
by the universal Jenkins shared-lib convention. VERIFY by reading the
main pipeline body — do not trust this row alone.

## CRITICAL — Prod+1 is a WRAPPER. Apply this rule BEFORE reading any helper.

**Rule (deterministic):**
- If the failed stage marker from log is `(Prod+1)` exactly → it is a
  LEAF stage in the main pipeline; read the helper that
  `stage('Prod+1')` calls.
- If the marker is `(Infra Prod+1)`, `(Deploy Prod+1)`,
  `(Automation)`, or `(Destroy Prod+1)` → these are NOT declared in
  the main pipeline. They are NESTED stages inside
  `vars/prodPlusOne.groovy`. You MUST read `vars/prodPlusOne.groovy`
  FIRST — do not read `vars/createGreenInfra.groovy` or
  `vars/deploy.groovy` first.

**Why this matters:** the main pipeline ALSO has separate `stage('Infra')`
and `stage('Deploy')` that delegate to leaf helpers (`createGreenInfra`,
`deploy`). Those handle the GREEN-INFRA + main DEPLOY flow, NOT the
Prod+1 sub-stages. Reading the wrong file leads to wrong evidence.

**Chain for any `*Prod+1*` marker (except literal `(Prod+1)`):**
1. `repo_read_file("jenkins_pipeline", "vars/prodPlusOne.groovy", 1, 80)`
2. Find the `stage("<marker>")` block inside its body.
3. Read the helper name the next line invokes
   (e.g. for `stage("Infra Prod+1")` the next line is the inner
   helper call; that is the file you drill into next, not the file
   for `stage('Infra')` in the main pipeline).
4. Continue drilling that helper until you reach the line whose
   content matches the fatal error from the log.

## Drill procedure (every failed RCA for this flow)

1. Read main pipeline body once to confirm the stage/helper mapping
   above is still accurate for the current pipeline version.
2. Identify the failed stage marker from console log
   (`[Pipeline] { (<StageName>)` last occurrence before fatal error).
3. If the marker matches a row in the table above, read that helper
   file. Otherwise treat it as a nested stage and read the wrapper
   helper for that branch first (most commonly `prodPlusOne` for any
   `*Prod+1*` marker).
4. From the helper body, identify any inner helper call or
   `libraryResource` reference relevant to the failure, and drill
   into that file next.
5. Stop drilling when the file you are reading contains the line that
   matches the fatal error in the log.

## Where the inner helpers live
- All helper files: `jenkins_pipeline/vars/<helperName>.groovy`
- Shell scripts referenced by `libraryResource`: `jenkins_pipeline/resources/scripts/<name>.sh`
- Top-level config / templates: `jenkins_pipeline/resources/`
- Canary script: `jenkins_pipeline/resources/canary.py`
- Terraform code invoked by `infraComposer(...)` call inside infra
  helpers: lives in the `InfraComposer` repo (separate clone).

## Where to look for AWS state
- `aws_account`, `aws_region`, `rule_arn`, `lb_listener_arn`,
  `target_port` come from `service.lookup(<service>)` in the primer
  (already given to you).
- For ALB / target group state, use `aws_describe` with appropriate
  operation; the TargetGroupArn / ListenerArn must come from the log,
  service.lookup, or a `repo_read_file` result — never invent.
