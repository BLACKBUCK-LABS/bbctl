# Jenkins Pipelines — Golden Reference

Authoritative map of every BlackBuck Jenkins pipeline currently in
production. Use this doc to look up:

* what stages a job has, in what order
* which helper function each stage calls (`vars/<name>.groovy`)
* the helper chain (which helper calls which)
* what runs in `post { success / failure / aborted }`
* job-specific gotchas the agent must respect when writing RCAs

The pipeline source repo is `jenkins_pipeline` (cloned to
`bbctl/repos/jenkins_pipeline/`). Use `repo_read_file("jenkins_pipeline", ...)`
to read any helper not summarized here.

For a single failed build, the RCA agent should:

1. Match the job name to one of the sections below.
2. Find the stage in the "Stages" table — that gives you the helper.
3. Use the "Helper chain" line to drill deeper.
4. If the failure crosses helpers, follow the chain in the
   "Cross-pipeline reference" table at the end.

QA-Automation is **NOT** covered here — it is not in current production use.

---

## 1. Cross-pipeline reference (start here)

| Pipeline | Build | Prod+1 | Infra | Deploy | Rollout | Cleanup | Failure path |
|---|---|---|---|---|---|---|---|
| **hotfix-noncanary** | `buildJob` (conditional) | — | `instance_provisioning` | `artifact_deployment` + `health_validation` | — | `cutover_cleanup` | `hotfix_rollback` |
| **Jenkinsfile_create_quick_infra** | `QuickBuildJob` / `buildJobFrontend` | — | `CreateQuickInfra` | `QuickDeploy` / `QuickDeployFrontend` | — | — | `rollbackInstances` (with `input` prompt) |
| **main_stagger_prod_plus_one** | `buildJob` | `prodPlusOne` → `createRuleForProdPlusOne` → `deployProdPlusOne` → `triggerAutomation` → `destroyPP1Infra` | `createGreenInfra` | `deploy` | `rollout` → `canary` | `destroyBlueInfra` | `rollbackMain("Single Job Rollback")` + VictorOps page + Slack RCA |
| **stagger-nonweb** | `buildJob` | — | `createGreenInfra` | `deploy` → `nonWebDeploy` | `rollout` → `nonwebRollout` | `destroyBlueInfra` | `rollbackMain("non_web_rollback")` (no VictorOps) |
| **stagger-prod-plus-one-frontend** | `buildJob` (FE lib) | `prodPlusOneFrontend` | `createGreenInfra` (FE lib) | `deploy(..., COMMIT_ID)` (FE lib) | `rollout` (FE lib) | `destroyBlueInfra` (FE lib) | `frontendRollback(SERVICE, "prod", COMMIT_ID)` (no VictorOps) |
| **OnBoardingJenkinFile (Stagger Onboarding)** | bash: jq validation → config.json append → InfraComposer scaffolding → git push → python enrichment | — | — | — | — | — | `set -e` exit (no Jenkins post-block; manual rollback) |

**Shared library:**
- 5 main pipelines: `staggered_plugins@release/REQ-463-staggerprodplusupdate-v2` (or `@master` for nonweb and hotfix).
- **Frontend pipeline uses a different library:** `staggered_plugins_fe@stagger-fe-temp`. All shared-named helpers (`buildJob`, `deploy`, `createGreenInfra`, `rollout`, `destroyBlueInfra`) resolve to **different** implementations there. Do not assume frontend behavior matches the main library.

---

## 2. hotfix-noncanary

**Script path:** `jenkins_pipeline/hotfix-noncanary.groovy`
**Likely Jenkins job names:** `Hotfix-Deploy`, `hotfix-noncanary`, `hotfix`
**Shared library:** `staggered_plugins@master` (loaded INSIDE the `Load Library` stage, not via `@Library` annotation)
**Agent:** `agent any`; `AWS_REGION='ap-south-1'`; `ansiColor('xterm')`

### Parameters
| Param | Type | Default | Notes |
|---|---|---|---|
| `SERVICE` | active-choice single-select | — | hardcoded ~100 services |
| `JFROG_BUILD` | reactive single-select | `'Select jar'` | runs `jf rt s 'Blackbuck/java/${SERVICE}/sha/staggered/*/*.zip'` to populate latest 10 |
| `backup` | boolean | false | "Create AMI Backup of healthy OLD instance?" |
| `COMMIT_ID` | string | `''` | only used when building a new jar |
| `Jira-Ticket` | string | `''` | HOTFIX ticket id |

### Stages
| # | Stage marker in console | Helper / inline |
|---|---|---|
| 1 | `(Load Library)` | `buildName "${params.SERVICE}"`; `library "staggered_plugins@master"` |
| 2 | `(Jira Details)` | `JiraDetails(params.SERVICE, params.COMMIT_ID, params['Jira-Ticket'])` |
| 3 | `(Input Validation)` | inline: `JFROG_BUILD=='Select jar' && COMMIT_ID==''` → error; both set → error (xor) |
| 4 | `(Build Artifact)` | `buildJob.call(params.SERVICE, params.COMMIT_ID)` — **only when** `JFROG_BUILD=='Select jar' && COMMIT_ID?.trim()` |
| 5 | `(Pre-Deployment)` | `pre_deployment.call(SERVICE, JFROG_BUILD, COMMIT_ID, backup)` — has sub-stages `1.1 Validate Parameters` + `1.2 Load Service Configuration` + `1.3 Validate Config Resources` |
| 6 | `(Instance Provisioning)` | `instance_provisioning.call(params.SERVICE)` |
| 7 | `(Artifact Deployment)` | `artifact_deployment.call(SERVICE, JFROG_BUILD=='Select jar' ? env.JFROG_BUILD : params.JFROG_BUILD)` |
| 8 | `(Health Validation)` | `health_validation.call(SERVICE)` |
| 9 | `(Cutover & Cleanup)` | `cutover_cleanup.call(SERVICE, backup)` |

