# FireflyIII MCP Server for Home Assistant

An add-on that provides a Model Context Protocol (MCP) server for FireflyIII, enabling Claude AI to interact with your personal finance data.

## About

This addon runs [LamPyrid](https://github.com/RadCod3/LamPyrid), an MCP server that exposes 22 tools for managing FireflyIII:
- Account management (list, search, retrieve)
- Transaction operations (create, update, delete, bulk operations)
- Budget tracking and analysis

## Prerequisites

1. **FireflyIII instance** - Running locally or remotely (can be another HA addon)
2. **Personal Access Token** - Generated from your FireflyIII instance
3. **Home Assistant OS** - Required for addon support

## Installation

1. Add this repository to Home Assistant
2. Install "FireflyIII MCP Server" from the Add-on Store
3. Configure the addon (see Configuration section)
4. Start the addon

## Configuration

### Required Settings

- **firefly_base_url**: URL of your FireflyIII instance
  - Example: `http://192.168.1.100:8080`
  - Example: `https://firefly.example.com`
- **firefly_token**: Personal access token from FireflyIII
  - Generate in FireflyIII: Profile → OAuth → Personal Access Tokens

### Optional Settings

- **mcp_port**: Port for MCP server (default: 3000)
- **logging_level**: Log verbosity (default: INFO)
  - Options: DEBUG, INFO, WARNING, ERROR, CRITICAL

## Getting Your FireflyIII Token

1. Log into your FireflyIII instance
2. Click your profile icon (top right)
3. Go to "Profile"
4. Select "OAuth" tab
5. Under "Personal Access Tokens", click "Create New Token"
6. Give it a name (e.g., "Home Assistant MCP")
7. Click "Create"
8. Copy the token immediately (you won't see it again)

## Using with Claude

### Claude Desktop

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "firefly": {
      "url": "http://homeassistant.local:3000/mcp"
    }
  }
}
```

Then restart Claude Desktop. The MCP server tools should appear in Claude's available tools.

### Claude Code

Add the MCP server using Claude Code on your laptop:

```bash
claude mcp add --transport http firefly http://homeassistant.local:3000/mcp
```

Replace `homeassistant.local` with your Home Assistant hostname or IP address if needed.

## Security Notes

- **Network Access**: This addon exposes port 3000 on your Home Assistant host
- **No Authentication**: The MCP server has no built-in authentication
- **Keep Local**: Do NOT expose port 3000 to the internet
- **Firewall**: Ensure your firewall blocks external access to this port
- **Token Safety**: Your FireflyIII token has full access to your financial data
- **Trusted Network**: Only use on trusted local networks

## Architecture Support

- ✅ amd64 (Intel/AMD 64-bit)
- ✅ aarch64 (ARM 64-bit, Raspberry Pi 4+)
- ❌ armv7 (older ARM)
- ❌ armhf (ARM hard float)
- ❌ i386 (32-bit Intel)

*Note: Python 3.14 requirement limits architecture support*

## Troubleshooting

### Addon won't start

Check the addon logs for configuration errors:
1. Settings → System → Logs (bottom of page)
2. Look for error messages about firefly_base_url or firefly_token
3. Verify both settings are configured with actual values (not the defaults)

### Can't connect from Claude

1. Verify the addon is running (green "Started" button in addon page)
2. Check the MCP endpoint is accessible:
   ```bash
   curl http://homeassistant.local:3000/mcp
   ```
3. Ensure you're on the same network as Home Assistant
4. If using a hostname, verify it resolves correctly

### 406 Not Acceptable Error

If Claude Code shows "406 Not Acceptable" errors:

1. **Check endpoint path**: Must include `/mcp` at the end
   - ✅ Correct: `http://homeassistant.local:3000/mcp`
   - ❌ Wrong: `http://homeassistant.local:3000`

2. **Verify transport mode**: Must use `--transport http`
   ```bash
   claude mcp add --transport http firefly http://HOST:3000/mcp
   ```

3. **Test endpoint**: Verify the MCP endpoint responds
   ```bash
   curl -X POST http://homeassistant.local:3000/mcp \
     -H "Content-Type: application/json" \
     -H "Accept: application/json, text/event-stream" \
     -d '{"jsonrpc": "2.0", "method": "initialize", "params": {}, "id": 1}'
   ```

### FireflyIII API errors in logs

1. Verify your token is still valid in FireflyIII
2. Check the base URL is correct (include `http://` or `https://`)
3. Test FireflyIII API directly:
   ```bash
   curl -H "Authorization: Bearer YOUR_TOKEN" http://your-firefly/api/v1/about
   ```

## License

Based on LamPyrid by RadCod3 (MIT License)
