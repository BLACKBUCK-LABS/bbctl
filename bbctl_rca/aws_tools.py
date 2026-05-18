"""Cross-account AWS describe tools for the RCA agent.

Scope (Option C — no SSM):
  - aws_describe_target_health  (ALB target health states)
  - aws_describe_target_group   (TG health-check config)
  - aws_describe_instance       (EC2 state, network, tags)
  - aws_describe_listener_rule  (ALB rule conditions/actions/weights)

Explicitly NOT implemented: aws_run_ssm_command. RCA never logs into
instances. For instance-side root cause the LLM tells the operator to
use `bbctl shell <instance_id>` themselves.

Auth model:
  - bbctl-rca runs as iam:Role bbctl-backend-service in account
    735317561518 (zinka). When the target ARN's account matches, use
    the default boto3 session (no STS).
  - Otherwise STS AssumeRole into
    arn:aws:iam::<account>:role/BBCTLRcaReadOnly using sts boto3 client
    and cache the temp credentials per-RCA (15-min session).

Account map locked in docs/rca/aws_iam_manual_setup.md:
  zinka     = 735317561518   (host)
  bbfinserv = 075903075452
  divum     = 597070799581
  tzf       = 476114138058
"""
from __future__ import annotations

import re
from typing import Any

import boto3
from botocore.exceptions import BotoCoreError, ClientError


HOST_ACCOUNT = "735317561518"
ACCOUNT_NAME_TO_ID = {
    "zinka":     "735317561518",
    "bbfinserv": "075903075452",
    "divum":     "597070799581",
    "tzf":       "476114138058",
}

# Per-RCA STS credential cache. Keyed by account_id; value is a dict of
# {AccessKeyId, SecretAccessKey, SessionToken, Expiration}. Cleared
# whenever the process restarts (good enough; STS sessions are 15 min).
_STS_CREDS_CACHE: dict[str, dict] = {}


def _account_id_from_arn(arn: str) -> str | None:
    """Extract the 12-digit account ID from an AWS ARN."""
    m = re.match(r"^arn:aws:[^:]+:[^:]*:(\d{12}):", arn)
    return m.group(1) if m else None


def _resolve_account_id(*, arn: str | None = None,
                       aws_account: str | None = None) -> str:
    """Pick the account ID to assume into. ARN wins if provided; else
    fall back to the service.lookup.aws_account name."""
    if arn:
        aid = _account_id_from_arn(arn)
        if aid:
            return aid
    if aws_account:
        mapped = ACCOUNT_NAME_TO_ID.get(aws_account.lower())
        if mapped:
            return mapped
        # Already an account ID?
        if re.fullmatch(r"\d{12}", aws_account):
            return aws_account
    # Default to host
    return HOST_ACCOUNT


def _assume_role(account_id: str) -> dict | None:
    """STS AssumeRole into <account_id>:role/BBCTLRcaReadOnly. Caches
    result. Returns None for the host account (no STS needed)."""
    if account_id == HOST_ACCOUNT:
        return None
    if account_id in _STS_CREDS_CACHE:
        return _STS_CREDS_CACHE[account_id]
    sts = boto3.client("sts")
    role_arn = f"arn:aws:iam::{account_id}:role/BBCTLRcaReadOnly"
    resp = sts.assume_role(
        RoleArn=role_arn,
        RoleSessionName=f"bbctl-rca-{account_id}",
        DurationSeconds=900,
    )
    creds = resp["Credentials"]
    _STS_CREDS_CACHE[account_id] = creds
    return creds


def _get_client(service: str, *, account_id: str, region: str):
    """Return a boto3 client for `service` in `region` of `account_id`.
    Uses default creds for the host account; STS AssumeRole for others."""
    creds = _assume_role(account_id)
    if creds is None:
        return boto3.client(service, region_name=region)
    return boto3.client(
        service,
        region_name=region,
        aws_access_key_id=creds["AccessKeyId"],
        aws_secret_access_key=creds["SecretAccessKey"],
        aws_session_token=creds["SessionToken"],
    )


def _region_from_arn(arn: str) -> str | None:
    """ARN segment 3 = region. Empty for global ARNs."""
    parts = arn.split(":")
    if len(parts) >= 4 and parts[3]:
        return parts[3]
    return None


def _err(msg: str) -> dict:
    return {"error": msg}


# ─── Tool implementations ──────────────────────────────────────────────