### Helper chain
```
pre_deployment
  ├─ libraryResource('config.json') + jq → env vars (SERVICE_NAME, AWS_REGION, CLUSTER, RULE_ARN, INSTANCE_TYPE, AMI_ID_BASE, SECURITY_GROUP_IDS, etc.)
  └─ has sub-stages "1.1 Validate Parameters", "1.2 Load Service Configuration", "1.3 Validate Config Resources"
       └─ "1.3 Validate Config Resources" runs `aws ec2 describe-images`, `aws ec2 describe-subnets`, `aws ec2 describe-security-groups`, `aws ec2 describe-key-pairs`, `aws iam get-instance-profile`. NotFound on any → "ERROR: Config resource validation failed" + lists the missing resource(s). This is NOT a health_check class failure; the message looks like one but the stage is config-validation.
instance_provisioning
  ├─ validates env vars
  ├─ fallback AMI from config.json if env.AMI_ID_BASE invalid → `aws ec2 describe-images`
  └─ `aws ec2 run-instances` → registers in BLUE_TG_ARN → sets env.NEW_INSTANCE_IDS, env.OLD_INSTANCE_IDS
artifact_deployment
  ├─ `jf rt dl Blackbuck/java/${SERVICE}/sha/${JFROG_BUILD}` to /var/www/hotfix/${SERVICE}
  ├─ unzip → S3 upload (blackbuck-deployments bucket)
  └─ SSM execution on NEW_INSTANCE_IDS
health_validation
  └─ `aws elbv2 describe-target-health` against BLUE_TG_ARN within `timeout(10, MINUTES)` `waitUntil` loop
cutover_cleanup
  ├─ deregister OLD_INSTANCE_IDS from BLUE_TG (pre-checks they're registered; skips `unused`/`None`/`null`)
  ├─ optional AMI backup of healthy OLD instance (gated by `backup` param)
  └─ terminate OLD instances
hotfix_rollback
  ├─ deregister env.NEW_INSTANCE_IDS from env.BLUE_TG_ARN
  ├─ wait for drain (state=='unused', up to 30 attempts × 10s)
  └─ terminate NEW_INSTANCE_IDS — skips entirely if NEW_INSTANCE_IDS was never set
```

### Post
| Result | Action |
|---|---|
| `always` | if `currentResult=='NOT_BUILT'` → `triggerRcaWebhook()` (BB-AI RCA) |
| `success` | `UpdateJiraStatus(params['Jira-Ticket'])` |
| `unstable` | `triggerRcaWebhook()` |
| `failure` | `hotfix_rollback()` then `triggerRcaWebhook()` |
| `aborted` | `hotfix_rollback()` |

### Gotchas
- Single-target-group flow (BLUE only — no green / no canary).
- `Build Artifact` stage is SKIPPED when a pre-built jar is selected — most hotfix runs.
- Stage `1.3 Validate Config Resources` failures must NOT be classified as `health_check`. The "Health Validation" stage is later (stage 8).
- `triggerRcaWebhook()` is always wrapped in try/catch — RCA failures don't fail the pipeline.

---

## 3. Jenkinsfile_create_quick_infra

**Script path:** `jenkins_pipeline/Jenkinsfile_create_quick_infra`
**Likely Jenkins job names:** `create-quick-infra`, `quick-infra`, `create-quick-infra-onboarding`
**Shared library:** `staggered_plugins@${libraryBranch}` — dynamic but currently hardcoded to `release/REQ-463-staggerprodplusupdate-v2`
**Agent:** `agent any`; `tools { maven 'Maven' }`; `ansiColor('xterm')`

### Parameters (reactive Active Choices)
| Param | Type | Notes |
|---|---|---|
| `IS_ONBOARDED` | choice [`No`, `Yes`] | gates show/hide of auto-resolvable params |
| `Jira-Ticket` | string | mandatory; pipeline errors if empty |
| `SERVICE` | DynamicReferenceParameter | dropdown of ~135 services if onboarded=Yes; free-text input if No |
| `INSTANCE_COUNT` | choice [1..7] | frontends must be 1 |
| `COMMIT_ID` | ValidatingStringParameterDefinition | regex `^([0-9a-fA-F]{7,40})?$` |
| `JFROG_BUILD` | reactive | `jf rt s` jar listing; hidden when IS_ONBOARDED=No |
| `service_type` | hiddenChoice | `Java` / `Docker` |
| `jar_path`, `project_name`, `IAM_ROLE`, `SUBNET_IDS`, `SECURITY_GROUP_IDS`, `HEALTH_CHECK_URL` | hiddenString | hidden when IS_ONBOARDED=Yes |
| `APP_PORT`, `dockerfile_path` | visibleForFrontendOnly | Docker/frontend + IS_ONBOARDED=No |
| `git_repo`, `AWS_REGION`, `AMI_ID`, `INSTANCE_CLASS`, `slack_channel`, `business`, `team_name` | hiddenString | |
| `ACCOUNT` | hiddenChoice | `zinka` / `divum` / `finserv` / `tzf` |
| `SERVER_CMD`, `java_version`, `build_command`, `LOG_FILE_PATTERN`, `NEWRELIC_FILE_PATH`, `NEWRELIC_JAR` | hideForFrontend | hidden for Docker/frontend + IS_ONBOARDED=Yes |

