# Runbook: terraform

## What this class means
Terraform (run from the `Infra` stage) failed to plan/apply. Could be
state drift, syntax error in `.tf`, AWS API quota/permission issue, or
resource already exists.

## Detect signals
- `Error: aws_<resource>.<name> already exists` (already-exists conflict)
- `Error: <provider> error:` (Terraform provider error)
- `terraform plan` exit != 0
- `Error: Reference to undeclared input variable`
- Failed stage = "Infra"

## When to RECLASSIFY out of this runbook

Terraform is often just the messenger. The actual cause may be an AWS
quota / IAM / config issue surfaced THROUGH terraform. Reclassify when:

| If the Error line contains ...                                | error_class    | Use runbook |
|---------------------------------------------------------------|---------------|-------------|
| `TooManyUniqueTargetGroupsPerLoadBalancer`                    | aws_limit     | aws_limit.md|
| `LimitExceeded` / `Service quota exceeded`                    | aws_limit     | aws_limit.md|
| `VcpuLimitExceeded` / `InstanceLimitExceeded`                 | aws_limit     | aws_limit.md|
| `AccessDenied` / `UnauthorizedOperation`                      | aws_limit (perms sub-mode) | aws_limit.md |
| `Stale state detected — auto-destroying` (alone, no Error:)   | NOT a failure — keep scanning down for the real Error: | — |

The "Stale state detected" line is informational chatter emitted by
`precheck.groovy` — it's a NORMAL recovery step that runs BEFORE the
actual terraform apply. If the actual apply later succeeds, the stale
state cleanup is fine. If the apply fails, the Error: line below has
the real cause; do NOT stop at the stale-state line.

## Pipeline source to cross-check (MANDATORY)

Both infra helpers call an `infraComposer()` function that clones the
InfraComposer repo at runtime and runs terraform from within it:

- `(Infra)` stage → `jenkins_pipeline/vars/createGreenInfra.groovy`
  reads `infraComposer(service)` → clones InfraComposer →
  `cd config/<service>/prod/` → terraform init/plan/apply
- `(Infra Prod+1)` stage → `jenkins_pipeline/vars/createRuleForProdPlusOne.groovy`
  reads `infraComposer(service)` → clones InfraComposer →
  `cd config/<service>/prodplusone/` → terraform init/plan/apply

InfraComposer repo layout (use `repo_read_file("InfraComposer", ...)` to read):
- `config/<service>/<env>/main.tf` — root module; declares which
  module from `module/<name>/` to call, plus input variables
- `config/<service>/<env>/variable.tf` — variable declarations for this env
- `module/<name>/main.tf` — the module; typically composes sub-modules
  (e.g. ec2, target-group, tg-attachment, listener-rule sub-modules)

Available env dirs in `config/<service>/`: `prod`, `prodplusone`,
`prod-scale`, `quick-deploy`. Derive the env from the failed stage name.

## Drill plan
1. `get_jenkins_job_config(job)` → confirm scriptPath
2. From log, identify the failing helper: `createGreenInfra.groovy`
   (Infra stage) or `createRuleForProdPlusOne.groovy` (Infra Prod+1);
   read it to find the `infraComposer()` call and the terraform vars passed
3. From log, extract the offending resource address and terraform error
4. Derive service name + env dir from build params or failed stage
5. `repo_read_file("InfraComposer", "config/<service>/<env>/main.tf", 1, 60)`
   → see which module is invoked and what input vars are wired
6. `repo_read_file("InfraComposer", "module/<module>/main.tf", 1, 60)`
   → inspect the module; follow any sub-module `source` refs if error
   points deeper
7. If resource-exists / state-drift conflict: call appropriate
   `aws_describe` for the resource type to confirm current AWS state
8. `repo_recent_commits("InfraComposer", 10)` — check for recent module
   changes that may have introduced the regression

## Action template
```
Finding: Terraform error during <action> on <resource>:
         "<exact error message>".
         <If 'already exists'>: AWS has resource <id>/<name> but
         Terraform state doesn't track it. Either someone created it
         outside Terraform, or state was lost.
         <If syntax/variable error>: <module-or-config>:<line> has
         <description>.
         <If quota>: AWS service quota <name> exceeded; see aws_limit runbook.

Action:
  <If 'already exists'>:
    Option A: Import existing resource into state:
      terraform import <resource-address> <existing-id>
      Then re-run pipeline.
    Option B: Delete the AWS resource if it was created by mistake,
      then re-run.
  <If syntax/variable error>:
    Edit <file>:<line> in InfraComposer repo, fix the syntax, commit,
    push, re-run pipeline.
  <If quota>:
    See aws_limit runbook — request quota increase in AWS console.
Verify:
  Re-run pipeline; expect Infra stage to complete past the error.
```

## Output schema notes
- `error_class: "terraform"`
- `failed_stage: "Infra"`
- `evidence[]` must include:
  - `jenkins_log` with terraform error
  - `jenkins_pipeline/vars/createGreenInfra.groovy:<line>` or
    `jenkins_pipeline/vars/createRuleForProdPlusOne.groovy:<line>` — the caller
  - `InfraComposer/config/<service>/<env>/main.tf:<line>` — root config
  - `InfraComposer/module/<name>/main.tf:<line>` — module with failing resource
  - For 'already exists': `aws:<resource>(<id>)` confirming AWS state

## Common pitfalls
- DO NOT suggest `terraform destroy` as a fix — destructive and rarely correct.
- DO NOT cite a TF file you didn't open via `repo_read_file`.
