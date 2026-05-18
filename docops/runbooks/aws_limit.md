# Runbook: aws_limit

## What this class means
AWS service-quota limit hit. Common quotas: EC2 vCPUs per region,
EBS volume count, Elastic IPs, ALB rules per listener, etc.

## Detect signals
- `VcpuLimitExceeded`
- `InstanceLimitExceeded`
- `Service quota exceeded`
- `RequestLimitExceeded`
- `LimitExceededException`
- `You have requested more vCPU capacity than your current limit`
- `Cannot exceed quota for ...`

## Pipeline source to cross-check (MANDATORY)
- The pipeline file that triggers the AWS API (usually
  `vars/createGreenInfra.groovy` running terraform, or
  `vars/nonwebdeploy.groovy` for direct ec2:RunInstances)

## Drill plan
1. `get_jenkins_job_config(job)` → scriptPath
2. `repo_read_file("jenkins_pipeline", "vars/createGreenInfra.groovy", ...)` — see where the AWS call happens
3. Identify which quota from the error message:
   - vCPU type (Running On-Demand <family> instances)
   - EBS volume count
   - Elastic IP count
   - ALB rules per listener
4. From service.lookup: `aws_account`, `aws_region`
5. (Optional, if quota is on listener rules): `aws_describe_listener_rule(<rule_arn>)` to see current rule count

## Action template
```
Finding: AWS quota '<quota name>' exceeded in account <aws_account>
         (<region>). Pipeline requested <amount> but limit is <limit>.
         Resource type: <EC2 family | ALB rule | EBS volume | etc.>.

Action:
  Step 1 (immediate workaround):
    Re-run the pipeline after waiting <N minutes> for in-flight resources
    to settle, OR retry in a different region if cross-region capable.

  Step 2 (correct fix — quota request):
    AWS Console (account <aws_account>) → Service Quotas → search
    '<quota name>' → Request quota increase → set value to <current * 1.5>.
    Approval typically 1-24 hours depending on the service.

  Step 3 (audit):
    Check if existing capacity is properly cleaned up. Some services
    leave zombie resources (orphaned ENIs, undeleted EBS volumes). Run
    `aws_describe_instance(...)` for recent instances in the same TG to
    see if cleanup is needed.
Verify:
  After quota increase: re-run pipeline; expect Infra/Deploy stage to
  pass the AWS API call.
```

## Output schema notes
- `error_class: "aws_limit"`
- `evidence[]` must include:
  - `jenkins_log` with the quota exceeded message
  - `jenkins_pipeline/<helper>:<line>` (the AWS API call site)
  - For quota-on-rules: `aws:listener_rule(<rule_arn>)` showing current state

## Common pitfalls
- DO NOT cite a specific quota number unless it's in the log.
- DO NOT suggest disabling the resource creation — fix the quota.
- DO NOT use BBCTL — this is an AWS console action.