### Script-scope state
- `effectiveParams` — mutable `Map` copy of `params` (because Jenkins `params` is immutable)
- `instanceIds` — list of EC2 IDs returned by `CreateQuickInfra`
- `ALLOW_AWS_INFRA_DISCOVERY = false` — gate; currently only `health_check_url` is auto-discovered
- Helper `rollbackInstances(ids, region, account)` — terminates EC2s
- Helper `keyNameForAccount(account)` — `finserv → prod-finserv-key`, `tzf → production_tzf`, default → `blackbuck_production`

### Stages
| # | Stage marker | Helper / inline |
|---|---|---|
| 1 | `(Load Library)` | `buildName "${SERVICE}_${COMMIT_ID}"`; loads dynamic-branch library |
| 2 | `(Jira Details)` | `JiraDetails(params.SERVICE, params.COMMIT_ID, params['Jira-Ticket'])` — errors if Jira-Ticket empty |
| 3 | `(Resolve Parameters)` | large inline script: if `IS_ONBOARDED==Yes` parses libraryResource `config.json` and populates `effectiveParams`; falls back to `discoverInfraFromRuleArn` for `health_check_url` only. If `IS_ONBOARDED==No` validates broader mandatory list, derives `KEY_NAME` from ACCOUNT, sets `DISK_SIZE='50'`, derives `multi_project` from `jar_path` + `project_name`. Frontend/Docker → enforces `INSTANCE_COUNT==1`. |
| 4 | `(Input Validation)` | inline xor: `JFROG_BUILD` vs `COMMIT_ID` — **when** `service_type in ['web','non-web','non-web-cron','non-web-consumer','Java']` |
| 5 | `(Build)` | `QuickBuildJob(SERVICE, COMMIT_ID, effectiveParams)` — **when** Java service types + `JFROG_BUILD=='Select jar'` + `COMMIT_ID` set |
| 6 | `(Build Frontend)` | `buildJobFrontend(SERVICE, COMMIT_ID, effectiveParams)` — **when** `service_type in ['Docker','frontend']` |
| 7 | `(Infra)` | `instanceIds = CreateQuickInfra(SERVICE, effectiveParams)` |
| 8 | `(Deploy)` | `QuickDeploy(SERVICE, "prod", effectiveParams + [INSTANCE_IDS: instanceIds])` — **when** Java service types |
| 9 | `(Deploy Frontend)` | `QuickDeployFrontend(...)` — **when** Docker/frontend |

### Helper chain
```
QuickBuildJob(service, COMMIT_ID, params)
  ├─ if IS_ONBOARDED=='Yes' → loads config.json libraryResource
  ├─ precheck.executePrechecks('Build')                          ← in onboarded path only
  ├─ JAVA_HOME by params.java_version + params.ACCOUNT
  │     (zinka/divum → amazon-corretto java21; others → openjdk)
  └─ Notification.build
buildJobFrontend(service, COMMIT_ID, params)
  └─ frontend artifact build (Docker / nodejs)
CreateQuickInfra(service, params)
  ├─ aws ec2 run-instances (count = INSTANCE_COUNT)
  └─ returns instance ID list
QuickDeploy(service, "prod", params)
  ├─ resolves JAR (params.JFROG_BUILD with 'staggered/' prefix stripped, else env[service+":jar_identifier"])
  ├─ S3 bucket by params.ACCOUNT (finserv→finserv-deployment, tzf→tzf-deployments, default→blackbuck-deployments)
  ├─ prepareLocalFiles() — writes deploy scripts
  └─ parallel SSM deploy to each instance in params.INSTANCE_IDS
QuickDeployFrontend(service, "prod", params)
  └─ frontend deploy variant
```

### Post
| Result | Action |
|---|---|
| `always` | `NOT_BUILT` → `triggerRcaWebhook()`; then `deleteDir()` |
| `success` | `UpdateJiraStatus(params['Jira-Ticket'])` |
| `unstable` | `triggerRcaWebhook()` |
| `failure` | `triggerRcaWebhook()` → Slack RCA via `Notification.rcaAlert` → interactive `input message: 'Pipeline failed. Destroy provisioned infra?'` → `rollbackInstances(instanceIds, ...)`. **NO VictorOps page.** |
| `aborted` | `input` prompt → `rollbackInstances(...)` |

### Gotchas
- This pipeline is the **bootstrap** job — used to spin up infra for a NEW service that does NOT yet exist in `config.json`.
- The compliance gate in `vars/JiraDetails.groovy` was patched in May 2026 to source `SERVICE` from git build params for this job; `config.json` is enrichment only. A `Compliance: SERVICE '<svc>' not found in config.json` failure on this job is a gate-logic regression, NOT a missing-entry bug. See `docops/runbooks/compliance.md` Mode 6.
- No Prod+1, no canary, no Rollout stage. Single-shot provisioning + deploy.
- Frontend/Docker services bypass `QuickBuildJob` + `QuickDeploy`; use `buildJobFrontend` + `QuickDeployFrontend`.
- `discoverInfraFromRuleArn` is gated behind `ALLOW_AWS_INFRA_DISCOVERY=false`; today only `health_check_url` is auto-discovered.
- Rollback path has interactive `input` prompt before destroy — pipeline can hang waiting for operator approval.

