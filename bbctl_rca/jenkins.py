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
