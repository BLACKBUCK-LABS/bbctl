# Runbook: health_check

## What this class means
The ALB target group probe never returned healthy for the new instance
during deploy. Pipeline aborts in the `Deploy` stage after N failed
poll iterations. The service either crashed at startup, bound to the
wrong port, or the health-check path returns non-2xx.

## Detect signals
- `Health Status failed to move to healthy within the time limit`
- `ALB target unhealthy`
- `Iteration <N> of <M>: still draining` repeated
- Failed stage = "Deploy"
- `error_class` should be `health_check`

## Pipeline source to cross-check (MANDATORY)
- `jenkins_pipeline/vars/nonwebdeploy.groovy` (or `vars/deploy.groovy`)
- `jenkins_pipeline/resources/healthy.sh` (the poll loop)

## Drill plan
1. `get_jenkins_job_config(job)` → scriptPath
2. `repo_read_file("jenkins_pipeline", "vars/nonwebdeploy.groovy", 1, 80)` — read the deploy helper
3. `repo_read_file("jenkins_pipeline", "resources/healthy.sh", 1, 60)` — read the poll loop
4. From log, extract:
   - `target_group_arn` → use `service.lookup.rule_arn` to derive if not in log
   - `instance_id` from "instance=i-..." or `aws_describe_listener_rule` to find targets
5. `aws_describe_target_health(<tg_arn>)` — confirm ALB sees instance as unhealthy
6. `aws_describe_target_group(<tg_arn>)` — get expected port + health_check_path
7. `aws_describe_instance(<instance_id>)` — confirm instance is running
8. `aws_run_ssm_command(<instance_id>, "ss -tlnp | grep <port>")` — check listener
9. If empty listener: `aws_run_ssm_command(<instance_id>, "tail -n 200 <log_path>")` — read service log
10. If listener present but ALB unhealthy: `aws_run_ssm_command(<instance_id>, "curl -i http://localhost:<port><health_check_path>")` — probe the endpoint

## Action template
```
Finding: Service on <instance_id> in <region> failed to become healthy.
         ALB target health = unhealthy, reason = <reason from describe>.
         <Specific cause from SSM probes — e.g. "Service log shows
         'Address already in use: bind' on port <port>" OR
         "Health endpoint <path> returns 503" OR
         "Service process not running (systemctl status shows failed)">.
Action:  Use `bbctl shell <instance_id>` to inspect.
         If port held by stale process: kill PID, redeploy.
         If endpoint returns 5xx: check service config / dependencies.
         If process down: `bbctl run <instance_id> -- 'sudo journalctl -n 100 -u <svc>'` for crash log.
Verify:  `aws elbv2 describe-target-health --target-group-arn <tg_arn>`
         shows state=healthy.
```

## Output schema notes
- `error_class: "health_check"`
- `failed_stage: "Deploy"`
- `evidence[]` must include:
  - `jenkins_log` showing health-check timeout
  - `jenkins_pipeline/vars/nonwebdeploy.groovy:<line>` (deploy helper)
  - `jenkins_pipeline/resources/healthy.sh:<line>` (poll loop)
  - `aws:target_health(<tg_arn>)` state + reason
  - `aws:ssm(<instance>, '<cmd>')` outputs

## Common pitfalls
- DO NOT default to port 8080 — use `service.lookup.target_port` or
  `aws_describe_target_group`'s `health_check_port`.
- DO NOT default to `/admin/version` — use `aws_describe_target_group`'s
  `health_check_path`.
- DO NOT default to `/var/log/blackbuck/gps.log` — use
  `service.lookup.log_path` or `<svc>.log`.
- Use BBCTL commands, NOT `ssh -i <pem>`.
