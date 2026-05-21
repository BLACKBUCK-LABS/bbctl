# Job flow: stagger-scaling

## Identity

- **Script path:** `jenkins_pipeline/scaling.groovy`
- **Likely Jenkins job names:** `Stagger Scaling`, `Stagger-Scaling`, `scale-out`
- **Shared library:** `staggered_plugins@feature/scale-job` (loaded INSIDE the `Load Library` stage)
- **Agent / options:** `agent any`; `AWS_REGION='ap-south-1'`; `DEPLOYMENT_TYPE='scale'`; `ansiColor('xterm')`

## Match

- `script_path` ends with `scaling.groovy`, OR
- `inline_script` contains stage bodies calling `pre_deployment(..., INSTANCE_COUNT)`
  (5-arg variant, the `INSTANCE_COUNT.toInteger()` last arg is unique to scale-out),
  `instance_provisioning(...)`, `artifact_deployment(...)`,
  `health_validation(...)`, with `hotfix_rollback()` in
  `post { failure { ... } }` AND NO `Cutover & Cleanup` stage.

## Parameters

| Param | Type | Default | Notes |
|---|---|---|---|
| `SERVICE` | active-choice single-select | — | ~90 services (web + non-web mixed) |
| `INSTANCE_COUNT` | choice [1..7] | — | Number of NEW instances to ADD (scale-out count) |
| `JFROG_BUILD` | reactive single-select | `'Select jar'` | existing jar from JFrog OR build new from COMMIT_ID |
| `COMMIT_ID` | validatingString | `''` | hex SHA 7-40 chars, optional; only when building new jar |
| `Jira-Ticket` | string | `''` | Status must be `READY FOR RELEASE` or `HOT FIX` |

## Stages

| # | Stage marker in console | Helper / inline |
|---|---|---|
| 1 | `(Load Library)` | `buildName "${params.SERVICE}_scale_x${params.INSTANCE_COUNT}"`; `library "staggered_plugins@feature/scale-job"` |
| 2 | `(Jira Details)` | `JiraDetails(params.SERVICE, params.COMMIT_ID, params['Jira-Ticket'])` |
| 3 | `(Input Validation)` | inline xor: `JFROG_BUILD=='Select jar' && COMMIT_ID==''` → error; both set → error |
| 4 | `(Validate Commit ID)` | inline: regex `[0-9a-fA-F]{7,40}` — **when** `params.COMMIT_ID?.trim()` |
| 5 | `(Build Artifact)` | `buildJob.call(params.SERVICE, params.COMMIT_ID)` — **when** `JFROG_BUILD=='Select jar' && COMMIT_ID?.trim()` |
| 6 | `(Pre-Deployment)` | `pre_deployment.call(SERVICE, JFROG_BUILD, COMMIT_ID, false, INSTANCE_COUNT.toInteger())` — **5-arg** variant for scale-out; sub-stages `1.1 Validate Parameters` + `1.2 Load Service Configuration` + `1.3 Validate Config Resources` + `1.4 Discover BLUE Target Group` |
| 7 | `(Instance Provisioning)` | `instance_provisioning.call(params.SERVICE)` |
| 8 | `(Artifact Deployment)` | `artifact_deployment.call(SERVICE, JFROG_BUILD=='Select jar' ? env.JFROG_BUILD : params.JFROG_BUILD)` |
| 9 | `(Health Validation)` | `health_validation.call(params.SERVICE)` |

**No `(Cutover & Cleanup)` stage.** Scale-out is additive — old instances stay running. New ones join the existing target group.

## Helper chain