def describe_target_health(target_group_arn: str,
                            aws_region: str | None = None,
                            aws_account: str | None = None) -> dict:
    """Return live target-health state for every target in the TG."""
    try:
        account_id = _resolve_account_id(arn=target_group_arn,
                                          aws_account=aws_account)
        region = aws_region or _region_from_arn(target_group_arn) or "ap-south-1"
        client = _get_client("elbv2", account_id=account_id, region=region)
        resp = client.describe_target_health(TargetGroupArn=target_group_arn)
    except (BotoCoreError, ClientError) as e:
        return _err(f"describe_target_health failed: {e}")
    out = []
    for d in resp.get("TargetHealthDescriptions", []):
        out.append({
            "target_id": (d.get("Target") or {}).get("Id"),
            "port": (d.get("Target") or {}).get("Port"),
            "state": (d.get("TargetHealth") or {}).get("State"),
            "reason": (d.get("TargetHealth") or {}).get("Reason"),
            "description": (d.get("TargetHealth") or {}).get("Description"),
        })
    return {"target_group_arn": target_group_arn,
            "account_id": account_id,
            "region": region,
            "targets": out}


def describe_target_group(target_group_arn: str,
                          aws_region: str | None = None,
                          aws_account: str | None = None) -> dict:
    """Return the TG's health-check config (path, port, interval, etc.)."""
    try:
        account_id = _resolve_account_id(arn=target_group_arn,
                                          aws_account=aws_account)
        region = aws_region or _region_from_arn(target_group_arn) or "ap-south-1"
        client = _get_client("elbv2", account_id=account_id, region=region)
        resp = client.describe_target_groups(TargetGroupArns=[target_group_arn])
    except (BotoCoreError, ClientError) as e:
        return _err(f"describe_target_group failed: {e}")
    groups = resp.get("TargetGroups", [])
    if not groups:
        return _err("target group not found")
    tg = groups[0]
    return {
        "target_group_arn": target_group_arn,
        "name": tg.get("TargetGroupName"),
        "protocol": tg.get("Protocol"),
        "port": tg.get("Port"),
        "vpc_id": tg.get("VpcId"),
        "health_check_protocol": tg.get("HealthCheckProtocol"),
        "health_check_port": tg.get("HealthCheckPort"),
        "health_check_path": tg.get("HealthCheckPath"),
        "health_check_interval_seconds": tg.get("HealthCheckIntervalSeconds"),
        "health_check_timeout_seconds": tg.get("HealthCheckTimeoutSeconds"),
        "healthy_threshold_count": tg.get("HealthyThresholdCount"),
        "unhealthy_threshold_count": tg.get("UnhealthyThresholdCount"),
    }


def describe_instance(instance_id: str,
                      aws_account: str,
                      aws_region: str) -> dict:
    """Return EC2 instance state, network, and tags."""
    try:
        account_id = _resolve_account_id(aws_account=aws_account)
        client = _get_client("ec2", account_id=account_id, region=aws_region)
        resp = client.describe_instances(InstanceIds=[instance_id])
    except (BotoCoreError, ClientError) as e:
        return _err(f"describe_instance failed: {e}")
    reservations = resp.get("Reservations", [])
    if not reservations or not reservations[0].get("Instances"):
        return _err(f"instance {instance_id} not found")
    inst = reservations[0]["Instances"][0]
    return {
        "instance_id": inst.get("InstanceId"),
        "state": (inst.get("State") or {}).get("Name"),
        "instance_type": inst.get("InstanceType"),
        "private_ip": inst.get("PrivateIpAddress"),
        "public_ip": inst.get("PublicIpAddress"),
        "vpc_id": inst.get("VpcId"),
        "subnet_id": inst.get("SubnetId"),
        "security_groups": [
            {"id": sg.get("GroupId"), "name": sg.get("GroupName")}
            for sg in inst.get("SecurityGroups") or []
        ],
        "tags": {t["Key"]: t["Value"] for t in inst.get("Tags") or []},
        "launch_time": str(inst.get("LaunchTime")),
        "ami_id": inst.get("ImageId"),
    }


def describe_listener_rule(rule_arn: str,
                            aws_region: str | None = None,
                            aws_account: str | None = None) -> dict:
    """Return ALB listener rule conditions + actions (incl. forward
    weights, the canary traffic-split source of truth)."""
    try:
        account_id = _resolve_account_id(arn=rule_arn, aws_account=aws_account)
        region = aws_region or _region_from_arn(rule_arn) or "ap-south-1"
        client = _get_client("elbv2", account_id=account_id, region=region)
        resp = client.describe_rules(RuleArns=[rule_arn])
    except (BotoCoreError, ClientError) as e:
        return _err(f"describe_listener_rule failed: {e}")
    rules = resp.get("Rules", [])
    if not rules:
        return _err("rule not found")
    r = rules[0]
    actions_out = []
    for a in r.get("Actions") or []:
        action = {"type": a.get("Type")}
        fwd = a.get("ForwardConfig") or {}
        if fwd:
            action["target_groups"] = [
                {"arn": tg.get("TargetGroupArn"), "weight": tg.get("Weight")}
                for tg in fwd.get("TargetGroups") or []
            ]
        actions_out.append(action)
    return {
        "rule_arn": rule_arn,
        "priority": r.get("Priority"),
        "conditions": r.get("Conditions"),
        "actions": actions_out,
        "is_default": r.get("IsDefault"),
    }
