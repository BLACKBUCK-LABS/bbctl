# Trace Walkthrough — Build 46 (health_check RCA)

**Job**: `stagger-prod-plus-devops-test`  
**Build**: `46`  
**Service**: `test-supply-wrapper-nonweb`  
**Result**: `health_check` — instance never became healthy  
**Cost**: `$0.3254` | **Iters**: `6` | **Tool calls**: `10`

---

## What happened in Jenkins (the error)

The `Deploy Prod+1` stage ran `healthy.sh` — a bash script that polls ALB target group health every 10s for 50 iterations (8+ minutes). Instance `i-0bae3c4ad893201ef` never flipped to `healthy`. Script exited with:
```
Health Status failed to move to healthy within the time limit
```

---

## How the trace is structured

Each section below maps to a trace block (`--- ITER N ---`) and shows:
- What LLM **saw** (messages in)
- What LLM **decided** (tool_calls out)
- What **server code** ran
- What **result** came back

---

## Iter 0 — Boot + Orientation

### LLM receives (2 messages)

| Message | Content |
|---|---|
| `role=system` | `prompts/rca_agent_system.md` — full instructions, chain-walk rules, evidence schema, tool usage rules |
| `role=user` | `log_window` (last 200 lines of Jenkins console) + `build_meta` ({job, build_id, url}) + `service.lookup(test-supply-wrapper-nonweb)` ({aws_account=zinka, aws_region=ap-south-1, rule_arn=..., target_port=8080, filebeat_log_path=...}) |

LLM has NO runbook, NO code, NO AWS state yet. Just the error log.

### LLM reasons (internal)

From log: sees `Health Status failed to move to healthy within the time limit` → error_class = `health_check`. Needs 3 things immediately:
1. Which Jenkins pipeline family? → `get_jenkins_job_config`
2. Which job_flow doc matches? → `list_job_flows`
3. What does the health_check runbook say to do? → `read_runbook`

### LLM emits — `finish_reason=tool_calls`

```json
tool_calls = [
  {"name": "get_jenkins_job_config", "arguments": {"job": "stagger-prod-plus-devops-test"}},
  {"name": "list_job_flows",         "arguments": {}},
  {"name": "read_runbook",           "arguments": {"name": "health_check"}}
]
```

**Code that runs this**: `agent.py:519` → `client.chat.completions.create(tools=TOOLS, tool_choice="auto")`  
**Tools defined in**: `bbctl_rca/tool_schemas.py` (19 function schemas)

### Server dispatches 3 tools in parallel

**Code path**: `agent.py:681-726` → `_dispatch_tool(name, args, ctx)` → `agent_dispatch.py TOOL_DISPATCH[name]`

#### Tool #1 — `get_jenkins_job_config`
- **Dispatch**: `agent_dispatch.py` → `mcp_tools.get_jenkins_job_config` (wired in `agent.py::_dispatch_tool` special case for Jenkins creds)
- **Code**: `bbctl_rca/jenkins.py::get_job_config("stagger-prod-plus-devops-test")`
- **What it does**: GETs `/job/stagger-prod-plus-devops-test/config.xml` from Jenkins REST API, regex-extracts `scriptPath`
- **Result returned**:
```json
{"scm_url": "https://github.com/BLACKBUCK-LABS/jenkins_pipeline",
 "script_path": "main_stagger_prod_plus_one.groovy",
 "inline_script": null}
```

#### Tool #2 — `list_job_flows`
- **Dispatch**: `agent_dispatch.py` → `mcp_tools.list_job_flows`
- **Code**: `bbctl_rca/mcp_tools.py::list_job_flows()` — walks `docops/job_flows/*.md`, extracts first paragraph of `## What this class means`
- **Result**: list of 6 job families with match criteria snippets

#### Tool #3 — `read_runbook`
- **Dispatch**: `agent_dispatch.py` → `mcp_tools.read_runbook`
- **Code**: `bbctl_rca/mcp_tools.py::read_runbook("health_check")` — reads `docops/runbooks/health_check.md` from disk
- **Result**: full runbook content including MANDATORY drill plan (DescribeTargetGroups + DescribeTargetHealth + DescribeInstances + read deploy helper + read health script)