---

## 4. main_stagger_prod_plus_one

**Script path:** `jenkins_pipeline/main_stagger_prod_plus_one.groovy`
**Likely Jenkins job names:** `Stagger-Prod-Plus-One`, `stagger-deploy`, `main-prod-deploy`
**Shared library:** `staggered_plugins@release/REQ-463-staggerprodplusupdate-v2`
**Agent:** `agent any`; `tools { maven 'Maven' }`; `ansiColor('xterm')`
**Environment:** `SUBMITTER_EMAILS = "thejasvi.bhat@..., rahul.aggarwal@..., vivekanand.matta@..."`

### Parameters
| Param | Type | Default |
|---|---|---|
| `COMMIT_ID` | string | `commit_id` |
| `SERVICE` | choice | hardcoded ~60 web services |
| `Jira-Ticket` | string | `''` |

### Stages
| # | Stage marker | Helper |
|---|---|---|
| 1 | `(Load Library)` | sets buildName + library |
| 2 | `(Jira Details)` | `JiraDetails(SERVICE, COMMIT_ID, Jira-Ticket)` |
| 3 | `(Build)` | `buildJob(SERVICE, COMMIT_ID)` |
| 4 | `(Prod+1)` | `prodPlusOne(SERVICE)` — on success sets `env.PROD_PLUS_ONE_COMPLETED = "true"` (this gates VictorOps paging in post.failure) |
| 5 | `(Infra)` | `createGreenInfra(SERVICE)` |
| 6 | `(Deploy)` | `deploy(SERVICE, "prod")` (web or non-web routing inside) |
| 7 | `(Rollout)` | `rollout(SERVICE)` (canary traffic shift) |
| 8 | `(Destroy)` | `destroyBlueInfra(SERVICE)` (post-cutover terminate old blue) |

### Helper chain
```
buildJob(SERVICE, COMMIT_ID)
  ├─ reads config.json (libraryResource)
  ├─ env[service+':branch_deploy'], env[service+':slack_channel']
  ├─ precheck.executePrechecks(SERVICE, ['Build'])           ← checkGitRepo
  ├─ JAVA_HOME by config.java_version (no account differentiation)
  └─ Notification.build → build JAR from git
prodPlusOne(SERVICE)
  ├─ precheck['prodPlusOne']                                 ← checkTerraformStateFile
  ├─ stage("Infra Prod+1") { createRuleForProdPlusOne(service, 150) }
  │     └─ validates rule_arn, lb_listener_arn, ami, aws_region, aws_account; INSTANCE_COUNT=1 hardcoded; calls createInfra(data, SERVICE, env['BUILD_ID'], priority=150)
  ├─ stage("Deploy Prod+1") { deployProdPlusOne(service, "preprod") } (catch → approvalProd1Failure())
  ├─ stage("Automation") { triggerAutomation(service); Notification.qaSanityApproval; approval() }
  └─ stage("Destroy Prod+1") { destroyPP1Infra(service) }
createGreenInfra(SERVICE)
  ├─ precheck['Infra']                                       ← validateAmiExists + getInstanceCountInGreenTargetGroup
  ├─ validates rule_arn, lb_listener_arn, ami, aws_region, aws_account
  ├─ defaults: DISK_SIZE=50, INSTANCE_CLASS=t3a.small
  └─ terraform creates green TG + N EC2s via createInfra(data, SERVICE, env['BUILD_ID'])
deploy(SERVICE, "prod")
  ├─ reads service_type from config
  ├─ if non-web → nonWebDeploy(SERVICE, "prod", serviceType)
  └─ else SSM-based parallel deploy with libraryResource scripts:
       app_restart, deploy_code, download_jar, healthy.sh, post_deploy_cleanup,
       prepare_jenkins, prepare_server, unhealthy.sh; templates filebeat.yml,
       supervisor.conf; optional fluent-bit.conf
rollout(SERVICE)
  ├─ precheck['Rollout']                                     ← checkroutingeligibility
  ├─ if non-web → nonwebRollout(service)
  └─ else web canary loop:
       reads env[service+":blue_target_arn"] and env[service+":green_target_arn"]
       extracts hostnames via SSM (with SSH fallback) per TG
       canary(app_name, blue_tg_arn, region, account) once per traffic_value from config
canary(app_name, blue_tg_arn, region, account)
  ├─ SSM-with-SSH-fallback → extract blue instance hostnames
  ├─ writes canary.py from libraryResource
  └─ python3 ./canary.py --percent_rollout 10 → 0 = pass, non-zero = rollback trigger
destroyBlueInfra(SERVICE)
  └─ terraform destroy on blue TG + old EC2s
```

### Post
| Result | Action |
|---|---|
| `always` | `NOT_BUILT` → `triggerRcaWebhook()`; `deleteDir()` |
| `success` | `UpdateJiraStatus(params['Jira-Ticket'])` |
| `unstable` | `triggerRcaWebhook()` |
| `failure` | (a) `rollbackMain("Single Job Rollback", params.SERVICE)`; (b) `triggerRcaWebhook()` capturing parsed RCA object; (c) post RCA to per-service Slack via `Notification.rcaAlert`; (d) IF `env.PROD_PLUS_ONE_COMPLETED=="true"` AND failure is NOT a canary failure → POST to VictorOps at `http://fms-vyakhya.alb.jinka.in/vyakhya/api/incident/create` with `{service_name:"devops", message_type:"CRITICAL", incidentKey:"jenkins_${SERVICE}_${BUILD_NUMBER}"}`. Canary-induced rollbacks (log contains `"Rollout back as Canary failed"` or `"Rolling Back as Result !=0"`) are EXCLUDED from VictorOps paging. |
| `aborted` | `rollbackMain("Single Job Rollback", params.SERVICE)` |

