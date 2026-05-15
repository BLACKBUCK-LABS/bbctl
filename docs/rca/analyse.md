
/# BBCTL — Codebase & Deployment Target Analysis

Generated: 2026-05-11

## 1. Repository Layout

```
BBCTLLLM/
├── bbctl/              Go CLI (the client binary)
├── homebrew-bbctl/     Homebrew tap formula
└── jenkins_pipeline/   Jenkins shared library (deployment automation)
```

### 1.1 bbctl (client)

- **Language:** Go 1.26.2
- **Module:** `github.com/blackbuck/bbctl`
- **Entry point:** `cmd/bbctl/main.go` → `commands.Execute()`
- **Build:** `make build` → `bin/bbctl` (ldflags injects `Version`)
- **Install:** `brew install Blackbuck-LABS/bbctl/bbctl` or release binary

**Cobra commands** (`bbctl/commands/`):

| Command | File | Purpose |
|---|---|---|
| `login` / `logout` | login.go, logout.go | Google SSO OIDC (PKCE), token → `~/.bbctl/token` |
| `run <i-…> -- <cmd>` | run.go | Single command over SSM |
| `shell <i-…>` | shell.go | Interactive gated REPL |
| `upload` / `download` | upload.go, download.go | File transfer via S3 presigned URL |
| `commands` | commands.go | List safe / restricted / denied tiers |
| `interactive` | interactive.go | Picker UI when no instance specified |
| `version` | version.go | Build metadata |

**Internal packages:**

- `internal/client` — HTTP client → backend at `bbctl-rca.jinka.in`
- `internal/config` — `~/.bbctl/config.yaml` + token, embedded defaults, account-alias resolver
- `internal/ec2` — multi-account instance picker, cache, fuzzy finder
- `internal/shell` — readline shell, completer, file-reference parsing, welcome banner

**Backend endpoints used:**

```
POST /v1/commands         run/classify a command
POST /v1/instances        list EC2 in an account
POST /v1/upload           inline file push
POST /v1/download         get presigned URL for file pull
POST /v1/stage            stage file (large/restricted)
POST /v1/attach           attach to Jira ticket
POST /v1/complete         tab completion
POST /v1/classify         tier lookup
GET  /v1/accounts         list accounts
DELETE /v1/commands/{id}  cancel in-flight
```

**Auth model:** Bearer JWT from Google OIDC (Desktop App client; client secret embedded — protected by PKCE).

**Embedded account aliases:**

| Alias | Account ID |
|---|---|
| zinka | 735317561518 |
| divum | 597070799581 |
| finserv | 075903075452 |
| tzf | 476114138058 |

**Command tiers** (`internal/shell/SafeCommandsTable`):

- **Safe** (~22) — read-only, immediate exec
- **Restricted** (~80+) — auto-creates Jira `REQ-…` ticket, manager approval, one-time use
- **Denied** (~50+) — shells, raw networking, anything escaping the audit pipeline

### 1.2 homebrew-bbctl

Tap with `Formula/bbctl.rb` for `brew install`.

### 1.3 jenkins_pipeline

Groovy shared library for blue/green prod rollouts:

- `vars/deploy.groovy`, `rollout.groovy`, `rollback.groovy`, `canary.groovy`
- Pre-deploy / health / Jira / notification stages
- `resources/` — canary scripts, fluent-bit/filebeat config, supervisor, cron canary

This is the pipeline that publishes the backend to the target instance.

---

## 2. Target Instance — `i-0ca911dd5fdd22584`

### 2.1 Identity

| Field | Value |
|---|---|
| Name tag | **Prod-bbctl-backend** |
| Account | 735317561518 (**zinka**) |
| Region / AZ | ap-south-1 / ap-south-1c (Mumbai) |
| State | running |
| Launched | 2026-04-29 11:40 UTC (volume attached 2026-04-23) |
| Instance type | t3a.medium (2 vCPU / 4 GB) |
| AMI | ami-087d1c9a513324697 |
| Key pair | blackbuck_production |

### 2.2 Network

| Field | Value |
|---|---|
| VPC | vpc-0a22ad559772a470f |
| Subnet | subnet-0aa48b4991f096eb1 |
| Private IP / DNS | 10.34.120.223 / ip-10-34-120-223.ap-south-1.compute.internal |
| Public IP | none (private only) |
| Security groups | `office-ssh` (sg-08c3ae833146111e3), `zinka-mumbai-prod` (sg-09b48a8be5de6aeb6) |

**Inbound rules** (zinka-mumbai-prod):

- TCP 0-65535 from `182.74.229.168/30` (office VPN)
- **TCP 8080 from `10.34.0.0/16`** — this is the bbctl backend listener
- All-traffic from sibling Blackbuck CIDRs (Divum prod, Singapore prod, Frankfurt, etc.)