### Conversation state after iter 0

```
[system]  rca_agent_system.md
[user]    log + meta + service.lookup
[assistant] tool_calls=[get_jenkins_job_config, list_job_flows, read_runbook]
[tool]    {script_path="main_stagger_prod_plus_one.groovy"}
[tool]    [{name:"main_stagger_prod_plus_one",...}, ...]
[tool]    health_check.md content (drill plan: read deployProdPlusOne.groovy, call 3 AWS APIs)
```

---

## Iter 1 — Big parallel batch (5 tools)

### LLM reads iter 0 results

- `script_path="main_stagger_prod_plus_one.groovy"` → matches job_flow `main_stagger_prod_plus_one`
- health_check runbook says: MANDATORY read `vars/prodPlusOne.groovy` (chain-walk start), call 3 AWS describes
- TG ARN + instance ID available in `service.lookup` and log

### LLM emits 5 parallel tool calls

```json
tool_calls = [
  {"name": "read_job_flow",   "arguments": {"name": "main_stagger_prod_plus_one"}},
  {"name": "repo_read_file",  "arguments": {"repo": "jenkins_pipeline", "path": "vars/prodPlusOne.groovy", "start": 1, "end": 80}},
  {"name": "aws_describe",    "arguments": {"service": "elbv2", "operation": "DescribeTargetGroups", "params": {"TargetGroupArns": ["arn:aws:...11b6eb5b298712fc"]}, "aws_account": "zinka", "aws_region": "ap-south-1"}},
  {"name": "aws_describe",    "arguments": {"service": "elbv2", "operation": "DescribeTargetHealth", "params": {"TargetGroupArn": "arn:aws:...11b6eb5b298712fc"}, "aws_account": "zinka", "aws_region": "ap-south-1"}},
  {"name": "aws_describe",    "arguments": {"service": "ec2",   "operation": "DescribeInstances",    "params": {"InstanceIds": ["i-0bae3c4ad893201ef"]}, "aws_account": "zinka", "aws_region": "ap-south-1"}}
]
```

### Server dispatches all 5

#### Tool #4 — `read_job_flow`
- **Code**: `mcp_tools.py::read_job_flow("main_stagger_prod_plus_one")` — reads `docops/job_flows/main_stagger_prod_plus_one.md`
- **Result**: stage→helper mapping table. Key: `Deploy Prod+1` stage → `vars/prodPlusOne.groovy` → `vars/deployProdPlusOne.groovy` chain

#### Tool #5 — `repo_read_file(vars/prodPlusOne.groovy)`
- **Code**: `mcp_tools.py::repo_read_file("jenkins_pipeline", "vars/prodPlusOne.groovy", 1, 80)`
- **What it does**: opens `repos/jenkins_pipeline/vars/prodPlusOne.groovy`, reads lines 1-80, prefixes line numbers
- **Key line found**: `line 17: deployProdPlusOne(service, "preprod")` — this is what runs for `Deploy Prod+1` stage

#### Tool #6 — `aws_describe(DescribeTargetGroups)`
- **Code**: `aws_tools.py::describe("elbv2", "DescribeTargetGroups", {...}, "zinka", "ap-south-1")`
- **What it does**: STS AssumeRole into zinka account → boto3 `elbv2.describe_target_groups(TargetGroupArns=[...])`
- **Key result**: `HealthCheckPath: "/admin/version"`, `Port: 80` (ALB-side default, NOT instance port)

#### Tool #7 — `aws_describe(DescribeTargetHealth)`
- **Code**: `aws_tools.py::describe("elbv2", "DescribeTargetHealth", {...}, "zinka", "ap-south-1")`
- **What it does**: `elbv2.describe_target_health(TargetGroupArn=...)`
- **Key result**: `Target.Port: 8080`, `TargetHealth.State: "unhealthy"`, `Reason: "Target.FailedHealthChecks"`
- **IMPORTANT**: `Target.Port=8080` ← this is the REAL instance port (different from TG default Port=80)

