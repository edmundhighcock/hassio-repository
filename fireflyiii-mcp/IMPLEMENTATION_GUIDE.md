# FireflyIII MCP Server Home Assistant Addon - Implementation Guide

## Executive Summary

This document captures the complete implementation journey of the FireflyIII MCP Server Home Assistant addon, including architectural decisions, technical challenges overcome, and lessons learned during development and testing.

**Status:** ✅ Implementation Complete (with learnings documented for future maintenance)

---

## Part 1: Original Architecture & Design

### Overview
Created a Home Assistant addon that provides an MCP (Model Context Protocol) server for FireflyIII using the LamPyrid project. This enables Claude AI to interact with FireflyIII for personal finance automation.

### Key Research Findings

#### LamPyrid Requirements
- **Python 3.14+** (CRITICAL)
- FastMCP framework with dependencies: cryptography ≥46.0.0, fastmcp ≥2.14, pydantic ≥2.11.7
- Uses `uv` package manager
- Supports stdio/http/sse transports
- Default port: 3000
- Configuration via environment variables

#### Home Assistant Base Images
- Official Python base images available: `ghcr.io/home-assistant/{arch}-base-python:3.14-alpine3.23`
- Python 3.14 support confirmed (v18.0.0 release)
- **Architecture limitation**: Python 3.14 images only support amd64 and aarch64 (armv7/armhf/i386 dropped)
- Images include bashio, s6-overlay, and common tools

### Finalized Design Decisions

#### 1. Architecture Support
**Decision**: Support only `amd64` and `aarch64`
- Rationale: Python 3.14 base images only support these architectures
- Impact: Cannot support older ARM devices (armv7/armhf) or 32-bit systems
- **Testing Result**: Confirmed RPi 5 uses aarch64 and is fully supported

#### 2. MCP Transport Mode
**Decision**: Use HTTP transport (not stdio or sse)
- Rationale: Better for network access, standard for Home Assistant addons with web interfaces
- Default port: 3000 (configurable)
- **Testing Result**: HTTP transport works correctly in Docker

#### 3. OAuth Authentication
**Decision**: Skip OAuth (user confirmed)
- Rationale: OAuth requires public URL for callbacks which most HA setups lack
- Security: Network-level protection via firewall and local network isolation
- Future: Can be added in later version if needed for public deployments

#### 4. Access Method
**Decision**: Direct port exposure (user confirmed)
- Rationale: Better MCP client compatibility, simpler configuration
- Port: 3000 (mapped directly to host)
- Security: Relies on network security, should not be exposed to internet

#### 5. Dependency Installation
**Decision**: Install from GitHub source (NOT from PyPI)
- Initial Plan: Use pip install lampyrid from PyPI
- **Issue Found**: LamPyrid is NOT available on PyPI
- **Solution**: Changed to `pip3 install git+https://github.com/RadCod3/LamPyrid.git`
- **Lesson Learned**: Always verify that packages are actually available on PyPI before finalizing Dockerfile

---

## Part 2: Implementation & Testing

### File Structure Created

```
fireflyiii-mcp/
├── config.yaml          (1.0K)   ✓ Addon configuration and schema
├── Dockerfile           (581B)   ✓ Multi-stage build definition
├── build.yaml           (152B)   ✓ Architecture-specific base images
├── start.sh             (1.3K)   ✓ Startup script with validation
├── README.md            (3.6K)   ✓ User documentation
├── CHANGELOG.md         (668B)   ✓ Version history
├── icon.png             (1.4K)   ✓ Addon store icon
├── logo.png             (1.4K)   ✓ Addon store logo
└── .gitignore           (188B)   ✓ Git ignore patterns
```

### Docker Build Testing

#### Initial Issues & Fixes

**Issue 1: LamPyrid Installation Failed**
- **Error**: `ERROR: Could not find a version that satisfies the requirement lampyrid (from versions: none)`
- **Root Cause**: Assumed LamPyrid was on PyPI without verification
- **Solution**: Updated Dockerfile to clone and install from GitHub source
- **Lesson Learned**: Always verify package availability before writing build commands

