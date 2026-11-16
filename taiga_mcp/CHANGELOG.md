# Changelog

## 0.1.0

- First release. Runs taiga-mcp v0.1.0 (commit f170155d), installed from the
  tag using its uv.lock; the build fails if the tag no longer points at that
  commit.
- Options: `taiga_url`, `project_id`, `log_level`, all optional.
- Port 8095/tcp (plain HTTP, LAN/tailnet only).