#### Tool #8 — `aws_describe(DescribeInstances)`
- **Code**: `aws_tools.py::describe("ec2", "DescribeInstances", {...}, "zinka", "ap-south-1")`
- **What it does**: `ec2.describe_instances(InstanceIds=["i-0bae3c4ad893201ef"])`
- **Key result**: `State: running`, `InstanceType: t3a.small` — instance is alive, just not responding to health check

### Conversation state after iter 1

```
[system]  rca_agent_system.md
[user]    log + meta + service.lookup
[assistant] iter 0 tool_calls
[tool] x3  iter 0 results
[assistant] iter 1 tool_calls (5 tools)
[tool]    job_flow doc → chain: prodPlusOne → deployProdPlusOne
[tool]    prodPlusOne.groovy:17 → deployProdPlusOne(service, "preprod")
[tool]    DescribeTargetGroups → HealthCheckPath=/admin/version, Port=80(ALB)
[tool]    DescribeTargetHealth → Target.Port=8080, State=unhealthy
[tool]    DescribeInstances → State=running
```

---

## Iter 2 — Drill into deploy helper

### LLM reasons

prodPlusOne.groovy:17 calls `deployProdPlusOne(...)` → must read `vars/deployProdPlusOne.groovy` to find the `libraryResource 'scripts/healthy.sh'` reference (per health_check runbook chain-walk rule).

### LLM emits 1 tool call

```json
{"name": "repo_read_file", "arguments": {"repo": "jenkins_pipeline", "path": "vars/deployProdPlusOne.groovy", "start": 1, "end": 80}}
```

### Server runs Tool #9

- **Code**: `mcp_tools.py::repo_read_file` — reads `repos/jenkins_pipeline/vars/deployProdPlusOne.groovy` lines 1-80
- **Key line found**: `line 21: def healthyScript = libraryResource 'scripts/healthy.sh'`
- Jenkins shared-lib rule: `libraryResource 'scripts/healthy.sh'` → resolves to `resources/scripts/healthy.sh` on disk

---

## Iter 3 — Read the health poll script

### LLM reasons

`libraryResource 'scripts/healthy.sh'` → must read `resources/scripts/healthy.sh` to see the poll loop.

### LLM emits 1 tool call

```json
{"name": "repo_read_file", "arguments": {"repo": "jenkins_pipeline", "path": "resources/scripts/healthy.sh", "start": 1, "end": 40}}
```

### Server runs Tool #10

- **Code**: `mcp_tools.py::repo_read_file` — reads `repos/jenkins_pipeline/resources/scripts/healthy.sh`
- **Key lines found**:
  - `ITERATIONS=50` — polls 50 times
  - `sleep 10` — every 10 seconds (total 8+ minutes)
  - `exit 1` when still unhealthy — this is what aborted the pipeline

---

## Iter 4 — LLM drafts final answer

### LLM has all evidence

Has all 6 mandatory evidence pieces from runbook:
- jenkins_log ✓
- deployProdPlusOne.groovy ✓
- healthy.sh ✓
- DescribeTargetHealth (State=unhealthy, Target.Port=8080) ✓
- DescribeTargetGroups (HealthCheckPath=/admin/version) ✓
- DescribeInstances (State=running) ✓

LLM emits `finish_reason=stop` with reasoning + draft JSON. No more tool calls.

**Code path**: `agent.py:556-557` — `if not msg.tool_calls: final_text = msg.content`

---

## Iter 5 — Chain-walk verification inject

### Server injects `_CHAIN_VERIFY_PROMPT`

**Code**: `agent.py:626-634` — when `_parse_final_json(final_text) is None` (JSON not parseable yet) AND `_chain_verify_done == False`:
```python
messages.append({
    "role": "user",
    "content": _CHAIN_VERIFY_PROMPT   # asks LLM to verify chain-walk completeness
})
```

`_CHAIN_VERIFY_PROMPT` asks:
1. Did you follow all function calls to their vars/ implementations?
2. Did you follow all `libraryResource 'scripts/...'` references?
3. Are evidence line ranges ≤15 lines? (if >15, you used read window not specific lines)

