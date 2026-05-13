# bbctl-rca Day 1 Setup — Commands Log

Target: `i-0ca911dd5fdd22584` (Prod-bbctl-backend, 10.34.120.223, Ubuntu 22.04.5 LTS)

**Last updated: 2026-05-12**

### Status summary

| Step | Status |
|------|--------|
| Step 1 — SOPS + age install | ✅ Done |
| Step 2 — age key pair | ✅ Done |
| Step 3 — webhook HMAC secret | ✅ Done |
| Step 4 — secrets file | ✅ Done |
| Step 5 — working dirs | ✅ Done |
| Step 6 — CloudWatch agent | ✅ Done |
| Step 7 — CloudWatch alarms | ⏳ Pending (need SNS ARN) |
| Day 2 — IMDSv2 enforce | ✅ Done |
| Days 3-4 — Jenkins setup | ✅ Partial (see below) |
| Day 5 — Clone repos + S3 docs sync | ✅ Done |

---

## Step 1 — Install SOPS + age ✅

```bash
sudo apt-get update && sudo apt-get install -y age

sudo wget -q "https://github.com/getsops/sops/releases/download/v3.9.4/sops-v3.9.4.linux.amd64" \
  -O /usr/local/bin/sops
sudo chmod 755 /usr/local/bin/sops

sops --version && age --version && age-keygen --version
# Expected: sops 3.9.4 / age 1.0.0 / age-keygen 1.0.0
```

---

## Step 2 — Generate age key pair ✅

```bash
sudo mkdir -p /etc/bbctl-rca/keys
sudo chown ubuntu:ubuntu /etc/bbctl-rca/keys
age-keygen -o /etc/bbctl-rca/keys/bbctl-rca.key
chmod 600 /etc/bbctl-rca/keys/bbctl-rca.key
cat /etc/bbctl-rca/keys/bbctl-rca.key
```

Public key (saved 2026-05-12):
```
age1m7rfcvvzpe5fhxpjwgagfw5naahd4j7fscm266cj283hymrkgd8s7t2mcy
```

---

## Step 3 — Generate webhook HMAC secret ✅

```bash
openssl rand -hex 32
# Store output as BBCTL_WEBHOOK_SECRET in Jenkins credentials
```

---

## Step 4 — Create + encrypt secrets file ✅

```bash
sudo chown ubuntu:ubuntu /etc/bbctl-rca

cat > /tmp/keys-plain.yaml << 'EOF'
llm_api_key: "<GEMINI_OR_ANTHROPIC_API_KEY>"
llm_provider: "gemini"
jenkins_token: "<JENKINS_API_TOKEN_FOR_BBCTL_USER>"
jenkins_url: "http://10.34.42.254:8080"
webhook_secret: "<OPENSSL_RAND_HEX_32_OUTPUT>"
github_pat: "<GITHUB_PAT_WITH_REPO_SCOPE>"
slack_webhook_url: ""
EOF

SOPS_AGE_RECIPIENTS=age1m7rfcvvzpe5fhxpjwgagfw5naahd4j7fscm266cj283hymrkgd8s7t2mcy \
  sops --encrypt \
  --age age1m7rfcvvzpe5fhxpjwgagfw5naahd4j7fscm266cj283hymrkgd8s7t2mcy \
  /tmp/keys-plain.yaml > /etc/bbctl-rca/keys.enc.yaml

chmod 600 /etc/bbctl-rca/keys.enc.yaml
rm -f /tmp/keys-plain.yaml

# Verify decrypt works
SOPS_AGE_KEY_FILE=/etc/bbctl-rca/keys/bbctl-rca.key \
  sops -d /etc/bbctl-rca/keys.enc.yaml | grep llm_provider
# Expected: llm_provider: gemini
```

---

## Step 5 — Create working directories ✅

```bash
sudo mkdir -p /var/cache/bbctl-rca /var/log/bbctl-rca /opt/bbctl-rca/repos
sudo chown -R ubuntu:ubuntu /var/cache/bbctl-rca /var/log/bbctl-rca /opt/bbctl-rca
ls -la /var/cache/bbctl-rca /var/log/bbctl-rca /opt/bbctl-rca/
```

Dirs:
- `/var/cache/bbctl-rca` — boltdb cache + dedup table
- `/var/log/bbctl-rca` — app logs
- `/opt/bbctl-rca/repos` — cloned jenkins_pipeline + InfraComposer

---

## Step 6 — Install + configure CloudWatch agent ✅

```bash
wget -q https://s3.amazonaws.com/amazoncloudwatch-agent/ubuntu/amd64/latest/amazon-cloudwatch-agent.deb \
  -O /tmp/cwagent.deb
sudo dpkg -i /tmp/cwagent.deb
rm -f /tmp/cwagent.deb

sudo tee /opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json << 'EOF'
{
  "metrics": {
    "namespace": "BBCtl/EC2",
    "metrics_collected": {
      "mem": {
        "measurement": ["mem_used_percent"],
        "metrics_collection_interval": 60
      },
      "disk": {
        "measurement": ["disk_used_percent"],
        "resources": ["/"],
        "metrics_collection_interval": 60
      }
    },
    "append_dimensions": {
      "InstanceId": "${aws:InstanceId}"
    }
  }
}
EOF

# Start agent
sudo /opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl \
  -a fetch-config -m ec2 \
  -c file:/opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json -s

sudo systemctl status amazon-cloudwatch-agent --no-pager | head -5
```