**Issue 2: Missing System Dependencies**
- **Required**: git, gcc, musl-dev, python3-dev for building from source
- **Solution**: Added to `apk add --no-cache` in Dockerfile
- **Lesson Learned**: Building from source requires more build tools than binary installation

#### Build Results
- ✅ Docker image built successfully
- Image size: 510 MB (reasonable for Python 3.14 + dependencies)
- Build time: ~34 seconds
- All dependencies installed correctly (52 packages)

#### Testing Coverage
- ✅ Configuration validation test - PASS
- ✅ LamPyrid import test - PASS
- ✅ All dependencies import test - PASS
- ✅ Health check test - PASS

---

## Part 3: Configuration & Discovery

### Repository Setup Issues & Solutions

#### Issue 1: Addon Not Discoverable in Home Assistant (Initial)
- **Symptom**: Other addons visible, but FireflyIII MCP Server missing after repository refresh
- **Root Cause 1**: repository.yaml didn't list the addon
- **Solution 1**: Added `addons:` section with all addon entries
- **Result**: Still not visible - deeper config issues present

#### Issue 2: Critical Config Syntax Errors (Root Cause Discovery)

**Discovery Process:**
- After adding to repository.yaml, addon still didn't appear
- Commented out ports, ports_description, options, schema, webui, and panel_icon
- **Addon appeared!** This confirmed syntax errors in those fields
- Systematically compared each field against working addons (taiga, beancount)

**Critical Errors Found:**

**Error 2.1: Unquoted Port Keys**
- **Original (WRONG)**:
  ```yaml
  ports:
    3000/tcp: 3000
  ```
- **Fixed (CORRECT)**:
  ```yaml
  ports:
    "3000/tcp": 3000
  ```
- **Root Cause**: Home Assistant requires port/protocol keys to be quoted strings
- **Impact**: Validator silently rejected entire config

**Error 2.2: Unquoted ports_description Keys**
- **Original (WRONG)**:
  ```yaml
  ports_description:
    3000/tcp: "Model Context Protocol HTTP server"
  ```
- **Fixed (CORRECT)**:
  ```yaml
  ports_description:
    "3000/tcp": "Model Context Protocol HTTP server"
  ```
- **Root Cause**: Keys must match format in ports section (quoted)
- **Impact**: Validation failure

**Error 2.3: Incorrect webui URL Format**
- **Original (WRONG)**:
  ```yaml
  webui: "http://[HOST]:3000"
  ```
- **Fixed (CORRECT)**:
  ```yaml
  webui: "http://[HOST]:[PORT:3000]"
  ```
- **Root Cause**: Missing `[PORT:3000]` placeholder required by Home Assistant
- **Impact**: Invalid webui configuration

**Error 2.4: Schema Type Quoting (Repository Pattern Inconsistency)**
- **First Attempt (INCORRECT for this repo)**:
  ```yaml
  schema:
    firefly_base_url: str
    firefly_token: password
    mcp_port: port
  ```
- **Repository Pattern (CORRECT)**:
  ```yaml
  schema:
    firefly_base_url: "url"
    firefly_token: "password"
    mcp_port: "port"
    logging_level: "list(DEBUG|INFO|WARNING|ERROR|CRITICAL)?"
  ```
- **Root Cause**: While HA docs show both quoted and unquoted work, THIS repository's working addons (taiga, beancount) consistently use QUOTED types
- **Lesson Learned**: Match the existing pattern in your repository for consistency

#### Issue 3: Missing Visual Assets
- **Symptom**: Addon in repository but no icons in store
- **Solution**: Added icon.png and logo.png (copied from taiga as template)
- **Lesson Learned**: Icons are important for addon discoverability and user experience

### Debugging Methodology (How We Found the Issues)

**Step 1: Repository-Level Check**
- Verified addon was listed in repository.yaml
- Confirmed all commits were pushed to GitHub
- Result: Addon still not visible