```
pre_deployment (5-arg scale-out variant)
  ├─ libraryResource('config.json') + jq → env vars (same set as hotfix flow)
  ├─ INSTANCE_COUNT.toInteger() → scale-out mode; backup=false hardcoded
  └─ sub-stages "1.1 Validate Parameters", "1.2 Load Service Configuration",
     "1.3 Validate Config Resources", "1.4 Discover BLUE Target Group"
       ├─ 1.3 runs aws ec2 describe-images / describe-subnets /
       │   describe-security-groups / describe-key-pairs / iam
       │   get-instance-profile. NotFound on any → "Config resource
       │   validation failed".
       └─ 1.4 runs `aws elbv2 describe-rules --rule-arns <rule_arn>`
          to resolve the BLUE target group ARN from the listener-rule
          ARN in config.json. Sets env.BLUE_TG_ARN. This is a READ-ONLY
          AWS call — no infrastructure is provisioned at this stage.
          If the agent runs after a slave bounce mid-`sh`,
          `JsonSlurperClassic` retained in scope by pre_deployment
          may fail to serialize for Jenkins workflow checkpoint
          (build 15 case — pipeline aborts on
          `NotSerializableException`, not on any AWS issue).
instance_provisioning
  ├─ validates env vars
  ├─ fallback AMI from config.json if env.AMI_ID_BASE invalid →
  │   aws ec2 describe-images
  └─ aws ec2 run-instances (count = INSTANCE_COUNT) →
     register in BLUE_TG_ARN (additive — joins existing TG) →
     sets env.NEW_INSTANCE_IDS. env.OLD_INSTANCE_IDS is NOT set
     for scale-out (no replace semantics).
artifact_deployment
  ├─ jf rt dl Blackbuck/java/${SERVICE}/sha/${JFROG_BUILD} →
  │   /var/www/hotfix/${SERVICE}
  ├─ unzip → S3 upload (blackbuck-deployments bucket)
  └─ SSM execution on NEW_INSTANCE_IDS only
health_validation
  └─ aws elbv2 describe-target-health against BLUE_TG_ARN for the
     NEW_INSTANCE_IDS within timeout(10, MINUTES) waitUntil loop
hotfix_rollback (fires only on post.failure / post.aborted)
  ├─ deregister env.NEW_INSTANCE_IDS from env.BLUE_TG_ARN
  ├─ wait for drain (state=='unused', up to 30 attempts × 10s)
  └─ terminate NEW_INSTANCE_IDS — skips entirely if NEW_INSTANCE_IDS
     was never set (clean failure pre-provisioning, like build 15).
```

## Post

| Result | Action |
|---|---|
| `success` | `echo "Scale-out complete — ${INSTANCE_COUNT} new instances added to ${SERVICE}"` (no Jira update, no RCA webhook) |
| `failure` | `echo "Scale-out failed — rolling back new instances"` then `hotfix_rollback()` |
| `aborted` | `hotfix_rollback()` |

No `triggerRcaWebhook()` is wired into this pipeline's post block (unlike main / nonweb / hotfix / frontend). RCA for scale-out failures must be triggered externally OR the pipeline can be updated to add the call.

## Stage → likely failure modes

