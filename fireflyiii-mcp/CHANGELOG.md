# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.9-pRuleGroups-dev] - 2026-08-11

### Added
- **Rule-group management MCP tools**: `list_rule_groups`, `create_rule_group`,
  `update_rule_group`. Rule groups run in ascending order at store-journal time,
  so these tools allow placing budget-assignment rules in a group that runs
  after all category-assignment rules (fixes budget tagging missing for
  transactions categorised by later rules).
- Simplified `Rule` responses now include `rule_group_id` / `rule_group_title`.

## [0.1.8-pAccounts-dev] - 2026-06-01

### Added
- **Account management MCP tools**: new `create_account` and `update_account` tools
  exposed by LamPyrid. The latter is the primary motivation — it allows setting
  `opening_balance` and `opening_balance_date` on existing accounts via PUT
  `/api/v1/accounts/{id}`, which was previously a UI-only operation. Use case:
  journalling the opening principal of a loan that was set up in Firefly without
  one.
- Pinned to LamPyrid `feat/post-pr-work` commit `eb71c7f` or later.

## [0.1.1-pRules-dev] - 2026-02-02

### Fixed
- **Documentation**: Corrected MCP endpoint URLs to include `/mcp` path in all connection examples
- **Troubleshooting**: Added 406 Not Acceptable error troubleshooting guide with diagnostic steps
- **Logging**: Enhanced startup logs to show complete MCP endpoint URL and connection instructions

### Changed
- Clarified Claude Code connection instructions with full `claude mcp add` command examples
- Improved README.md with explicit endpoint path documentation

## [0.1.0] - 2025-02-01

### Added
- Initial release of FireflyIII MCP Server addon
- Support for amd64 and aarch64 architectures
- HTTP transport mode for MCP server
- FireflyIII personal access token configuration
- Configurable MCP port (default: 3000)
- Configurable logging level
- Configuration validation with helpful error messages
- Health check for container monitoring
- Comprehensive user documentation
