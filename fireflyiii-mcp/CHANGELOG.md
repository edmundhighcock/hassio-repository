# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
