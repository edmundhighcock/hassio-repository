"""MCP smoke test of a running add-on container over HTTP.

usage: smoke.py BASE_URL SEED_JSON     (SEED_JSON: the seed.py SEED_RESULT payload)
Run with the taiga-mcp venv's python (it provides mcp and httpx). Prints
PASS/FAIL per check, never a token; exit status = number of failures.
"""
import asyncio
import json
import sys
import uuid

import httpx
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

base, seed_path = sys.argv[1].rstrip("/"), sys.argv[2]
token = json.load(open(seed_path))["tokens"]["agent-a"]["token"]
fails = 0


def check(name, cond, detail=""):
    global fails
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else f"  [{detail}]"))
    fails += 0 if cond else 1


async def call(s, name, **args):
    r = await s.call_tool(name, args)
    text = "".join(getattr(c, "text", "") for c in r.content)
    data = r.structuredContent
    if isinstance(data, dict) and set(data) == {"result"}:
        data = data["result"]
    if data is None:
        try:
            data = json.loads(text)
        except ValueError:
            data = {}
    return bool(r.isError), data, text


async def main():
    r = httpx.get(f"{base}/health", timeout=10)
    check("/health 200 without a token", r.status_code == 200, r.status_code)

    r = httpx.post(f"{base}/mcp", json={}, timeout=10)
    check("no token -> 401", r.status_code == 401, r.status_code)
    r = httpx.post(f"{base}/mcp", json={}, timeout=10,
                   headers={"Authorization": "Basic abc"})
    check("non-bearer auth -> 401", r.status_code == 401, r.status_code)

    async with httpx.AsyncClient(headers={"Authorization": f"Bearer {token}"},
                                 timeout=30) as http:
        async with streamable_http_client(f"{base}/mcp", http_client=http) as (rd, wr, _):
            async with ClientSession(rd, wr) as s:
                await s.initialize()
                tools = {t.name for t in (await s.list_tools()).tools}
                check("tool list has 13 tools, no delete",
                      len(tools) == 13 and not any("delete" in t for t in tools), tools)

                err, me, txt = await call(s, "whoami")
                check("whoami as agent-a (default project from options)",
                      not err and me.get("username") == "agent-a"
                      and me.get("project", {}).get("id") == 1, txt)

                subj = f"addon smoke {uuid.uuid4().hex[:8]}"
                err, st, txt = await call(s, "create_story", subject=subj)
                check("create_story", not err and "ref" in st, txt)
                ref = st.get("ref")

                err, ip, txt = await call(s, "set_status", item_type="story",
                                          ref=ref, status_name="In progress")
                check("set_status -> In progress",
                      not err and ip.get("status") == "In progress", txt)

                err, dn, txt = await call(s, "close", item_type="story", ref=ref)
                check("close -> Done, is_closed",
                      not err and dn.get("status") == "Done" and dn.get("is_closed") is True, txt)

    # bad token: Taiga's rejection is reported as a tool error, not a crash
    async with httpx.AsyncClient(headers={"Authorization": "Bearer not-a-token"},
                                 timeout=30) as http:
        async with streamable_http_client(f"{base}/mcp", http_client=http) as (rd, wr, _):
            async with ClientSession(rd, wr) as s:
                await s.initialize()
                err, _, txt = await call(s, "whoami")
                check("bad token rejected by Taiga", err, txt)


asyncio.run(main())
print(f"{fails} failure(s)")
sys.exit(fails)
