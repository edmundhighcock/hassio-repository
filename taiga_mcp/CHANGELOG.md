# Changelog

## 0.1.1

- Runs taiga-mcp v0.1.1 (commit da80cb34). The Agent role now has
  `modify_epic`, so agents can link stories to epics; the server still
  reports a clear error if a role lacks it.
- Docs and README examples use the LAN address 192.168.0.36 instead of
  taiga-mcp.lan.

## 0.1.0

- First release. Runs taiga-mcp v0.1.0 (commit f170155d), installed from the
  tag using its uv.lock; the build fails if the tag no longer points at that
  commit.
- Options: `taiga_url`, `project_id`, `log_level`, all optional.
- Port 8095/tcp (plain HTTP, LAN/tailnet only).