### LLM responds

Chain verified. Evidence lines reviewed. Emits final JSON with `finish_reason=stop`.

**Code**: `agent.py:636-680` — JSON finalize step with `response_format=json_object` enforced.

---

## Server post-processing

### `_fill_repo_snippets()` — `agent.py:977`

For each repo evidence entry `{source, line_start, line_end}`:
- Opens `repos/<repo>/<path>` from disk
- Reads lines `line_start` to `line_end`
- Injects as `snippet` field verbatim

**Why**: LLM emits coordinates only (no snippet). Server fills from disk. Eliminates code hallucination — LLM cannot invent code it does not write.

### `audit.write()` — `bbctl_rca/audit.py`

Stores full RCA JSON + trace to `/var/log/bbctl-rca/audit/` + `outcomes.sqlite`.

---

## Final RCA output

```json
{
  "error_class": "health_check",
  "failed_stage": "Deploy Prod+1",
  "root_cause": "healthy.sh ran 50 iterations (8+ min), instance i-0bae3c4ad893201ef stayed unhealthy. HealthCheckPath=/admin/version returned non-2xx.",
  "evidence": [
    {"source": "jenkins_log",                                        "snippet": "Health Status failed to move to healthy..."},
    {"source": "jenkins_pipeline/vars/deployProdPlusOne.groovy",    "line_start": 21, "line_end": 21},
    {"source": "jenkins_pipeline/resources/scripts/healthy.sh",     "line_start": 21, "line_end": 23},
    {"source": "aws:target_health(arn:...11b6eb5b298712fc)",         "snippet": "State=unhealthy, Target.Port=8080"},
    {"source": "aws:target_group(arn:...11b6eb5b298712fc)",          "snippet": "HealthCheckPath=/admin/version"},
    {"source": "aws:instance(i-0bae3c4ad893201ef)",                  "snippet": "State=running"}
  ],
  "suggested_commands": [
    {"cmd": "bbctl shell i-0bae3c4ad893201ef"},
    {"cmd": "curl http://localhost:8080/admin/version"}
  ]
}
```

Port `8080` sourced from `DescribeTargetHealth.Target.Port` (not hardcoded).
HealthCheckPath `/admin/version` sourced from `DescribeTargetGroups.HealthCheckPath`.

---

## Complete file → function map

| Trace section | Code file | Function |
|---|---|---|
| API call with tools | `bbctl_rca/agent.py:519` | `client.chat.completions.create(tools=TOOLS)` |
| Tool schemas (what LLM sees) | `bbctl_rca/tool_schemas.py` | `TOOLS` list (19 definitions) |
| Tool name → Python fn | `bbctl_rca/agent_dispatch.py` | `TOOL_DISPATCH` dict |
| Dispatch + dedup | `bbctl_rca/agent.py:681-726` | `_dispatch_tool(name, args, ctx)` |
| Get Jenkins config | `bbctl_rca/jenkins.py` | `get_job_config(job)` |
| List/read job_flows | `bbctl_rca/mcp_tools.py` | `list_job_flows()`, `read_job_flow(name)` |
| List/read runbooks | `bbctl_rca/mcp_tools.py` | `list_runbooks()`, `read_runbook(name)` |
| Read repo file | `bbctl_rca/mcp_tools.py` | `repo_read_file(repo, path, start, end)` |
| AWS cross-account | `bbctl_rca/aws_tools.py` | `describe(service, operation, params, account, region)` |
| Chain-walk inject | `bbctl_rca/agent.py:626-634` | `_CHAIN_VERIFY_PROMPT` injection |
| Server fills snippets | `bbctl_rca/agent.py:977` | `_fill_repo_snippets(evidence)` |
| Store result | `bbctl_rca/audit.py` | `write(rca)` |
| Job_flow docs | `docops/job_flows/main_stagger_prod_plus_one.md` | stage→helper mapping |
| Runbook docs | `docops/runbooks/health_check.md` | drill plan, DO NOT rules |
| LLM instructions | `prompts/rca_agent_system.md` | method, chain-walk rules, schema |
