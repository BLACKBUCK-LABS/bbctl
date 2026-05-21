# Trace Walkthrough — Build 5177 (aws_limit RCA)

**Job**: `Stagger Prod Plus One`  
**Build**: `5177`  
**Service**: `toll-gold`  
**Result**: `aws_limit` — ALB target group count limit hit  
**Cost**: `$0.272` | **Iters**: `5` | **Tool calls**: `7`

---

## What happened in Jenkins (the error)

The `Infra Prod+1` stage runs Terraform to create a new listener rule + target group for the prod+1 instance. Terraform called `aws_lb_listener_rule.create()` but the ALB already had 100 unique target groups attached — the AWS hard limit. Error:

```
Error: creating ELBv2 Listener Rule: TooManyUniqueTargetGroupsPerLoadBalancer:
  You have reached the maximum number of unique target groups that you can
  associate with a load balancer of type 'application': [100]
```

Terraform is the messenger. Real cause = AWS service quota exhausted on the ALB.

---

## How the trace is structured

Each iter: what LLM received → what it decided → what server code ran → what came back.

---

## Iter 0 — Boot + Orientation

### LLM receives (2 messages)

| Message | Content |
|---|---|
| `role=system` | `prompts/rca_agent_system.md` — full rules, chain-walk method, evidence schema |
| `role=user` | `log_window` (last 200 lines) + `build_meta` + `service.lookup(toll-gold)` ({aws_account=divum, rule_arn=arn:...divum...private-internal-load-blancer/...}) |

LLM scans log backwards: last fatal line = `TooManyUniqueTargetGroupsPerLoadBalancer`. Suspects `terraform` or `aws_limit` class.

### LLM emits — 3 parallel tools

```json
tool_calls = [
  {"name": "get_jenkins_job_config", "arguments": {"job": "Stagger Prod Plus One"}},
  {"name": "list_job_flows",         "arguments": {}},
  {"name": "read_runbook",           "arguments": {"name": "terraform"}}
]
```

**Code**: `agent.py:519` → `client.chat.completions.create(tools=TOOLS, tool_choice="auto")`

### Server dispatches 3 tools

| Tool | Code | Returns |
|---|---|---|
| `get_jenkins_job_config` | `jenkins.py` → Jenkins REST `/config.xml` | `{script_path: null, inline_script: "...prodPlusOne(params.SERVICE)..."}` |
| `list_job_flows` | `mcp_tools.list_job_flows()` → reads `docops/job_flows/*.md` stems | `[{name:"main_stagger_prod_plus_one", match:...}, ...]` |
| `read_runbook(terraform)` | `mcp_tools.read_runbook("terraform")` → reads `docops/runbooks/terraform.md` from disk | Terraform runbook content including **reclassify table**: `TooManyUniqueTargetGroupsPerLoadBalancer → aws_limit` AND the ALB ARN derivation formula |

**The reclassify table in `terraform.md` is what tells LLM:**
1. This is actually `aws_limit` class, not `terraform`
2. Derive ALB ARN from `service.lookup.rule_arn` using the formula:
   - `rule_arn` ends with `listener-rule/app/<alb-name>/<alb-id>/...`
   - `alb_arn` = `arn:...loadbalancer/app/<alb-name>/<alb-id>`

### Conversation state after iter 0

```
[system]  rca_agent_system.md
[user]    log + meta + service.lookup (includes rule_arn with ALB ID embedded)
[assistant] tool_calls=[get_jenkins_job_config, list_job_flows, read_runbook]
[tool]    {inline_script has "prodPlusOne(params.SERVICE)"}
[tool]    job_flows list → main_stagger_prod_plus_one match
[tool]    terraform.md → reclassify table → aws_limit + ALB ARN formula
```

---

## Iter 1 — Big parallel batch (3 tools)

### LLM reads iter 0 results

- `inline_script` has `prodPlusOne(...)` → matches `main_stagger_prod_plus_one` job flow
- terraform runbook says: reclassify to `aws_limit`, derive ALB ARN, read pipeline chain
- `service.lookup.rule_arn` = `arn:aws:...:597070799581:listener-rule/app/private-internal-load-blancer/e61d1cac4f02d585/...`
- LLM extracts: `alb_arn` = `arn:aws:...:597070799581:loadbalancer/app/private-internal-load-blancer/e61d1cac4f02d585`