### 2.3 Storage

| Field | Value |
|---|---|
| Root volume | vol-0ce19dc2bba7a8989 |
| Size / Type / IOPS | 30 GB / gp3 / 3000 |
| Encrypted | **no** |

### 2.4 IAM & Identity

- **Instance profile:** `arn:aws:iam::735317561518:instance-profile/bbctl-backend-service`
- This is the role the backend assumes — needs `ssm:SendCommand` / `ssm:GetCommandInvocation` across all bbctl-managed accounts (cross-account assume-role), S3 audit-bucket write, Jira API secret read.

### 2.5 Metadata / Telemetry

- IMDS: `HttpTokens: optional` → **IMDSv1 allowed** (hardening gap)
- Detailed monitoring: **disabled**
- Hibernation: disabled

---

## 3. Service-Readiness Checklist

| # | Item | Status / Action |
|---|---|---|
| 1 | Instance reachable on port 8080 from internal CIDR | OK — SG rule present |
| 2 | DNS `bbctl-rca.jinka.in` resolves to this instance / its ALB | **Verify** — config default points here |
| 3 | IAM role `bbctl-backend-service` has SSM + S3 + Jira creds | **Verify** policies attached |
| 4 | SSM agent registered (instance shows in `DescribeInstanceInformation`) | **Verify** — current creds lack `ssm:DescribeInstanceInformation` |
| 5 | supervisord / systemd unit running the backend binary | **Verify** via SSM or Jenkins canary log |
| 6 | fluent-bit / filebeat shipping logs (configs in `jenkins_pipeline/resources/`) | **Verify** running |
| 7 | Jira service account secret present (for `REQ-` ticket creation) | **Verify** |
| 8 | OIDC public keys reachable (`accounts.google.com`) for JWT verify | OK if egress allowed |
| 9 | Audit S3 bucket writable for 13-month retention | **Verify** bucket policy |
| 10 | Cross-account assume-role into divum / finserv / tzf | **Verify** trust policies |

---

## 4. Gaps & Recommendations

| Severity | Item | Fix |
|---|---|---|
| High | IMDSv1 enabled (`HttpTokens: optional`) | `aws ec2 modify-instance-metadata-options --http-tokens required` |
| Medium | Root EBS unencrypted | Snapshot → re-create encrypted, swap volume during maintenance window |
| Medium | Single instance, no ASG / ALB visible | Front with internal ALB + 2 AZs for HA (backend is critical path for prod ops) |
| Medium | Detailed monitoring off | Enable; CloudWatch agent for memory/disk |
| Low | Disk 30 GB | Watch growth (logs + binary updates); set CW alarm at 75% |
| Low | SG opens `0-65535` to office VPN | Restrict to 22 + 8080 |

---

## 5. Quick Verify Commands

From a workstation with appropriate creds:

```bash
# Backend reachability
curl -sS -o /dev/null -w '%{http_code}\n' http://10.34.120.223:8080/healthz

# SSM agent registration (needs ssm:DescribeInstanceInformation)
aws ssm describe-instance-information \
  --filters "Key=InstanceIds,Values=i-0ca911dd5fdd22584" \
  --profile zinkamain --region ap-south-1

# Backend service status (via bbctl itself, once deployed)
bbctl run i-0ca911dd5fdd22584 -a zinka -- supervisorctl status
bbctl run i-0ca911dd5fdd22584 -a zinka -- tail -n 200 /var/log/bbctl-backend/app.log
```

---

## 6. Deployment Flow (inferred)

```
Jenkins (jenkins_pipeline/vars/deploy.groovy)
  ├─ precheck             (precheck.groovy)
  ├─ pre_deployment       (pre_deployment.groovy)
  ├─ build artifact       (buildJob.groovy)
  ├─ instance_provisioning (instance_provisioning.groovy)
  ├─ rollout              (rollout.groovy) → SCP/SSM to i-0ca911dd5fdd22584
  ├─ supervisor restart   (resources/supervisor.conf)
  ├─ canary check         (resources/canary.py)
  ├─ health_validation    (health_validation.groovy)
  └─ notification         (notification.groovy)
```

Rollback via `vars/rollback.groovy` / `rollbackMain.groovy`.

---

## 7. Summary

- **Codebase** = Go CLI + Jenkins Groovy library + Homebrew tap. Clean Cobra layout; backend contract well-defined.
- **Target instance** = `Prod-bbctl-backend` (zinka, ap-south-1, t3a.medium, private). Running and on-network.
- **Ready to deploy:** network path and IAM profile in place. Confirm items 2-10 in §3 before pushing a release.
- **Pre-prod hardening:** flip IMDSv2-required, encrypt root volume, add ALB+ASG for HA.