---

## Step 7 — CloudWatch alarms ⏳ PENDING — needs SNS ARN

Run from local machine with zinka-cost profile once SNS ARN available.

```bash
# Disk > 75% alarm
aws cloudwatch put-metric-alarm \
  --alarm-name "bbctl-ec2-disk-high" \
  --namespace "BBCtl/EC2" \
  --metric-name disk_used_percent \
  --dimensions Name=InstanceId,Value=i-0ca911dd5fdd22584 Name=path,Value=/ Name=device,Value=xvda1 Name=fstype,Value=ext4 \
  --statistic Average \
  --period 300 \
  --evaluation-periods 2 \
  --threshold 75 \
  --comparison-operator GreaterThanThreshold \
  --alarm-actions <SNS_ARN_OR_SKIP> \
  --profile zinka-cost --region ap-south-1

# Memory > 85% alarm
aws cloudwatch put-metric-alarm \
  --alarm-name "bbctl-ec2-mem-high" \
  --namespace "BBCtl/EC2" \
  --metric-name mem_used_percent \
  --dimensions Name=InstanceId,Value=i-0ca911dd5fdd22584 \
  --statistic Average \
  --period 300 \
  --evaluation-periods 2 \
  --threshold 85 \
  --comparison-operator GreaterThanThreshold \
  --alarm-actions <SNS_ARN_OR_SKIP> \
  --profile zinka-cost --region ap-south-1
```

---

## Day 2 — IMDSv2 enforce ✅ (run from local machine)

```bash
aws ec2 modify-instance-metadata-options \
  --instance-id i-0ca911dd5fdd22584 \
  --http-tokens required \
  --http-put-response-hop-limit 1 \
  --profile zinka-cost --region ap-south-1
```

---

## Days 3-4 — Jenkins master setup ✅ Partial

On Jenkins master (10.34.42.254):

### Done ✅
- **MCP plugin installed** via `Manage Jenkins → Plugins → Available → "MCP Server"`
  - Plugin installed but NOT yet active — servlet routes not registered until restart
  - Midnight restart planned to activate
- **`BBCTL_WEBHOOK_SECRET` credential** added — Secret text in Jenkins credentials, value = webhook HMAC from Step 3
- **Jenkins auth**: Jenkins uses Google SSO (CloudFlare) — no local users possible
  - Using `g.hariharan@blackbuck.com` API token (`11628cd0a08db14ecb5770fc20c02892bf`) stored in `keys.enc.yaml → jenkins_token`
  - No dedicated `bbctl-rca-bot` user needed / possible

### Decision — Jenkins REST API (Option A)
MCP plugin `/mcp-server/mcp` returns HTTP 404 until restart. Jobs running, can't restart now.

**Use Jenkins REST API directly** — same data, no plugin required:
```bash
# Full build console log
curl -u "g.hariharan@blackbuck.com:<TOKEN>" \
  "http://10.34.42.254:8080/job/<JOB>/<BUILD>/consoleText"

# Build metadata
curl -u "g.hariharan@blackbuck.com:<TOKEN>" \
  "http://10.34.42.254:8080/job/<JOB>/<BUILD>/api/json"
```

### TODO after midnight restart
```bash
# Verify MCP plugin active
curl -s -o /dev/null -w "%{http_code}" \
  -u "g.hariharan@blackbuck.com:11628cd0a08db14ecb5770fc20c02892bf" \
  "http://10.34.42.254:8080/mcp-health"
# Expected: 200
```
If 200 → can use MCP plugin instead of raw REST (either works).

---

## Day 5 — Clone repos on bbctl-ec2 ✅

Org: `BLACKBUCK-LABS`. Auth: `Jenkins-git-bb` PAT (interactive, not in URL).

```bash
cd /opt/bbctl-rca/repos

git clone https://github.com/BLACKBUCK-LABS/jenkins_pipeline.git
# 24912 objects, 97.33 MiB

git clone https://github.com/BLACKBUCK-LABS/InfraComposer.git
# 5897 objects, 3.61 MiB

chmod -R a-w /opt/bbctl-rca/repos/
du -sh /opt/bbctl-rca/repos/   # 127M
ls -la /opt/bbctl-rca/repos/
```

Result:
```
127M    /opt/bbctl-rca/repos/
dr-xr-xr-x  InfraComposer
dr-xr-xr-x  jenkins_pipeline
```

Both dirs read-only (`dr-xr-xr-x`). ✅

---

## Key values reference (REDACTED — stored in /etc/bbctl-rca/keys.enc.yaml)

| Key | Location |
|-----|----------|
| LLM API key | `keys.enc.yaml → llm_api_key` |
| Jenkins token | `keys.enc.yaml → jenkins_token` |
| Webhook secret | `keys.enc.yaml → webhook_secret` + Jenkins credential `BBCTL_WEBHOOK_SECRET` |
| GitHub PAT | `keys.enc.yaml → github_pat` |
| age private key | `/etc/bbctl-rca/keys/bbctl-rca.key` (chmod 600) |
| age public key | `age1m7rfcvvzpe5fhxpjwgagfw5naahd4j7fscm266cj283hymrkgd8s7t2mcy` |
