import json
import subprocess
from pathlib import Path

REPOS_DIR = Path("/opt/bbctl-rca/repos")
DOCS_DIR = Path("/opt/bbctl-rca/docops")
CONFIG_JSON = REPOS_DIR / "jenkins_pipeline" / "resources" / "config.json"

_config: dict | None = None


def _load_config() -> dict:
    global _config
    if _config is None:
        with open(CONFIG_JSON) as f:
            _config = json.load(f)
    return _config


def repo_search(repo: str, query: str, max_results: int = 20) -> str:
    """ripgrep search over repo files."""
    repo_path = REPOS_DIR / repo
    if not repo_path.exists():
        return f"repo {repo} not found at {repo_path}"
    result = subprocess.run(
        ["rg", "--line-number", "--context", "3", "-m", str(max_results), query, str(repo_path)],
        capture_output=True, text=True
    )
    return result.stdout or f"no matches for '{query}' in {repo}"


def repo_read_file(repo: str, path: str, start: int = 0, end: int = 0) -> str:
    """Read file from repo with optional line range.

    Line numbers in the returned text are the REAL file line numbers (not
    array indices) so the agent can cite them directly in evidence.
    """
    file_path = REPOS_DIR / repo / path
    if not file_path.exists():
        return f"file not found: {file_path}"
    lines = file_path.read_text(errors="replace").splitlines()
    if start and end:
        sliced = lines[start - 1:end]
        return '\n'.join(f"{start + i}: {l}" for i, l in enumerate(sliced))
    if start:
        sliced = lines[start - 1:start + 99]
        return '\n'.join(f"{start + i}: {l}" for i, l in enumerate(sliced))
    return '\n'.join(f"{i+1}: {l}" for i, l in enumerate(lines))


def repo_list_dir(repo: str, path: str = "") -> list[str]:
    """List immediate children of a directory in the repo. Useful when the
    agent doesn't know the exact filename yet (e.g. exploring `vars/`).
    Directories are returned with a trailing `/`.
    """
    base = REPOS_DIR / repo / path
    if not base.exists():
        return [f"path not found: {path}"]
    if not base.is_dir():
        return [f"not a directory: {path}"]
    out = []
    for child in sorted(base.iterdir()):
        if child.name.startswith(".git"):
            continue
        out.append(child.name + ("/" if child.is_dir() else ""))
    return out


def repo_find_function(repo: str, name: str, max_hits: int = 5) -> str:
    """Find where a Groovy / Java / Python function is *defined* in a repo.

    Matches the common definition patterns used in this codebase:
      Groovy:  `def <name>(`, `static def <name>(`, `<name> = { ... }`
      Java:    `<modifier...> ReturnType <name>(`  (handled via simple grep)
      Python:  `def <name>(`

    Returns ripgrep-style hits with line numbers, capped at max_hits.
    """
    repo_path = REPOS_DIR / repo
    if not repo_path.exists():
        return f"repo {repo} not found at {repo_path}"
    # Two patterns OR'd: definition forms vs. assignment forms.
    pattern = rf"(?:def\s+|static\s+def\s+|^\s*){__import__('re').escape(name)}\s*[\(\=]"
    result = subprocess.run(
        ["rg", "--line-number", "--no-heading", "--type-add", "groovy:*.groovy",
         "-tgroovy", "-tjava", "-tpy",
         "-m", str(max_hits), pattern, str(repo_path)],
        capture_output=True, text=True, check=False,
    )
    if result.returncode not in (0, 1):
        return f"search error: {result.stderr.strip()[:200]}"
    out = result.stdout.strip()
    return out or f"no definitions found for '{name}' in {repo}"


def repo_recent_commits(repo: str, n: int = 10) -> str:
    """Return the last N commits with author + date + short message.

    Helps the agent answer "what changed recently?" — often the actual root
    cause of a freshly-failing pipeline is a commit landed in the last hour.
    """
    repo_path = REPOS_DIR / repo
    if not (repo_path / ".git").exists():
        return f"repo {repo} not a git clone"
    fmt = "%h %ad %an | %s"
    result = subprocess.run(
        ["git", "-C", str(repo_path), "log", f"-n{n}",
         "--date=short", f"--pretty=format:{fmt}"],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        return f"git log failed: {result.stderr.strip()[:200]}"
    return result.stdout


def docs_list() -> list[str]:
    """List available org docs."""
    return [f.name for f in sorted(DOCS_DIR.glob("*.md"))]


def docs_get(name: str) -> str:
    """Read doc content."""
    path = DOCS_DIR / name
    if not path.exists():
        path = DOCS_DIR / f"{name}.md"
    if not path.exists():
        return f"doc not found: {name}"
    return path.read_text()


_SLIM_FIELDS = (
    "name", "service_name", "service_identifier", "env",
    "aws_account", "region", "aws_region",
    "deploy_type", "infra_type", "is_non_web", "service_type",
    "instance_class", "ami", "target_group_name", "rule_arn",
    "health_check_path", "health_check_port", "canary_threshold", "traffic_values",
    "auto_scaling_group", "min_capacity", "max_capacity", "desired_capacity",
    "git_repo", "github_repo", "repo", "repo_name", "service_repo",
    "branch", "default_branch",
    "new_relic_name", "newrelic_name", "nr_app_name",
    "slack_channel",
    # Surfaced for health_check class so LLM can tell operator where to look
    "log_path", "service_port", "port", "app_port", "container_port",
    # Real-world field names used in this org's config.json (alternatives to
    # the canonical names above). `target_port` is the ALB target group port,
    # `filebeat_log_path` is the service log file path, `key_name` is the SSH
    # key name (operator builds `.pem` path from it), `server_command` is the
    # full `java ...` startup command (contains `-Dlog.dir=` hint).
    "target_port", "filebeat_log_path", "key_name", "server_command",
)


def service_lookup(service_name: str) -> dict:
    """Return slim config.json entry — only fields useful for RCA reasoning.

    Drops verbose metadata (ARNs of unrelated resources, timestamps, tags) to
    cut prompt tokens. Operator can inspect full config via repo_read_file if
    LLM requests it.
    """
    config = _load_config()
    entry = config.get(service_name)
    if not entry:
        matches = [k for k in config if service_name.lower() in k.lower()]
        if matches:
            return {"matches": matches[:5], "hint": "use exact name"}
        return {"error": f"service '{service_name}' not found in config.json"}
    # Keep only meaningful fields, drop None/empty
    slim = {k: entry[k] for k in _SLIM_FIELDS if k in entry and entry[k] not in (None, "", [], {})}
    if not slim:
        # service exists but no matching slim fields — return all top-level keys for visibility
        slim = {"_keys": list(entry.keys())[:20]}
    return slim
