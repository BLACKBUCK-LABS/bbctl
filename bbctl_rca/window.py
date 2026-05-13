import re

# ANSI escape code pattern
ANSI_ESCAPE = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')

# Patterns that indicate error lines
ERROR_PATTERNS = re.compile(
    r'(error|ERROR:|Exception|FAILURE|FATAL|parse error|Caused by|'
    r'stack trace|exit code|Result !=0|BUILD FAILED|fatal:|FAILED)',
    re.IGNORECASE
)

CONTEXT_LINES = 50
MAX_LINES = 300


def extract_window(raw_log: str) -> str:
    lines = raw_log.splitlines()
    lines = [ANSI_ESCAPE.sub('', line) for line in lines]

    hit_indices = {
        i for i, line in enumerate(lines)
        if ERROR_PATTERNS.search(line)
    }

    if not hit_indices:
        # fallback: last 200 lines
        return '\n'.join(lines[-200:])

    # expand ±CONTEXT_LINES around each hit
    selected = set()
    for idx in hit_indices:
        for j in range(max(0, idx - CONTEXT_LINES), min(len(lines), idx + CONTEXT_LINES + 1)):
            selected.add(j)

    # always include last 50 lines
    for j in range(max(0, len(lines) - 50), len(lines)):
        selected.add(j)

    result_lines = [lines[i] for i in sorted(selected)]

    # cap at MAX_LINES
    if len(result_lines) > MAX_LINES:
        result_lines = result_lines[-MAX_LINES:]

    return '\n'.join(result_lines)
