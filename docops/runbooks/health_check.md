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

## Pipeline source to cross-check (MANDATORY) — CHAIN-WALK

Follow the chain from log → main pipeline → outer helper → inner helper
→ resource script. Each file tells you where to look next; don't guess.

Concrete chain for a Prod+1 failure:

```
console log says: stage 'Prod+1' status=FAILED
                  Health Status failed to move to healthy ...
       ↓
get_jenkins_job_config(job) returns scriptPath, e.g.
  main_stagger_prod_plus_one.groovy
       ↓
repo_read_file(jenkins_pipeline, main_stagger_prod_plus_one.groovy)
  shows:
    stage('Prod+1') { steps { script {
      prodPlusOne(params.SERVICE)       ← outer helper
    }}}
       ↓
repo_read_file(jenkins_pipeline, vars/prodPlusOne.groovy)
  shows:
    deployProdPlusOne(service, env)     ← inner helper
       ↓
repo_read_file(jenkins_pipeline, vars/deployProdPlusOne.groovy)
  shows:
    def healthyScript = libraryResource 'scripts/healthy.sh'
                                         ↑ Jenkins shared-lib path
       ↓
repo_read_file(jenkins_pipeline, resources/scripts/healthy.sh)
  shows the actual poll loop that printed "Health Status failed..."
```

**Known correct paths (Jenkins shared-lib convention):**
- `vars/<helper>.groovy` — pipeline step implementation
- `libraryResource 'X'` → on disk = `resources/X`
- `resources/scripts/healthy.sh` — web service health poll
- `resources/scripts/non_web_healthy.sh` — non-web health poll
- `resources/canary.py` — canary measurement

DO NOT try `vars/healthy.sh` or `resources/healthy.sh` — those don't
exist. The script lives under `resources/scripts/`.

## Drill plan — execute ALL in parallel in iter 0

In a single LLM iteration, emit these tool calls in parallel:

1. `get_jenkins_job_config(job)` → confirm scriptPath
2. `repo_read_file("jenkins_pipeline", "vars/deployProdPlusOne.groovy", 1, 80)`
   (or `vars/nonwebdeploy.groovy` for plain Deploy stage)
3. `repo_read_file("jenkins_pipeline", "resources/scripts/non_web_healthy.sh", 1, 80)`
   (or `resources/scripts/healthy.sh` if service_type is web — distinguish
   via service.lookup.service_type)
4. `read_runbook("health_check")` (this file)
5. **MANDATORY** — `aws_describe(service='elbv2', operation='DescribeTargetGroups',
   params={'TargetGroupArns': [<tg_arn>]}, aws_account=..., aws_region=...)`
   → returns `Port` + `HealthCheckPath` + `HealthCheckProtocol`. You MUST use
   these exact values in suggested_commands (do NOT default to 8080 or
   `/admin/version`).
6. `aws_describe(service='elbv2', operation='DescribeTargetHealth',
   params={'TargetGroupArn': <tg_arn>}, ...)` → instance state + reason
7. `aws_describe(service='ec2', operation='DescribeInstances',
   params={'InstanceIds': [<instance_id>]}, ...)` → confirm state

If iter 0 results show drill-deeper need (e.g. canary TG had different
state than blue), iter 1 can fetch
`aws_describe(elbv2, DescribeRules, {'RuleArns': [<rule_arn>]})`
to see traffic split. Most cases finish in 1-2 iters.

## Action template
```
Finding: Service '<svc>' on instance <instance_id> in account
         <aws_account> (<region>) failed to become healthy during
         <failed_stage>. ALB target health = unhealthy, reason =
         <reason from describe_target_health>. Target group expects
         port=<tg.port>, health_check_path=<tg.health_check_path>.

Action:
  Investigate service-side cause on the instance using BBCTL. RCA
  cannot determine WHY the service is unhealthy without instance
  access — operator runs these checks. SUBSTITUTE the real values
  from aws_describe + service.lookup BEFORE emitting; do NOT leave
  <placeholder> strings.

    bbctl shell <REAL_INSTANCE_ID>      # from log_window verbatim

  Inside the shell, check (in order, USE REAL VALUES):
    sudo tail -n 200 <REAL_LOG_PATH from service.lookup.filebeat_log_path>
    sudo ss -tlnp | grep <REAL_PORT from aws_describe.Port>
    curl -i http://localhost:<REAL_PORT><REAL_HC_PATH from aws_describe.HealthCheckPath>
    systemctl status <REAL_SVC_NAME>

  If aws_describe(DescribeTargetGroups) returned Port=7005 and
  HealthCheckPath=/actuator/health, the suggested_commands MUST read:
    curl -i http://localhost:7005/actuator/health
  NOT:
    curl -i http://localhost:8080/admin/version

  If service is up + endpoint returns 200, ALB connectivity is the
  issue: check the instance's security group ingress from the
  ALB SG on the TG port.

Verify:
  Re-run pipeline AFTER fixing the service-side cause. The Deploy
  stage should pass past the health-check loop.
```

## STRICT — values discipline for health_check class

| Forbidden default (training-data bias)         | Real source                                 |
|------------------------------------------------|---------------------------------------------|
| port 8080                                      | aws_describe(elbv2, DescribeTargetGroups).Port |
| /admin/version                                 | aws_describe(elbv2, DescribeTargetGroups).HealthCheckPath |
| /var/log/blackbuck/gps.log                     | service.lookup.filebeat_log_path            |
| /var/lib/jenkins/.ssh/blackbuck_production.pem | service.lookup.pem_path_hint (use BBCTL anyway) |

If you wrote port 8080 in your draft JSON and the aws_describe
returned a different port, REVISE before emitting. Each value in
suggested_commands must trace to a tool result from THIS RCA.

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