| Stage marker | Error class | Drill |
|---|---|---|
| `(Load Library)` | scm, dependency | feature/scale-job library branch not resolvable |
| `(Jira Details)` | compliance | use `docops/runbooks/compliance.md` Modes 1-5 (Mode 6 is `create-quick-infra`-only, NOT this pipeline) |
| `(Input Validation)` | (pipeline-level) | JFROG_BUILD vs COMMIT_ID xor violated |
| `(Validate Commit ID)` | (pipeline-level) | COMMIT_ID regex fail |
| `(Build Artifact)` | scm, dependency, java_runtime | git fetch / maven dep / JAR build error |
| `(Pre-Deployment)` / sub-stage `1.1 Validate Parameters` | (pipeline-level) | xor + mandatory-param errors |
| `(Pre-Deployment)` / sub-stage `1.2 Load Service Configuration` | parse_error, config_validation | jq parse fail; missing config.json keys |
| `(Pre-Deployment)` / sub-stage `1.3 Validate Config Resources` | **config_validation** (NOT health_check) | AMI / subnet / SG / key-pair / IAM-profile NotFound. Drill `vars/pre_deployment.groovy`. Fix = update `config.json` to a real resource ID, OR create the missing AWS resource. |
| `(Pre-Deployment)` / sub-stage `1.4 Discover BLUE Target Group` | **java_runtime** (CPS serialization), aws_describe (NotFound) | Build 15 case: slave bounce caused Jenkins to checkpoint pipeline state, `JsonSlurperClassic` retained in `pre_deployment` was not Serializable → `NotSerializableException`. NOT an AWS error. Drill `vars/pre_deployment.groovy` for the JsonSlurperClassic usage and refactor it to discard the parser after extracting primitive values. If the failure IS actually NotFound from `describe-rules`, that's a stale `rule_arn` in config.json. |
| `(Instance Provisioning)` | aws_limit, config_validation | `RunInstances` quota; AMI / subnet / SG NotFound. Note: scale-out adds N more instances to an ALB target group that may already be close to the per-ALB target-group-target limit. |
| `(Artifact Deployment)` | scm, ssm, network | JFrog 401 / 404, SSM SendCommand fail, S3 upload denial |
| `(Health Validation)` | health_check | New instances never healthy; service crash on startup; healthz 4xx/5xx |

## Before drilling — check recent commits

For any failure in this pipeline that traces back to code in
`jenkins_pipeline/` or `InfraComposer/`, call
`repo_recent_commits("jenkins_pipeline", 5)` BEFORE recommending
a fix. The library branch is `feature/scale-job` (not master) — be
careful that the version of `pre_deployment.groovy` /
`instance_provisioning.groovy` etc. you read IS the one this
pipeline loads. If the agent's local clone tracks master but the
pipeline loaded `feature/scale-job`, the read content may not match
what actually ran. Verify by reading the file from the loaded
revision (the console log shows the SHA, e.g.
`Checking out Revision 3de6e63...`).

See `docops/jenkins_pipelines_golden.md` §3 ("Universal rule") for
the full statement.

## Gotchas (operator-relevant)

- **Different library branch.** `feature/scale-job` is NOT
  `master`. The same helper names (`pre_deployment`,
  `instance_provisioning`, `artifact_deployment`,
  `health_validation`, `hotfix_rollback`) may resolve to slightly
  different implementations on that branch. Read the file from the
  revision the build actually checked out, not blindly from master.
- **Scale-out, not replace.** `instance_provisioning` adds N new
  EC2s to BLUE_TG_ARN; old instances are NOT touched. There is no
  `Cutover & Cleanup` stage, no `OLD_INSTANCE_IDS` env var, and no
  AMI backup. Don't write RCAs that talk about traffic shift or
  old-instance termination here.
- **`pre_deployment` is the 5-arg variant.** The 5th positional arg
  (`INSTANCE_COUNT.toInteger()`) is what switches it into scale-out
  mode. If you see argument-count errors at line 265, the loaded
  library branch may have the older 4-arg `pre_deployment`.
- **Build 15 failure was NOT an AWS issue.** The fatal line was
  `Caused: java.io.NotSerializableException: groovy.json.JsonSlurperClassic`.
  Slave-4 bounced mid-`sh`, Jenkins tried to checkpoint pipeline
  state, the retained `JsonSlurperClassic` instance was not
  Serializable, save failed, pipeline aborted. The earlier
  `aws elbv2 describe-rules` call in stage 1.4 succeeded; the
  NotFound / aws-describe path is unrelated. Don't be misled by the
  slave-4-bouncing chatter into recommending AWS profile / region /
  permission fixes — fix is on the pipeline-code side
  (refactor `pre_deployment` / library helper to avoid retaining
  `JsonSlurperClassic` in any closure or step-spanning variable).
- **No RCA webhook in post.** Failures here don't auto-call
  `triggerRcaWebhook()`. RCAs are operator-initiated via curl.
- **No on-call paging.** Scale-out is a planned op; no VictorOps
  page on failure (unlike `main_stagger_prod_plus_one`).
