# Taiga MCP Server

An [MCP](https://modelcontextprotocol.io) server (streamable HTTP) that lets
household AI agents create, find, update, move and close Taiga 6 user stories
and tasks. It runs [taiga-mcp](https://github.com/edmundhighcock/taiga-mcp)
v0.1.0, installed from that tag (commit `f170155d`) with the dependency
versions locked in its `uv.lock`. There are no delete tools. See the server's
README for the tool list.

## Authentication: pass-through

The add-on holds **no credentials**. Each agent sends its own Taiga
application token:

    Authorization: Bearer <that agent's Taiga application token>

and the server forwards it to Taiga for that one request. Consequences:

- A request with no bearer token gets HTTP 401. There is no anonymous access.
  `GET /health` is the only open path (used by the watchdog).
- Taiga enforces permissions per agent and its history names the acting agent.
- Tokens are never logged, at any log level.
- Tokens are minted per agent by an administrator, with the scripts in the
  `ham` repo: `/home/edmundhighcock/Code/ham/services/taiga-agent-access/`
  (`create_agent_role.py`, `create_agent_user.py`). Do not share a token
  between agents.
- The Taiga **"Agent" role cannot delete** anything (no `delete_*`
  permissions), nor administer the project or link epics. This is Taiga's
  enforcement, not this add-on's.

## Network

Port **8095/tcp** is published on the host (default 8095) and is **plain
HTTP**. It is for the LAN and tailnet only and **must never be
port-forwarded** to the internet: bearer tokens travel unencrypted. Other
add-ons can reach it at `http://9547a9e0-taiga-mcp:8095` over the Supervisor
network whatever the host mapping. The MCP endpoint is `/mcp`.

## Options

All options are optional and have defaults.

| Option | Default | Meaning |
| --- | --- | --- |
| `taiga_url` | `http://9547a9e0-taiga:8001` | Taiga back-end base URL (the Taiga add-on's internal hostname, gunicorn port). |
| `project_id` | `1` | Default project id ("Life"); tools can name another. |
| `log_level` | `info` | `debug`, `info`, `warning` or `error`. |

## Client example

Keep the token in a file readable only by you
(`chmod 600 ~/.config/taiga/agent.token`). `.mcp.json` for Claude Code:

```json
{
  "mcpServers": {
    "taiga": {
      "type": "http",
      "url": "http://taiga-mcp.lan:8095/mcp",
      "headersHelper": "sh -c 'printf \"{\\\"Authorization\\\": \\\"Bearer %s\\\"}\" \"$(cat ~/.config/taiga/agent.token)\"'"
    }
  }
}
```

If your Claude Code lacks `headersHelper`, use
`"headers": {"Authorization": "Bearer ${TAIGA_TOKEN}"}` and start with
`TAIGA_TOKEN=$(cat ~/.config/taiga/agent.token) claude`.

## Updating the pinned server version

Change `TAIGA_MCP_TAG` and `TAIGA_MCP_COMMIT` together in the Dockerfile. The
build fails if the tag does not resolve to that commit, and installs with
`uv sync --frozen` so the tag's `uv.lock` pins hold.

## Tests

`tests/test.sh` builds the image for amd64, starts taiga-mcp's real Taiga test
stack, runs the add-on against it and drives it with a real MCP client. See the
script header.
