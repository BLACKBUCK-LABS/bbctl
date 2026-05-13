"""Build a sanitized, compressed log window for LLM consumption.

Strategy (cheapest path first):
1. Tier 1 (default): tail-only — last TAIL_LINES after noise filtering.
   Most Jenkins failures put the error at the end (pipeline exits on first
   failure). If the tail contains at least one error marker → return that.
2. Tier 2 (fallback): error-anchored — ±CONTEXT_LINES around each hit + last
   50 lines, capped at MAX_LINES. Used when tail has no error marker (rare:
   error then post-cleanup noise).
3. Tier 3 (deep=true): same as Tier 2 but with larger limits.

Also performed unconditionally:
- ANSI stripping
- Drop INFO / DEBUG / Pipeline noise lines
- Dedupe consecutive identical lines
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
    r'\d+/\d+ KB)\b',
    re.IGNORECASE,
)

# Tier configuration
TAIL_LINES = 200            # Tier 1: how many tail lines after filtering
CONTEXT_LINES = 50          # Tier 2: ±N around each error hit
MAX_LINES = 300             # Tier 2 cap
DEEP_CONTEXT_LINES = 100    # Tier 3
DEEP_MAX_LINES = 500


def _strip_and_filter(raw_log: str) -> list[str]:
    """ANSI strip, drop noise lines, dedupe consecutive duplicates."""
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
        out.append(line)
        prev = line
    return out


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
    """Return sanitized + compressed log window. Progressive tiers per docstring."""
    lines = _strip_and_filter(raw_log)
    if not lines:
        return ""

    if deep:
        return _anchored_window(lines, DEEP_CONTEXT_LINES, DEEP_MAX_LINES)

    # Tier 1: tail-only
    tail = lines[-TAIL_LINES:]
    if any(ERROR_PATTERNS.search(l) for l in tail):
        return '\n'.join(tail)

    # Tier 2: error-anchored fallback
    return _anchored_window(lines, CONTEXT_LINES, MAX_LINES)