### LLM emits 3 parallel tools

```json
tool_calls = [
  {"name": "read_job_flow",   "arguments": {"name": "main_stagger_prod_plus_one"}},
  {"name": "repo_read_file",  "arguments": {"repo": "jenkins_pipeline", "path": "vars/createRuleForProdPlusOne.groovy", "start": 1, "end": 80}},
  {"name": "aws_describe",    "arguments": {
    "service": "elbv2",
    "operation": "DescribeTargetGroups",
    "params": {"LoadBalancerArn": "arn:aws:...:597070799581:loadbalancer/app/private-internal-load-blancer/e61d1cac4f02d585"},
    "aws_account": "divum",
    "aws_region": "ap-south-1"
  }}
]
```

Key: LLM derived ALB ARN from `service.lookup.rule_arn` using the formula — no `<alb_arn>` placeholder.

### Server dispatches all 3

#### Tool #4 — `read_job_flow(main_stagger_prod_plus_one)`
- **Code**: `mcp_tools.read_job_flow()` → reads `docops/job_flows/main_stagger_prod_plus_one.md`
- **Returns**: Stage→helper table. `Infra Prod+1` → nested in `prodPlusOne.groovy` → calls `createRuleForProdPlusOne(service, 150)` → `vars/createRuleForProdPlusOne.groovy`

#### Tool #5 — `repo_read_file(createRuleForProdPlusOne.groovy)`
- **Code**: `mcp_tools.repo_read_file()` → opens `repos/jenkins_pipeline/vars/createRuleForProdPlusOne.groovy` lines 1-80
- **Key line found**: `line 19: createInfra(data, SERVICE, env['BUILD_ID'], priority)` — this is what triggers Terraform

#### Tool #6 — `aws_describe(DescribeTargetGroups)`
- **Code**: `aws_tools.describe("elbv2", "DescribeTargetGroups", {LoadBalancerArn: ...}, "divum", "ap-south-1")`
- **What it does**: boto3 → STS AssumeRole into divum account → `elbv2.describe_target_groups(LoadBalancerArn=...)`
- **Returns**: list of all 100 target groups on the ALB, confirms limit is hit

---

## Iter 2 — LLM drafts answer (voluntary stop)

LLM has: fatal error (log) + Groovy call chain (createRuleForProdPlusOne.groovy:19) + AWS confirmation (100 TGs). Drafts final JSON.

Emits `finish_reason=stop`. Server checks `_parse_final_json(text)` — valid JSON present → chain-walk NOT injected (skip, already has answer).

**Code path**: `agent.py:556-558` — `if not force_final and not msg.tool_calls: final_text = msg.content`

---

## Iter 3 — Chain-walk injection fires

`_parse_final_json(final_text) is None` (iter 2 text is prose + JSON, not pure JSON) → server injects `_CHAIN_VERIFY_PROMPT`.

**Code**: `agent.py:626-634`

LLM reviews chain-walk:
- `createRuleForProdPlusOne(...)` called at line 13 of `prodPlusOne.groovy` → followed to `createRuleForProdPlusOne.groovy` ✓
- `createInfra(data, SERVICE, ...)` at line 19 — local function in same file → no extra file needed ✓
- BUT: InfraComposer terraform config not read yet — LLM decides to read it for completeness

Emits 1 more tool:
```json
{"name": "repo_read_file", "arguments": {"repo": "InfraComposer", "path": "config/toll-gold/prodplusone/main.tf", "start": 1, "end": 80}}
```

**Code**: same tool dispatch → `mcp_tools.repo_read_file("InfraComposer", "config/toll-gold/prodplusone/main.tf", 1, 80)`

Returns: module declaration at lines 23-43 showing `module "createProdPlusOneInfra"` calls `module/createRuleForProdPlusOneInfra` — the TF module that creates the listener rule.

---

## Iter 4 — Final JSON emitted

Chain-walk complete. LLM emits final JSON with `finish_reason=stop`.

Server runs `_fill_repo_snippets()` — reads actual file bytes for evidence entries `line_start/line_end`.

