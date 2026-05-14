import httpx
from functools import lru_cache

# Jenkins REST client
# Base: http://10.34.42.254:8080
# Auth: g.hariharan@blackbuck.com : <jenkins_token from SOPS keys.enc.yaml>


async def get_console_log(job: str, build: str | int, base_url: str, auth: tuple) -> str:
    url = f"{base_url}/job/{job}/{build}/consoleText"
    async with httpx.AsyncClient(timeout=30) as client:
        r = await client.get(url, auth=auth)
        r.raise_for_status()
        return r.text


async def get_build_meta(job: str, build: str | int, base_url: str, auth: tuple) -> dict:
    url = f"{base_url}/job/{job}/{build}/api/json"
    async with httpx.AsyncClient(timeout=10) as client:
        r = await client.get(url, auth=auth)
        r.raise_for_status()
        return r.json()


async def get_last_failed_build(job: str, base_url: str, auth: tuple) -> dict:
    url = f"{base_url}/job/{job}/lastFailedBuild/api/json"
    async with httpx.AsyncClient(timeout=10) as client:
        r = await client.get(url, auth=auth)
        r.raise_for_status()
        return r.json()


async def get_job_config(job: str, base_url: str, auth: tuple) -> dict:
    """Fetch the Jenkins job's config.xml and extract the bits the agent
    cares about: which SCM repo holds the pipeline definition, which branch,
    and which script path is run (e.g. `main_stagger_prod_plus_one.groovy`).

    Returns a structured dict; the raw XML stays on the side under "raw_xml"
    in case the agent needs it.
    """
    url = f"{base_url}/job/{job}/config.xml"
    async with httpx.AsyncClient(timeout=10) as client:
        r = await client.get(url, auth=auth)
        r.raise_for_status()
        xml = r.text

    return _parse_job_config(xml)


def _parse_job_config(xml: str) -> dict:
    """Pull SCM + script path out of a Jenkins job config.xml.

    Avoids a real XML parser — Jenkins' config.xml uses well-known tag names
    so a tolerant regex sweep is good enough and stays dependency-free.
    """
    import re as _re

    def _find(tag: str) -> str | None:
        m = _re.search(rf"<{tag}>([^<]+)</{tag}>", xml)
        return m.group(1).strip() if m else None

    return {
        "scm_url": _find("url"),
        "scm_branch": _find("name") or _find("branchSpec") or _find("branch"),
        "script_path": _find("scriptPath"),
        # Inline pipeline scripts live under <script>...</script> (not common
        # for the stagger family — those use scriptPath — but cover both)
        "inline_script": _find("script"),
        "raw_xml": xml[:8000],  # cap so the LLM doesn't choke on massive XML
    }
