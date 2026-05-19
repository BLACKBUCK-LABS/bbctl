# Job flow: QA-Automation

## Match
Job whose Jenkins config `script_path` is `QA-Automation.groovy` OR
whose name contains `QA-automation` / `qa-automation`. The pipeline
runs test cases for a service, uploads reports to Slack.

## Main pipeline
`jenkins_pipeline/QA-Automation.groovy`

## Top-level stages
| Stage marker in console log | Body in main pipeline             |
|-----------------------------|-----------------------------------|
| `(git checkout)`            | clone service test repo           |
| `(Run Test Cases)`          | execute test suite                |
| `(Test Reports)`            | gather + upload report to Slack   |

This pipeline is short and self-contained — no `vars/` helper
delegation for the main stages (each stage's body is inline).

## Drill procedure
1. Read main pipeline body — the failing logic is usually inline in
   the stage block (shell scripts, gradle/mvn invocations).
2. If the stage runs a script via `sh`, identify the script and read
   its source if it lives in this repo or the service repo.
3. Service repo for test code: derive from `service.lookup.git_repo`
   if the failure is in service tests.

## Resources
- Slack credentials / upload helpers: built into pipeline body.
- No Terraform, no infra creation.
