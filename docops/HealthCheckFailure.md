# HealthCheckFailure — Stagger Deploy Health Check Triage

When the `Deploy` stage runs `healthy.sh <tg-arn> <region> <instance-id> <env>` and the ALB target group probe stays `unhealthy` for the full poll window, the pipeline aborts with:

```
Health Status for  after N iterations: unhealthy
...
Health Status failed to move to healthy within the time limit
Error in Deploy_i-<instance-id>: script returned exit code 1
Error in non-web deployment: script returned exit code 1
This error occurred in the nonwebdeploy.groovy script
```

**This is NOT** a Java runtime error, NewRelic error, or SSH error. The ALB simply never saw a 2xx from the new instance.

## Access pattern — use BBCTL

Org-standard CLI for EC2 access is `bbctl`. Do NOT raw-ssh.

```bash
bbctl shell <instance-id>                       # interactive login
bbctl run   <instance-id> -- 'sudo tail -n 500 /var/log/blackbuck/<svc>.log'
bbctl run   <instance-id> -- 'sudo ss -tlnp | grep <port>'
bbctl run   <instance-id> -- 'curl -i http://localhost:<port>/<health-path>'
```

`<instance-id>` comes from the failing `Error in Deploy_i-<id>` line or the `health_check.target` block in the RCA.

## Likely root causes (in priority order)

1. **Service didn't start on the instance.**
   - Crash in main(), missing config, port already in use, JVM OOM at startup.
   - **Verify:** `bbctl shell <instance-id>` then tail the service log at `log_path` from `config.json`. Look for a stack trace at the end.

2. **Port mismatch between service and target group.**
   - Service listens on port X, but TG `health_check_port` (or `port`) is set to Y.
   - **Verify:**
     ```bash
     bbctl run <instance-id> -- 'sudo ss -tlnp | grep -E "<service_port>|<health_check_port>"'
     ```
     Plus: `aws elbv2 describe-target-groups --target-group-arns <tg-arn> --query 'TargetGroups[0].[HealthCheckPort,Port]'`

3. **Health endpoint path returns non-2xx.**
   - Service is up, listening on the right port, but `/health` (or `health_check_path` from config.json) returns 5xx or 4xx.
   - **Verify:**
     ```bash
     bbctl run <instance-id> -- 'curl -i http://localhost:<health_check_port><health_check_path>'
     ```
     Expect `HTTP/1.1 200`.

4. **Security group blocks ALB → instance on the TG port.**
   - Instance SG doesn't allow inbound from the ALB SG on the TG port.
   - **Verify:** `aws ec2 describe-security-groups --group-ids <instance-sg>` — confirm an ingress rule from the ALB SG on the TG port.

5. **Slow boot vs health check threshold.**
   - Service eventually becomes healthy but takes longer than `healthy_threshold × interval` (typically 2 × 30s = 60s).
   - **Verify:** check service start time in log vs the deploy timestamp. If service became healthy AFTER the poll loop exited, this is the cause.
   - **Fix:** raise TG `HealthCheckIntervalSeconds`/`HealthyThresholdCount` for slow-starting services.

6. **Dependency unreachable (DB / Redis / Kafka / downstream API).**
   - Service starts, but the health endpoint does a deep check that requires an external dep, which is down or blocked.
   - **Verify:** service log will show a connect-timeout / refused on the dep. Check the dep's status separately.

## What to ignore (non-fatal upstream noise)

These commonly appear in the same log but are NOT the cause:
- `WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!` — pipeline has SSM fallback for instance login; SSH host-key mismatch never blocks deploy.
- `<error>Application X does not exist.</error>` from NewRelic — appName isn't registered yet. Observability gap, not a deployment failure.
- `Did you forget the def keyword? ... seems to be setting a field named pipelineSuccess` — Jenkins script-warning, not the failure cause.

## Decision-grade action template (for RCA output)

```
Finding: <service-name> deployment failed health check on <tg-name>
         (instance <instance-id>, region <region>). Target probe stayed
         unhealthy for <N> iterations.

Action  (RECOMMENDED, in order):
  1. SSH/SSM into <instance-id> and tail <log_path> for crash/exception.
     Most common: service exited at startup. Fix the runtime error, redeploy.
  2. Verify service listening port matches TG health_check_port:
       sudo ss -tlnp | grep <service_port>
  3. If service is running, hit health endpoint locally:
       curl -i http://localhost:<health_check_port><health_check_path>

Verify: Re-run pipeline. Watch for "Health Status: healthy" within
        ~60s of deploy start.
```

## Related

- `vars/nonwebdeploy.groovy` — wraps `healthy.sh` invocation
- `resources/healthy.sh` — the poll loop (50 iterations × interval)
- `resources/config.json` — service registry; per-service `log_path`, `service_port`, `health_check_path`, `health_check_port`
