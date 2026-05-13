"""Build a sanitized, compressed log window for LLM consumption.

Strategy:
- Always scan the FULL log (post noise-filter) for error markers, NOT just the
  tail. Real causes are often buried before retry noise / cleanup blocks.
- For each marker, include ±CONTEXT_LINES of surrounding context.
- Always include the last 50 lines (final state / exit reason).
- Cap at MAX_LINES; if exceeded, keep last MAX_LINES (most recent errors win).
- deep=true uses wider context (DEEP_CONTEXT_LINES) and higher cap.

Unconditional pre-window cleaning:
- ANSI escape strip
- Drop low-signal noise: INFO/DEBUG/[Pipeline] markers, Maven Downloaded from,
  Progress percent lines, blank lines
- Dedupe consecutive identical lines (retry-spam compression)
"""
import re

# ANSI escape code pattern
ANSI_ESCAPE = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')

# Patterns that indicate error lines
ERROR_PATTERNS = re.compile(
    r'(error|ERROR:|Exception|FAILURE|FATAL|parse error|Caused by|'
    r'stack trace|exit code|Result !=0|BUILD FAILED|fatal:|FAILED)',
    re.IGNORECASE,
)

# Lines to drop pre-window (low-signal noise that bloats tokens)
NOISE_PATTERNS = re.compile(
    r'^\s*(\[Pipeline\] (?:end|}|stage|node|withCredentials|withEnv|script|sh)|'
    r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*?DEBUG\b|'
    r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*?INFO\b|'
    r'\[INFO\]|\[DEBUG\]|'
    r'Downloading from|Downloaded from|'
    r'Progress \(\d+\):|'
    r'\d+/\d+ KB|'
    # Canary / Kayenta polling spam (often 100s of repeats per build)
    r'Got canary run result successfully\.|'
    r'Canary run triggered successfully\.|'
    r'Response from Kayenta\{|'
    r'triggering canary run for canary|'
    # Generic poll/wait spam
    r'Waiting for|'
    r'Polling for|'
    # Maven / gradle download chatter
    r'Resolved \S+ in \d+(\.\d+)? s|'
    r'^\s*\.{3,}\s*$|'
    # Terraform plan refresh noise
    r'Refreshing state\.\.\.|'
    r'Reading\.\.\.|'
    r'Read complete after)\b',
    re.IGNORECASE,
)

CONTEXT_LINES = 50          # default ±N around each error hit
MAX_LINES = 400             # default cap (raised from 300 — see all errors)
DEEP_CONTEXT_LINES = 100    # deep mode
DEEP_MAX_LINES = 800        # deep cap


MAX_LINE_LEN = 250  # truncate long lines (config dumps, large JSON blobs)
# Single-line dict/JSON dumps: 3+ key=value pairs separated by commas →
# drop entirely. These are big config dumps that bloat tokens but rarely
# hold the failure cause.
_BIG_DICT_RE = re.compile(r"=\S+\s*,\s*\S+=\S+\s*,\s*\S+=\S+\s*,")


def _strip_and_filter(raw_log: str) -> list[str]:
    """ANSI strip, drop noise lines, dedupe consecutive duplicates, truncate long lines."""
    out = []
    prev = None
    for line in raw_log.splitlines():
        line = ANSI_ESCAPE.sub('', line)
        if not line.strip():
            continue
        if NOISE_PATTERNS.search(line):
            continue
        if line == prev:
            continue
        # Drop massive single-line dict/JSON dumps (service config snapshots, etc.)
        if len(line) > 400 and _BIG_DICT_RE.search(line):
            continue
        # Truncate any remaining long lines (large JSON values, command output)
        if len(line) > MAX_LINE_LEN:
            line = line[:MAX_LINE_LEN] + " …[truncated]"
        out.append(line)
        prev = line
    return out


# Stage markers: "[Pipeline] { (Build)", "[Pipeline] { (Rollout)", etc.
_STAGE_RE = re.compile(r"\[Pipeline\] \{ \(([^)]+)\)")

# Failure markers: anything past these lines is post-failure cleanup, not
# the failing stage. "Declarative: Post Actions" runs *after* failure so we
# must stop scanning before it.
_FAILURE_MARKER_RE = re.compile(
    r"(Rolling Back as Result|Rollout back as Canary|"
    r"BUILD FAILED|ERROR:|hudson\.AbortException|"
    r"Canary run failed for canary run id|"
    r'"canary_run_status"\s*:\s*"Fail"|'
    r"Stage \".*?\" skipped due to earlier failure)",
    re.IGNORECASE,
)

# Stages we always ignore — they wrap post{} blocks, not real pipeline work
_IGNORED_STAGES = {
    "declarative: post actions",
    "declarative: tool install",
    "declarative: agent setup",
    "post actions",
}


def extract_failed_stage(raw_log: str) -> str | None:
    """Find the user-defined stage where the failure actually happened.

    Strategy: scan log, track last stage entry. Stop at first failure marker.
    Skip Jenkins-internal stages like 'Declarative: Post Actions' which wrap
    the post{} block that runs AFTER the real failure.
    """
    last_real_stage = None
    for line in raw_log.splitlines():
        m = _STAGE_RE.search(line)
        if m:
            name = m.group(1)
            if name.lower() not in _IGNORED_STAGES:
                last_real_stage = name
            continue
        # Once we see a failure marker, stop — anything after is cleanup
        if _FAILURE_MARKER_RE.search(line):
            break
    return last_real_stage


def _anchored_window(lines: list[str], context: int, cap: int) -> str:
    """Error-anchored window: ±context around each error hit + last 50 lines."""
    hits = {i for i, l in enumerate(lines) if ERROR_PATTERNS.search(l)}
    if not hits:
        return '\n'.join(lines[-cap:])

    selected = set()
    for idx in hits:
        for j in range(max(0, idx - context), min(len(lines), idx + context + 1)):
            selected.add(j)
    # always include last 50
    for j in range(max(0, len(lines) - 50), len(lines)):
        selected.add(j)

    result = [lines[i] for i in sorted(selected)]
    if len(result) > cap:
        result = result[-cap:]
    return '\n'.join(result)


def extract_window(raw_log: str, deep: bool = False) -> str:
    """Return sanitized + compressed log window. Scans FULL log for errors."""
    lines = _strip_and_filter(raw_log)
    if not lines:
        return ""

    if deep:
        return _anchored_window(lines, DEEP_CONTEXT_LINES, DEEP_MAX_LINES)
    return _anchored_window(lines, CONTEXT_LINES, MAX_LINES)
