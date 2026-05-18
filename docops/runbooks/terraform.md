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

## Pipeline source to cross-check (MANDATORY)
- `jenkins_pipeline/vars/createGreenInfra.groovy` (or similar) — the
  helper that runs `terraform plan/apply`
- `InfraComposer/config/<service>/<env>/main.tf` — the per-service config
- `InfraComposer/module/<module-name>/` — the module being used

## Drill plan
1. `get_jenkins_job_config(job)` → scriptPath
2. `repo_read_file("jenkins_pipeline", "vars/createGreenInfra.groovy", ...)` — see the tf command
3. From log, identify the offending resource (e.g. `aws_instance.alchemist`)
4. Extract service + env from build params or service.lookup
5. `repo_read_file("InfraComposer", "config/<service>/<env>/main.tf", 1, 100)` — see config
6. `repo_search("InfraComposer", "<resource-name>")` — find module that declares this resource
7. `repo_read_file("InfraComposer", "module/<module>/main.tf", ...)` — inspect module
8. If resource-exists conflict: `aws_describe_instance(<id>)` or equivalent describe call to confirm AWS state
9. `repo_recent_commits("InfraComposer", 10)` — check for just-pushed module changes

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
  - `jenkins_pipeline/vars/createGreenInfra.groovy:<line>` (caller)
  - `InfraComposer/config/<service>/<env>/main.tf:<line>` (config)
  - For 'already exists': `aws:instance(<id>)` or equivalent confirming AWS state

## Common pitfalls
- DO NOT suggest `terraform destroy` as a fix — destructive and rarely correct.
- DO NOT cite a TF file you didn't open via `repo_read_file`.
