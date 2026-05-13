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
    """Read file from repo with optional line range."""
    file_path = REPOS_DIR / repo / path
    if not file_path.exists():
        return f"file not found: {file_path}"
    lines = file_path.read_text().splitlines()
    if start and end:
        lines = lines[start - 1:end]
    elif start:
        lines = lines[start - 1:start + 99]
    return '\n'.join(f"{i+1}: {l}" for i, l in enumerate(lines))


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


def service_lookup(service_name: str) -> dict:
    """Return config.json entry for a service."""
    config = _load_config()
    entry = config.get(service_name)
    if not entry:
        # fuzzy: partial match
        matches = [k for k in config if service_name.lower() in k.lower()]
        if matches:
            return {"matches": matches, "hint": "use exact name"}
        return {"error": f"service '{service_name}' not found in config.json"}
    return entry