**Code**: `agent.py:977 _fill_repo_snippets()` → reads `repos/InfraComposer/config/toll-gold/prodplusone/main.tf` lines 23-43 from disk.

---

## Final RCA output — explained

```json
{
  "error_class": "aws_limit",
  "failed_stage": "Infra Prod+1",
  "root_cause": "ALB reached 100 unique TG limit. Terraform tried to add one more.",

  "evidence": [
    // 1. Log confirms the exact AWS error
    {"source": "jenkins_log",
     "snippet": "TooManyUniqueTargetGroupsPerLoadBalancer: ...100"},

    // 2. Terraform config shows WHICH module creates the listener rule
    {"source": "InfraComposer/config/toll-gold/prodplusone/main.tf",
     "line_start": 23, "line_end": 43},
     // Lines 23-43: module "createProdPlusOneInfra" { source = "...createRuleForProdPlusOneInfra" }
     // Server filled this from disk — LLM emitted only coordinates

    // 3. AWS confirms current TG count on the ALB
    {"source": "aws:target_groups(arn:...divum.../private-internal-load-blancer/...)"}
  ],

  "suggested_commands": [
    // ALB ARN from service.lookup.rule_arn — no placeholder
    "aws elbv2 describe-target-groups --load-balancer-arn arn:...597070799581.../private-internal-load-blancer/e61d1cac4f02d585 --profile divum",
    // Quota increase
    "aws service-quotas request-service-quota-increase --quota-code L-41782ECF --desired-value 150"
  ]
}
```

---

## Complete file → function map

| Trace section | Code file | Function |
|---|---|---|
| Boot: OpenAI API call | `bbctl_rca/agent.py:519` | `client.chat.completions.create(tools=TOOLS)` |
| Tool schemas (19 definitions) | `bbctl_rca/tool_schemas.py` | `TOOLS` list |
| Tool name → Python fn | `bbctl_rca/agent_dispatch.py` | `TOOL_DISPATCH` dict |
| Jenkins job config fetch | `bbctl_rca/jenkins.py` | `get_job_config(job, url, auth)` |
| Job_flow doc selection | `bbctl_rca/mcp_tools.py` | `list_job_flows()`, `read_job_flow(name)` |
| Terraform runbook (reclassify table) | `docops/runbooks/terraform.md` | `read_runbook("terraform")` → `mcp_tools.read_runbook()` |
| ALB ARN derivation | `docops/runbooks/terraform.md` (formula in reclassify section) | LLM reads formula, derives from `service.lookup.rule_arn` |
| Groovy file chain-walk | `bbctl_rca/mcp_tools.py` | `repo_read_file("jenkins_pipeline", "vars/createRuleForProdPlusOne.groovy", 1, 80)` |
| AWS live state | `bbctl_rca/aws_tools.py` | `describe("elbv2", "DescribeTargetGroups", {LoadBalancerArn: ...}, "divum", ...)` |
| InfraComposer TF config | `bbctl_rca/mcp_tools.py` | `repo_read_file("InfraComposer", "config/toll-gold/prodplusone/main.tf", 1, 80)` |
| Chain-walk verification | `bbctl_rca/agent.py:626-634` | `_CHAIN_VERIFY_PROMPT` injection |
| Server fills snippets | `bbctl_rca/agent.py:977` | `_fill_repo_snippets(evidence)` |
| Audit store | `bbctl_rca/audit.py` | `write(rca)` |

---

## What this RCA proves works

✓ ALB ARN derived from `service.lookup.rule_arn` — no `<alb_arn>` placeholder  
✓ `divum` account used (correct — toll-gold's ALB is in divum)  
✓ InfraComposer terraform config read — operator can see exact module  
✓ Evidence line precision: main.tf lines 23-43 (module block), groovy line 19 (`createInfra` call)  
✓ Cost $0.272 (5 iters, down from $0.46 with old wrong job_flow match)  
✓ No failure signals  
✓ `failure_signals: []` — clean run

## What still needs attention

- `quota-code L-41782ECF` — should verify against AWS documentation (expected: `L-417A185B` for "Unique target groups per ALB"). Wrong code = quota request fails. LLM guessed from memory.
- `aws:target_groups` evidence `verified: null` — server couldn't verify AWS evidence (expected, no disk file to verify against). Not a bug — just unfilled.