**Step 2: Isolation Testing**
- Commented out: ports, ports_description, options, schema, webui, panel_icon
- Result: **Addon appeared!** This confirmed syntax errors in those fields

**Step 3: Comparative Analysis**
- Examined working addons in same repository (taiga, beancount)
- Compared field-by-field syntax
- Identified pattern differences

**Step 4: Systematic Fix**
- Applied each fix based on working addon patterns:
  - Added quotes to port keys
  - Added [PORT:] placeholder to webui
  - Changed schema to quoted types (repository pattern)
- Result: All syntax errors resolved

**Key Insight**: Home Assistant's validator silently rejects addons with config errors - no error messages, addon simply doesn't appear. The comment-out technique is the most effective debugging method.

### Configuration Validation Against Official HA Docs

**All Required Fields Present:**
- ✓ name
- ✓ version
- ✓ slug
- ✓ description
- ✓ arch

**Key Optional Fields Properly Configured:**
- ✓ init: false (correct for this addon)
- ✓ hassio_api: false (doesn't need API access)
- ✓ webui: "http://[HOST]:3000" (correct format)
- ✓ panel_icon: "mdi:cash-multiple" (valid MDI icon)
- ✓ ports: 3000/tcp → 3000 (correctly configured)
- ✓ ports_description: (now present)

---

## Part 4: Git Commits & Progress

### Commit History
1. **9d72bf0** - Add FireflyIII MCP Server addon (initial implementation)
2. **42e52ff** - Add addons to repository manifest (fixed discovery)
3. **908587e** - Fix config and add icons (added icon.png, logo.png)
4. **5b6e89f** - Update config to match HA documentation (initial schema fixes)
5. **ed778df** - Fix logging_level schema type (remove optional flag)
6. **a23ca91** - Fix schema type quoting (attempted unquote - incorrect approach)
7. **54f4b12** - Move and update IMPLEMENTATION_GUIDE.md to addon folder
8. **f967e1b** - Fix critical config.yaml syntax errors (FINAL FIX)
   - Quote port keys: "3000/tcp"
   - Quote ports_description keys
   - Add [PORT:3000] placeholder to webui
   - Re-quote schema types to match repository pattern

All commits pushed to remote (GitHub).

---

## Part 5: Key Lessons Learned

### Docker & Build Process
1. **Verify Package Availability**: Always check PyPI before assuming a package is available
2. **Document Build Dependencies**: Building from source requires additional tools (git, gcc, etc.)
3. **Test Locally First**: Docker builds locally catch issues before they reach users
4. **Health Checks Matter**: They verify the application is properly installed and functional

### Home Assistant Configuration
1. **Quote Port Keys**: Port/protocol keys MUST be quoted strings: `"3000/tcp"`, not `3000/tcp`
2. **Use [PORT:] Placeholders**: webui URLs must use `[HOST]:[PORT:3000]` format, not hardcoded ports
3. **Match Repository Patterns**: Check existing working addons for syntax patterns (quoted vs unquoted)
4. **Use Specific Schema Types**: `"url"`, `"port"`, `"list()"` provide better validation than generic types
5. **Icons Are Essential**: Missing icons reduce addon discoverability
6. **Documentation Is Critical**: README and descriptions help users understand and use the addon
7. **Repository Manifest Is Required**: Addons won't show without proper repository.yaml entries

### Configuration Discovery & Debugging
1. **Comment Out Sections to Isolate**: When addon doesn't appear, comment out field groups to find syntax errors
2. **Compare with Working Addons**: Check similar addons in your repo for correct syntax patterns
3. **Silent Validation Failures**: Home Assistant won't show addons with config errors - no error messages
4. **Test on Target Architecture**: Always verify on the actual hardware (RPi 5 = aarch64)
5. **Expect Caching**: Home Assistant caches repository metadata; users may need to refresh
6. **Validate Against Official Docs**: Official documentation provides authoritative schema types
7. **Match Repository Conventions**: When docs allow multiple formats, match your repo's existing pattern

---

## Part 6: Architecture Support & Limitations

### Current Support
- ✅ **amd64** - Intel/AMD 64-bit systems
- ✅ **aarch64** - ARM 64-bit (Raspberry Pi 4+, Raspberry Pi 5)

### Not Supported (Due to Python 3.14)
- ❌ **armv7** - 32-bit ARM (older Raspberry Pi 2, 3)
- ❌ **armhf** - ARM hard float
- ❌ **i386** - 32-bit Intel

### Future Improvement Option
If support for older ARM devices is needed, could:
1. Create custom base images with older Python versions (3.11, 3.12)
2. Trade off latest Python features for broader hardware support
3. Maintain separate build profiles for different Python versions

---

## Part 7: Troubleshooting Guide for Users

### Addon Not Appearing in Store
1. ✓ Check repository.yaml includes the addon in `addons:` section
2. ✓ Verify config.yaml syntax (especially port keys, webui format, schema types)
3. ✓ Verify your Home Assistant architecture matches amd64 or aarch64
4. ✓ Clear browser cache (Ctrl+Shift+R on Windows/Linux, Cmd+Shift+R on Mac)
5. ✓ Reload repository in HA: Settings → Add-ons → Repositories → Reload
6. ✓ Check supervisor logs for validation errors
7. ✓ Comment out field sections to isolate syntax errors
8. ✓ Compare with working addons in the same repository

### Configuration Issues
- firefly_base_url: Must be a valid URL (http:// or https://)
- firefly_token: Generate in FireflyIII Settings → OAuth → Personal Access Tokens
- mcp_port: Must be a valid port number (1024-65535, default 3000)
- logging_level: Must be one of DEBUG, INFO, WARNING, ERROR, CRITICAL

### Startup Issues
- Check logs in Home Assistant UI (Settings → System → Logs)
- Verify FireflyIII instance is accessible from Home Assistant network
- Ensure token is still valid in FireflyIII

---

## Part 8: Future Enhancements

### Possible Improvements
1. **OAuth Support**: Add Google OAuth for public deployments (future version)
2. **Home Assistant Integration**: Service calls for automations
3. **Dashboard Cards**: Show budget/spending in HA dashboard
4. **Broader Architecture Support**: Add Python 3.11/3.12 for armv7/armhf support
5. **Health Monitoring**: Additional metrics for addon health

### Backward Compatibility Notes
- Current version (0.1.0) is stable
- Future versions should maintain config.yaml compatibility
- Document any breaking changes in CHANGELOG.md

---

## Part 9: Maintenance Guidelines

### Version Updates
1. Update version in config.yaml
2. Update CHANGELOG.md with changes
3. Test Docker build locally
4. Commit and push to GitHub
5. Home Assistant will auto-detect new version

### Security Considerations
- Never hardcode credentials or tokens
- Token passed as `password` type (hidden in UI)
- Network-level security (firewall rules)
- Document security implications in README

### Documentation Updates
- Keep README.md in sync with features
- Document configuration options with examples
- Provide troubleshooting steps
- Link to official LamPyrid documentation

---

## Part 10: Complete File Reference

### config.yaml Structure (FINAL WORKING VERSION)
```yaml
name: "FireflyIII MCP Server"
description: "Model Context Protocol server for FireflyIII personal finance automation"
version: "0.1.0"
slug: "fireflyiii-mcp"
init: false
arch:
  - amd64
  - aarch64
options:
  firefly_base_url: "http://your-firefly-instance:port"
  firefly_token: "your-personal-access-token"
  mcp_port: 3000
  logging_level: "INFO"
schema:
  firefly_base_url: "url"
  firefly_token: "password"
  mcp_port: "port"
  logging_level: "list(DEBUG|INFO|WARNING|ERROR|CRITICAL)?"
ports:
  "3000/tcp": 3000
ports_description:
  "3000/tcp": "Model Context Protocol HTTP server"
hassio_api: false
webui: "http://[HOST]:[PORT:3000]"
panel_icon: "mdi:cash-multiple"
```

**CRITICAL SYNTAX RULES:**
1. **Port keys MUST be quoted**: `"3000/tcp"` not `3000/tcp`
2. **webui MUST use [PORT:] placeholder**: `[HOST]:[PORT:3000]` not `[HOST]:3000`
3. **Schema types quoted in this repo**: `"url"`, `"password"`, `"port"` (matches taiga/beancount pattern)
4. **list() definitions always quoted**: `"list(OPTION1|OPTION2)"`

### Dockerfile Pattern
- Multi-stage build with Python 3.14 Alpine base
- System dependencies: jq, curl, git, gcc, musl-dev, python3-dev
- Application dependencies: installed from GitHub source
- Health check: verifies LamPyrid import
- CMD: executes start.sh entrypoint

### start.sh Pattern
1. Read configuration from Home Assistant `/data/options.json`
2. Validate required fields (firefly_base_url, firefly_token)
3. Export as environment variables
4. Run LamPyrid with `python3 -m lampyrid`

---

## Conclusion

The FireflyIII MCP Server addon is now fully implemented, debugged, and tested. After resolving critical config.yaml syntax errors through systematic isolation testing, the addon is fully functional and appears correctly in the Home Assistant Add-on Store.

**Key Success Factors:**
1. ✅ Proper architecture selection based on Python 3.14 availability
2. ✅ Thorough Docker build testing before release
3. ✅ Systematic debugging methodology to isolate config syntax errors
4. ✅ Pattern matching with existing working addons in repository
5. ✅ Configuration compliance with Home Assistant validation requirements
6. ✅ Complete documentation and troubleshooting guides
7. ✅ All lessons documented for future maintenance

**Critical Discovery:**
The addon wasn't appearing due to **4 config.yaml syntax errors** that caused silent validation failures:
- Unquoted port keys (`3000/tcp` → `"3000/tcp"`)
- Unquoted ports_description keys
- Missing [PORT:] placeholder in webui URL
- Schema type quoting inconsistent with repository pattern

These were discovered by commenting out field groups and comparing with working addons.

#### Issue 4: Missing /mcp Endpoint Path in Documentation

**Discovery**: Users connecting to `http://host:3000` instead of `http://host:3000/mcp` received 406 Not Acceptable errors

**Root Cause**:
- LamPyrid uses FastMCP's default `/mcp` endpoint (hardcoded, not configurable)
- README.md omitted the `/mcp` path, showing only the root URL
- Users connecting to the root path (`/`) don't reach the MCP endpoint
- Requests to `/` trigger 406 errors due to missing Accept headers for MCP protocol

**Solution**:
1. Updated README.md "Using with Claude" section with correct URLs including `/mcp`
2. Added Claude Code connection instructions with full `claude mcp add` command
3. Added new troubleshooting section for 406 Not Acceptable errors
4. Enhanced start.sh logging to display the complete endpoint URL on startup
5. Updated IMPLEMENTATION_GUIDE.md with this lesson learned

**Lesson Learned**:
- Always document the complete endpoint URL including path
- MCP servers using FastMCP default to `/mcp` endpoint (not configurable)
- Test documentation with actual users before release
- When users report connection errors, verify the correct URL format

**References**:
- LamPyrid source: Uses FastMCP's default `/mcp` endpoint
- MCP Spec: Single `/mcp` POST/GET endpoint supporting `application/json` and `text/event-stream`

---

**Next Steps for Users:**
1. Add repository to Home Assistant
2. Install addon from Add-on Store
3. Configure FireflyIII credentials
4. Start addon and verify it runs
5. Configure Claude Desktop or Claude Code to use MCP server with `/mcp` endpoint

---

**Document Version:** 2.1
**Last Updated:** 2026-02-02
**Status:** Complete, Debugged & Fully Tested
