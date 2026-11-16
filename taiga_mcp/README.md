# Taiga MCP Server

MCP server (streamable HTTP) for household AI agents to manage Taiga stories
and tasks. Pass-through auth: every client sends its own Taiga token as a
bearer token; the add-on stores none. Plain HTTP on port 8095, LAN/tailnet
only - never port-forward. See DOCS.md.