### Gotchas
- `env.PROD_PLUS_ONE_COMPLETED` gates VictorOps — pre-Prod+1 failures don't page on-call.
- Canary-induced rollbacks page neither VictorOps nor Slack-RCA-Phase-C (detected via log substring match).
- VictorOps payload uses fixed `service_name:"devops"` per the API contract; `entityId = BUILD_NUMBER`.
- `triggerRcaWebhook.buildAlertMessage(rca)` is a nested call on the var — used to format the RCA summary line for the Slack post.

---

## 5. stagger-nonweb

**Script path:** `jenkins_pipeline/stagger-nonweb.groovy`
**Likely Jenkins job names:** `Stagger-NonWeb`, `stagger-prod-nonweb`, `nonweb-deploy`
**Shared library:** `staggered_plugins@master`
**Agent:** `agent any`; `tools { maven 'Maven' }`; `ansiColor('xterm')`
**Environment:** `SUBMITTER_EMAILS = "thejasvi.bhat@..., rahul.aggarwal@..., vivekanand.matta@..."`

### Parameters
| Param | Type | Default |
|---|---|---|
| `COMMIT_ID` | string | `commit_id` |
| `SERVICE` | choice | hardcoded ~35 nonweb services |
| `Jira-Ticket` | string | `''` |

### Stages
| # | Stage marker | Helper |
|---|---|---|
| 1 | `(Load Library)` | buildName + library |
| 2 | `(Jira Details)` | `JiraDetails(SERVICE, COMMIT_ID, Jira-Ticket)` |
| 3 | `(Build)` | `buildJob(SERVICE, COMMIT_ID)` |
| 4 | `(Infra)` | `createGreenInfra(SERVICE)` |
| 5 | `(Deploy)` | `deploy(SERVICE, "prod")` — routes to `nonWebDeploy` |
| 6 | `(Rollout)` | `rollout(SERVICE)` — routes to `nonwebRollout` |
| 7 | `(Destroy)` | `destroyBlueInfra(SERVICE)` |

### Helper chain
```
Same buildJob → createGreenInfra → deploy → rollout → destroyBlueInfra as
main_stagger_prod_plus_one, but:
  deploy → nonWebDeploy(SERVICE, "prod", serviceType)
              ├─ same script bundle as web + non_web_healthy.sh
              ├─ optional fluent-bit.conf
              └─ parallel SSM deploy
  rollout → nonwebRollout(service)
              ├─ reads canary_timing from config
              ├─ non-web-cron: clamps each value to [0..60]; default [5]
              ├─ non-web-consumer: default [0]
              └─ time-based delays; no NewRelic canary call
```

### Post
| Result | Action |
|---|---|
| `always` | `NOT_BUILT` → `triggerRcaWebhook()`; `deleteDir()` |
| `success` | `UpdateJiraStatus(params['Jira-Ticket'])` |
| `unstable` | `triggerRcaWebhook()` |
| `failure` | `rollbackMain("non_web_rollback", params.SERVICE)` — **NO VictorOps, NO Slack RCA, NO BB-AI failure call.** |
| `aborted` | `rollbackMain("non_web_rollback", params.SERVICE)` |

