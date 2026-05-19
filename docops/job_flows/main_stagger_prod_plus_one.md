# Job flow: main_stagger_prod_plus_one

## Match
Job whose Jenkins config `script_path` is `main_stagger_prod_plus_one.groovy`
OR whose `inline_script` contains `prodPlusOne(params.SERVICE)` as a stage
body. Service param `SERVICE` resolves to a web-style backend service
(non-frontend, non-nonweb).

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

## CRITICAL — Prod+1 is a wrapper, NOT a leaf stage

`stage('Prod+1')` in the main pipeline only calls `prodPlusOne(...)`.
The `prodPlusOne` helper file ITSELF declares its OWN inner stages
(`Infra Prod+1`, `Deploy Prod+1`, `Automation`, `Destroy Prod+1`).
Therefore every console log marker like `(Infra Prod+1)` or
`(Deploy Prod+1)` originates INSIDE `vars/prodPlusOne.groovy`, not in
the main pipeline.

If the failed stage marker contains the substring `Prod+1` BUT is not
literally `(Prod+1)` itself:
- Do NOT look in the main pipeline body for that stage.
- READ `jenkins_pipeline/vars/prodPlusOne.groovy` first.
- Find the matching `stage("<marker>")` block inside that helper.
- Read the helper it calls (e.g. by reading the call expression on the
  next line).

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
