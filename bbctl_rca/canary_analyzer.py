"""Extract per-traffic-stage canary results from build log.

The Rollout stage runs `python3 canary.py --percent_rollout N` multiple times,
once per traffic value (e.g. 5, 20, 50, 100). At each stage, 7 canary configs
return Pass|Fail. This module parses the log and emits one structured row per
stage so the LLM doesn't have to reconstruct the sequence itself.

Output format:
  {
    "stages": [
      {"percent": 5,  "all_pass": true,  "fails": []},
      {"percent": 20, "all_pass": true,  "fails": []},
      {"percent": 50, "all_pass": false, "fails": ["FMS-GPS-Other-latency"]},
    ],
    "failed_at_percent": 50,
    "passed_before_failure": [5, 20],
    "load_dependent": true   # passed_before_failure non-empty
  }
"""
import re


# canary.py invocation lines have: --percent_rollout N (next int)
_PERCENT_RE = re.compile(r"--percent_rollout\s+(\d+)")
# Each canary run prints {"canary_config_name": "X", ..., "canary_run_status": "Pass|Fail"}
_RESULT_RE = re.compile(
    r'"canary_config_name"\s*:\s*"([^"]+)"[^}]*?'
    r'"canary_run_status"\s*:\s*"(Pass|Fail)"',
    re.DOTALL,
)
# Failure log line for fast-path stage tagging
_FAILED_LINE_RE = re.compile(
    r"Canary run failed for canary run id:\s*\S+\s+and canary config id:\s*(\S+)"
)


def analyze(raw_log: str) -> dict:
    """Walk log linearly, group by --percent_rollout boundary. Return stages list."""
    stages: list[dict] = []
    current = None

    for line in raw_log.splitlines():
        m = _PERCENT_RE.search(line)
        if m:
            # Close previous stage if any
            if current is not None:
                stages.append(current)
            current = {"percent": int(m.group(1)), "results": [], "fails": []}
            continue
        if current is None:
            continue

    # Re-scan: extract config+status pairs and bucket them by percent stage.
    # Easier: walk again finding percent markers + config:status blocks.
    stages = []
    current = None
    # Use line-by-line state machine; results JSON spans multiple lines so we
    # also scan the concatenated raw_log for result regex within each stage.
    last_percent_pos = -1
    percent_positions: list[tuple[int, int]] = []  # (offset, percent)
    for m in _PERCENT_RE.finditer(raw_log):
        percent_positions.append((m.start(), int(m.group(1))))

    if not percent_positions:
        return {"stages": [], "failed_at_percent": None,
                "passed_before_failure": [], "load_dependent": False}

    # Slice raw_log per stage; extract results inside each slice
    percent_positions.append((len(raw_log), -1))  # sentinel
    for i in range(len(percent_positions) - 1):
        start, percent = percent_positions[i]
        end, _ = percent_positions[i + 1]
        chunk = raw_log[start:end]
        results = []
        for r in _RESULT_RE.finditer(chunk):
            results.append({"config": r.group(1), "status": r.group(2)})
        fails = [r["config"] for r in results if r["status"] == "Fail"]
        stages.append({
            "percent": percent,
            "config_count": len(results),
            "pass_count": sum(1 for r in results if r["status"] == "Pass"),
            "all_pass": len(results) > 0 and not fails,
            "fails": fails,
        })

    failed_at = next((s["percent"] for s in stages if s["fails"]), None)
    passed_before = [s["percent"] for s in stages if s["all_pass"] and (failed_at is None or s["percent"] < failed_at)]
    return {
        "stages": stages,
        "failed_at_percent": failed_at,
        "passed_before_failure": passed_before,
        "load_dependent": bool(passed_before) and failed_at is not None,
    }