### Gotchas
- **NO Prod+1 stage** (nonweb services don't take HTTP traffic, so no preprod validation).
- Failure path is intentionally thin — no on-call paging.
- `nonwebRollout` uses time-based stagger, not canary score. Don't write RCAs that assume Kayenta/NewRelic canary semantics here.

---

## 6. stagger-prod-plus-one-frontend

**Script path:** `jenkins_pipeline/stagger-prod-plus-one-frontend.groovy`
**Likely Jenkins job names:** `Stagger-Prod-Plus-One-Frontend`, `frontend-deploy`, `stagger-fe`
**Shared library:** **`staggered_plugins_fe@stagger-fe-temp`** ← DIFFERENT library
**Agent:** `agent any`; `tools { maven 'Maven' }`; `ansiColor('xterm')`
**Environment:** `SUBMITTER_EMAILS = "thejasvi.bhat@..., rahul.aggarwal@..., vivekanand.matta@..."`

### Parameters
| Param | Type | Default |
|---|---|---|
| `COMMIT_ID` | string | `commit_id` |
| `SERVICE` | choice | 7 frontends: `gps-shipper-frontend`, `gps-share-frontend`, `boss-frontend`, `trip-frontend`, `brokerage-bo-fe`, `access-portal`, `bb-transformer` |
| `Jira-Ticket` | string | `''` |

### Stages
| # | Stage marker | Helper / inline |
|---|---|---|
| 1 | `(Load Library)` | buildName + lib; declarative `steps` block writes libraryResource `config.json` to `${WORKSPACE}/${BUILD_ID}.json` and `aws_account.json` to workspace |
| 2 | `(Jira Details)` | `JiraDetails(SERVICE, COMMIT_ID, Jira-Ticket)` |
| 3 | `(Build)` | `buildJob(SERVICE, COMMIT_ID)` — frontend-lib variant |
| 4 | `(Prod+1)` | `prodPlusOneFrontend(SERVICE, COMMIT_ID)` ← DIFFERENT from `prodPlusOne` |
| 5 | `(Infra)` | `createGreenInfra(SERVICE)` — frontend-lib variant |
| 6 | `(Deploy)` | `deploy(SERVICE, "prod", COMMIT_ID)` — **3-arg** signature (frontend lib) |
| 7 | `(Rollout)` | `rollout(SERVICE)` — frontend-lib variant |
| 8 | `(Destroy)` | `destroyBlueInfra(SERVICE)` — frontend-lib variant |

### Post
| Result | Action |
|---|---|
| `always` | `NOT_BUILT` → `triggerRcaWebhook()`; `deleteDir()` |
| `success` | `UpdateJiraStatus(params['Jira-Ticket'])`; `sh "rm -rf ${WORKSPACE}/${BUILD_ID}.json"` |
| `unstable` | `triggerRcaWebhook()` |
| `failure` | `frontendRollback(params.SERVICE, "prod", params.COMMIT_ID)` — **NO VictorOps, NO Slack RCA, NO BB-AI failure call.** |
| `aborted` | `frontendRollback(SERVICE, "prod", COMMIT_ID)` |

### Gotchas
- All helpers (`buildJob`, `deploy`, `createGreenInfra`, `rollout`, `destroyBlueInfra`) resolve to DIFFERENT implementations from `staggered_plugins_fe@stagger-fe-temp`. **Do not** assume the frontend `deploy` matches the main `deploy` — the signature is 3-arg here, 2-arg in main.
- Library is loaded in declarative `steps`, NOT inside a `script {}` block — unusual mix. Most other pipelines load inside `script {}`.
- Per-build config snapshot is cleaned up only on success — failure leaves `${BUILD_ID}.json` in workspace for debugging.

---

## 7. OnBoardingJenkinFile (Stagger Onboarding)

**Script path:** `jenkins_pipeline/config/OnBoardingJenkinFile`
**Likely Jenkins job names:** `Stagger-Onboarding`, `service-onboarding`, `onboarding-job`
**This is pure bash**, not a Jenkinsfile. Runs `set -e`; any non-zero command aborts.

### Inputs (env vars supplied by the Jenkins job's parameter form)
- `service_name`, `traffic_value`, `git_repo_name`
- `aws_account_name`, `aws_region_name`
- `lb_listener_arn`, `rule_arn`
- `ami_id`, `instance_class_name`, `instance_no`, `disk_sizes`
- `server_commands`, `build_commands`, `service_type`
- `new_relic_app_name`, `new_relic_file_path`, `new_relic_jar_path`
- `slack_channel`, `qa_automation`, `qa_automation_name`
- `team_name`, `business`, `java_version`, `filebeat_log_path`, `filebeat_required_for_service`
- `canary_timing` (only for `non-web-cron`)
- `git` (GitHub PAT)

### Sequential bash sections (act as "stages")
1. **Pull config repo** — `cd /var/lib/jenkins/ramesh/jenkins_pipeline/`; `git checkout master`; `git pull`
2. **Duplicate check** — `jq 'has($service_name)' config.json` → exit 1 if onboarded
3. **Traffic value ascending check**
4. **GitHub repo existence** — `curl api.github.com/repos/BLACKBUCK-LABS/$git_repo_name` with `Authorization: token $git`
5. **ALB listener_arn validation** — `aws elbv2 describe-listeners`
6. **ALB rule_arn validation** — `aws elbv2 describe-rules`
7. **Instance count positive integer** — regex `^[1-9][0-9]*$`
8. **Disk size ≥ 10 GB**
9. **service_identifier regex** — forced format `^*preprod.*\*$` (line 6)
10. **Slack channel non-empty**, **new_relic_file_path / new_relic_jar_path non-empty**
11. **filebeat_log_path format** — when `filebeat_required_for_service==true`, must match `^/var/log/[^*]+\.log$` (no `*`)
12. **Config-compass registration** — `curl -X POST http://configcompass.alb.jinka.in/config-compass/config/service-header-mapping` (must return HTTP 200)
13. **Append to config.json** — three jq branches based on `qa_automation` (yes/no) and `service_type` (`non-web-cron` adds `canary_timing` field)
14. **InfraComposer terraform scaffolding** — `cd /var/lib/jenkins/ramesh/InfraComposer`; copies template dir to `/tmp/$service_name`; `sed` replaces `{{service_name}}`, `{{aws_region_name}}`, `{{aws_account_name}}` in `prod/`, `prodplusone/`, `prod-scale/` (main.tf + variable.tf each); commits to InfraComposer `main`
15. **Push config.json change** — `git add . && git commit && git push origin master`
16. **Enrich config.json** — `python3 ./resources/update_config.py "$service_name" "$aws_account_name" "$aws_region_name"` (auto-discovers infra metadata from healthy instance)
17. **Push enrichment** — second `git push`

### Post — N/A
No Jenkins `post {}`. Any `set -e` failure aborts; rollback is manual (revert the two git pushes).

### Gotchas
- Two separate repos checked out into Jenkins controller filesystem at `/var/lib/jenkins/ramesh/` — fragile, won't work on other controllers.
- ConfigCompass HTTP-200 check is hard — service header mapping must register before config.json gets the new entry.
- The new-relic validation block is commented out (lines 126-135 of the script). Don't rely on those checks.
- Final python enrichment step appends additional fields (subnet, security groups, key_name, instance_profile) by querying AWS for a healthy reference instance.

---

## 8. Helper summaries (signature + purpose + callers)

| Helper | Signature | Purpose (1-line) | Called by |
|---|---|---|---|
| `JiraDetails` | `(String service, String commitId, String jiraTicket, String expectedStatus = "READY FOR RELEASE")` | Compliance gate — team-board-mapping, Jira status, signed-off SHA, clone detection. Falls back to `JiraDetails_oldflow(jiraTicket)` if service not onboarded. | all 6 pipelines |
| `buildJob` | `(String service, String COMMIT_ID)` | Reads config.json, runs `precheck['Build']`, builds JAR from git | main_stagger_prod_plus_one, stagger-nonweb, frontend, hotfix-noncanary |
| `QuickBuildJob` | `(String service, String COMMIT_ID, Map params = [:])` | Conditional precheck (only if IS_ONBOARDED=Yes), sets JAVA_HOME by `java_version + ACCOUNT` | Jenkinsfile_create_quick_infra (Build) |
| `buildJobFrontend` | `(String service, String COMMIT_ID, Map params)` | Frontend artifact build (Docker / nodejs) | Jenkinsfile_create_quick_infra (Build Frontend) |
| `CreateQuickInfra` | `(String service, Map params)` | Runs N EC2s; returns instance-id list | Jenkinsfile_create_quick_infra (Infra) |
| `QuickDeploy` | `(def SERVICE, def ENVIRONMENT, params)` | Reads `params.INSTANCE_IDS`, resolves JAR, parallel SSM deploy | Jenkinsfile_create_quick_infra (Deploy) |
| `QuickDeployFrontend` | similar to QuickDeploy | Frontend variant | Jenkinsfile_create_quick_infra (Deploy Frontend) |
| `prodPlusOne` | `(def service)` | Orchestrates 4 sub-stages: Infra Prod+1, Deploy Prod+1, Automation, Destroy Prod+1 | main_stagger_prod_plus_one (Prod+1) |
| `prodPlusOneFrontend` | `(SERVICE, COMMIT_ID)` | Frontend Prod+1 (from `staggered_plugins_fe`) | stagger-prod-plus-one-frontend (Prod+1) |
| `createGreenInfra` | `(String SERVICE)` | `precheck['Infra']`, validates 5 config keys, terraform-creates green TG + N EC2s | main, nonweb, frontend (Infra) |
| `createRuleForProdPlusOne` | `(String SERVICE, Number priority)` | Builds prod+1 ALB rule with priority (150 from prodPlusOne); INSTANCE_COUNT=1 hardcoded | prodPlusOne (Infra Prod+1) |
| `deployProdPlusOne` | `(service, "preprod")` | Deploy to prod+1 instance; on catch → `approvalProd1Failure()` | prodPlusOne (Deploy Prod+1) |
| `destroyPP1Infra` | `(SERVICE)` | terraform destroy targeted to prod+1 modules | prodPlusOne (Destroy Prod+1) |
| `deploy` | main lib: `(def SERVICE, def ENVIRONMENT)` ; FE lib: `(SERVICE, ENVIRONMENT, COMMIT_ID)` | Reads service_type; routes to `nonWebDeploy` or web SSM deploy | main, nonweb, frontend (Deploy) |
| `nonWebDeploy` | `(SERVICE, ENVIRONMENT, serviceType)` | Same script bundle as web + `non_web_healthy.sh` | deploy (nonweb path) |
| `rollout` | `(def service)` | `precheck['Rollout']`; routes to `nonwebRollout` for nonweb, else web canary loop | main, nonweb, frontend (Rollout) |
| `nonwebRollout` | `(def service)` | Time-based stagger (canary_timing); no NewRelic canary | rollout (nonweb path) |
| `canary` | `(app_name, blue_tg_arn, region, account)` | Writes `canary.py` libraryResource, runs `python3 ./canary.py --percent_rollout 10` | rollout (web path) — per traffic_value |
| `destroyBlueInfra` | `(SERVICE)` | terraform destroy blue TG + old EC2s | main, nonweb, frontend (Destroy) |
| `precheck` | `runPrechecks(String stageName, Map args)` + `executePrechecks(SERVICE, List stages = ['Build'])` | Stage→precheck-method dispatch (checkGitRepo, checkTerraformStateFile, validateAmiExists, getInstanceCountInGreenTargetGroup, checkTargetGroupsAndTags, checkroutingeligibility, checkDestroyPrerequisites, checkRollbackPrerequisites) | every main stage |
| `pre_deployment` | `(SERVICE, JFROG_BUILD, COMMIT_ID, backup)` | Sub-stages `1.1 Validate Parameters` / `1.2 Load Service Configuration` / `1.3 Validate Config Resources`. The 1.3 sub-stage runs AWS describe-* for AMI/subnet/SG/key-pair/IAM-profile — NotFound = config-validation failure, NOT health_check. | hotfix-noncanary (Pre-Deployment) |
| `instance_provisioning` | `(String SERVICE)` | Validates env, fallback AMI, `aws ec2 run-instances`, registers in BLUE_TG | hotfix-noncanary (Instance Provisioning) |
| `artifact_deployment` | `(String SERVICE, String JFROG_BUILD)` | `jf rt dl` JAR → unzip → S3 upload → SSM deploy on NEW_INSTANCE_IDS | hotfix-noncanary (Artifact Deployment) |
| `health_validation` | `(String SERVICE)` | `aws elbv2 describe-target-health` waitUntil + 10-min timeout on BLUE_TG_ARN against NEW_INSTANCE_IDS | hotfix-noncanary (Health Validation) |
| `cutover_cleanup` | `(SERVICE, Boolean backup)` | Deregister OLD from BLUE, optional AMI backup, terminate OLD | hotfix-noncanary (Cutover & Cleanup) |
| `hotfix_rollback` | `()` | Deregister NEW_INSTANCE_IDS from BLUE_TG, drain wait, terminate NEW (skips if NEW_INSTANCE_IDS never set) | hotfix-noncanary (post.failure, post.aborted) |
| `rollbackMain` | `(String mode, String service)` | Main + nonweb rollback (mode: `"Single Job Rollback"` or `"non_web_rollback"`) | main_stagger_prod_plus_one + stagger-nonweb (failure / aborted) |
| `frontendRollback` | `(SERVICE, env, COMMIT_ID)` | Frontend-specific rollback (from `staggered_plugins_fe`) | stagger-prod-plus-one-frontend (failure / aborted) |
| `UpdateJiraStatus` | `(jiraTicketId)` | Moves ticket to "Closed" (or equivalent) post-success | every pipeline (post.success) |
| `Notification.rcaAlert` | `(rcaObject, channel)` | Posts RCA summary to per-service Slack channel | main_stagger_prod_plus_one (post.failure) |
| `Notification.build` | `(...)` | Posts build-stage notification | buildJob, QuickBuildJob |
| `Notification.qaSanityApproval` | `(...)` | Posts QA approval ping into per-service Slack channel | prodPlusOne (Automation) |
| `triggerRcaWebhook` | `()` + nested `.buildAlertMessage(rca)` | Calls BB-AI RCA service; returns parsed RCA object; helper to format the Slack alert line | every pipeline (post.failure, NOT_BUILT, unstable) |
| `managerApproval` | `(String submitterEmails)` | Interactive `input` Proceed/Abort, gated by submitter list | likely inside `approval()` / `approvalProd1Failure()` |

---

## 9. Stage → likely failure modes index

The agent should match the **stage marker that aborted the build** to the most-likely failure classes:

| Stage marker | Most likely failure classes | Drill |
|---|---|---|
| `(Load Library)` | scm, dependency | library branch/ref not resolvable; check `@Library` resolution |
| `(Jira Details)` | compliance | use `runbooks/compliance.md` Modes 1-5 (Mode 6 for `create-quick-infra` ONLY) |
| `(Resolve Parameters)` | config_validation, parse_error | config.json missing keys; jq parse fails |
| `(Input Validation)` | (none — pipeline-level error) | xor of JFROG_BUILD vs COMMIT_ID violated |
| `(Build)` / `(Build Artifact)` / `(Build Frontend)` | scm, dependency, java_runtime, compliance | git fetch fail, mvn dep resolution, JAR build error, signed-off SHA mismatch |
| `(Pre-Deployment)` → sub-stage `1.3 Validate Config Resources` | **config_validation** (NOT health_check) | key-pair / subnet / AMI / SG / IAM-profile NotFound; check `config.json` drift vs AWS |
| `(Instance Provisioning)` | aws_limit, config_validation | `RunInstances` quota, AMI/subnet/SG NotFound |
| `(Artifact Deployment)` | scm, ssm, network | JFrog 401/404, SSM SendCommand failure, S3 upload denial |
| `(Health Validation)` | health_check | TG never healthy; service crash; healthz 4xx/5xx |
| `(Cutover & Cleanup)` | aws_limit, ssm | deregister race; terminate denials |
| `(Infra)` / `(Infra Prod+1)` | terraform, stale_tf_state, aws_limit | terraform apply errors, "already exists" conflicts, ALB rule limit |
| `(Deploy)` / `(Deploy Prod+1)` | ssm, java_runtime, health_check | SSM exec fail; app launch crash; healthy.sh poll fail |
| `(Rollout)` | canary_fail, canary_script_error | NewRelic canary score < 80; canary.py crashed |
| `(Destroy)` / `(Destroy Prod+1)` | terraform, aws_limit | terraform destroy fail; orphaned target groups |
| `(Automation)` | timeout, dependency | QA-automation trigger fail (now disabled flow but stage marker still appears) |

---

## 10. How to use this doc from the agent

When the classifier fires and the agent enters its iter-0 batch:

1. Identify the pipeline by `build_meta.job` (match against the "Likely Jenkins job names" lines above). If unsure, read `get_jenkins_job_config(job)` to confirm script_path.
2. Identify the failed stage from the `[Pipeline] { (StageName)` markers in `log_window`. The LAST stage marker before the fatal error is the failing stage.
3. Cross-reference stage → helper in this doc's section for that pipeline.
4. Pull the helper file via `repo_read_file("jenkins_pipeline", "vars/<helper>.groovy", ...)`.
5. If the failure is deep, follow the helper chain inside the section.
6. Cross-check against `docops/runbooks/<error_class>.md` for the action template, but PREFER the stage-specific guidance in this doc when the two diverge (e.g. `1.3 Validate Config Resources` is config-validation, NOT health_check).

This doc is meant to be loaded via `read_doc("jenkins_pipelines_golden")` once the agent has identified the job, OR pre-fetched by `_build_tool_context` whenever `build_meta.job` matches a known pipeline family.

---

## 11. Maintenance

* **When a new pipeline lands**, add a section here.
* **When a helper changes signature**, update both the section using it AND the helper table.
* **When a stage's failure mode catches a wrong-RCA**, add a row to the stage→failure-modes index (§9).
* **Source of truth** is `jenkins_pipeline/` git master, not this doc — re-verify by reading the file when in doubt.
