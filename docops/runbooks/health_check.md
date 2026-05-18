# Runbook: health_check

## What this class means
The ALB target group probe never returned healthy for the new instance
during deploy. Pipeline aborts in the `Deploy` (or `Deploy Prod+1`) stage
after N failed poll iterations. The service either crashed at startup,
bound to the wrong port, or the health-check path returns non-2xx.

**Important — scope of this RCA:** RCA confirms WHERE the failure is
(which TG, which instance, what config it expected) but DOES NOT log
into the instance to determine WHY the service is unhealthy. That last
step is for the operator via `bbctl shell <instance_id>`. We never use
SSM SendCommand or any other instance-shell path.

## Detect signals
- `Health Status failed to move to healthy within the time limit`
- `ALB target unhealthy`
- `Iteration <N> of <M>: still draining` repeated
- Failed stage = "Deploy" or "Deploy Prod+1"

## Pipeline source to cross-check (MANDATORY)
- `jenkins_pipeline/vars/deployProdPlusOne.groovy` (or `nonwebdeploy.groovy`
  for plain Deploy stage)
- `jenkins_pipeline/scripts/non_web_healthy.sh` (the poll loop)

## Drill plan — execute ALL in parallel in iter 0

In a single LLM iteration, emit these tool calls in parallel:

1. `get_jenkins_job_config(job)` → confirm scriptPath
2. `repo_read_file("jenkins_pipeline", "vars/deployProdPlusOne.groovy", 1, 80)`
   (or `nonwebdeploy.groovy` for plain Deploy stage)
3. `read_runbook("health_check")` (this file)
4. `aws_describe_target_health(<tg_arn from service.lookup.rule_arn or log>)`
5. `aws_describe_target_group(<tg_arn>)` — get expected port + health_check_path
6. `aws_describe_instance(<instance_id from log>, <aws_account>, <aws_region>)`
   — confirm instance state, security groups, tags

If iter 0 results show drill-deeper need (e.g. canary TG had different
state than blue), iter 1 can fetch `aws_describe_listener_rule(<rule_arn>)`
to see traffic split. Most cases finish in 1-2 iters.

## Action template
```
Finding: Service '<svc>' on instance <instance_id> in account
         <aws_account> (<region>) failed to become healthy during
         <failed_stage>. ALB target health = unhealthy, reason =
         <reason from describe_target_health>. Target group expects
         port=<tg.port>, health_check_path=<tg.health_check_path>.

Action:
  Investigate service-side cause on the instance using BBCTL (org
  standard CLI). The RCA cannot determine WHY the service is
  unhealthy without instance access — operator must run these checks:

    bbctl shell <instance_id>           # interactive login

  Inside the shell, check (in order):
    sudo tail -n 200 <service.lookup.log_path>
        → look for stack trace, "Failed to start", "port already in use"
    sudo ss -tlnp | grep <service.lookup.target_port>
        → is service listening on the expected port?
    curl -i http://localhost:<port><health_check_path>
        → does the health endpoint return 2xx?
    systemctl status <service-name>
        → process state

  If service is up + endpoint returns 200, then ALB connectivity is
  the issue: check the instance's security group ingress from the
  ALB SG on the TG port.

Verify:
  Re-run pipeline AFTER fixing the service-side cause. The Deploy
  stage should pass past the health-check loop.
```

## Output schema notes
- `error_class: "health_check"`
- `failed_stage: "Deploy"` or "Deploy Prod+1"
- `evidence[]` must include:
  - `jenkins_log` showing health-check timeout
  - `jenkins_pipeline/vars/deployProdPlusOne.groovy:<line>` (or
    `nonwebdeploy.groovy`) — the helper that orchestrated the deploy
  - `jenkins_pipeline/scripts/non_web_healthy.sh:<line>` — poll loop
  - `aws:target_health(<tg_arn>)` — state + reason from describe
  - `aws:target_group(<tg_arn>)` — expected port + health_check_path
  - `aws:instance(<instance_id>)` — running state + tags

## suggested_commands

ALL commands are `tier: safe` (BBCTL UI interactive login is read-only
from the operator's perspective — they decide what to run inside).
Examples:
- `bbctl shell <instance_id>` (safe — interactive login)
- `aws elbv2 describe-target-health --target-group-arn <arn> --region <region>`
  (safe — describe only)
- `aws ec2 describe-instances --instance-ids <id> --region <region>`
  (safe — describe only)

## STRICT — DO NOT WRITE

- DO NOT emit `aws_run_ssm_command(...)` — tool is removed.
- DO NOT emit `ssh -i <pem>` — never use raw SSH.
- DO NOT default to port `8080` — use `target_port` from
  service.lookup or `aws_describe_target_group.port`.
- DO NOT default to `/var/log/blackbuck/gps.log` — that's the GPS
  service's log. Use `service.lookup.log_path` (e.g.
  `/var/log/blackbuck/test-supply-wrapper-nonweb.log` for service
  `test-supply-wrapper-nonweb`).
- DO NOT default to `/admin/version` — use `health_check_path` from
  `aws_describe_target_group` (e.g. `/actuator/health`).
- DO NOT fabricate "instance state: unhealthy" if `aws_describe_*`
  returned an error or empty. Set `needs_deeper: true` instead.
