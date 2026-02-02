#!/usr/bin/with-contenv bashio

bashio::log.info "Starting FireflyIII MCP Server..."

# Read configuration from Home Assistant
export FIREFLY_BASE_URL=$(bashio::config 'firefly_base_url')
export FIREFLY_TOKEN=$(bashio::config 'firefly_token')
export MCP_PORT=$(bashio::config 'mcp_port')
export LOGGING_LEVEL=$(bashio::config 'logging_level')

# Set MCP server transport mode
export MCP_TRANSPORT="http"
export MCP_HOST="0.0.0.0"

# Validate required configuration
if [[ -z "$FIREFLY_BASE_URL" ]] || [[ "$FIREFLY_BASE_URL" == "http://your-firefly-instance:port" ]]; then
    bashio::log.fatal "Please set firefly_base_url in the addon configuration!"
    bashio::log.fatal "Example: http://192.168.1.100:8080 or https://firefly.example.com"
    exit 1
fi

if [[ -z "$FIREFLY_TOKEN" ]] || [[ "$FIREFLY_TOKEN" == "your-personal-access-token" ]]; then
    bashio::log.fatal "Please set firefly_token in the addon configuration!"
    bashio::log.fatal "Generate a token in FireflyIII: Profile → OAuth → Personal Access Tokens"
    exit 1
fi

bashio::log.info "Configuration validated successfully"
bashio::log.info "FireflyIII URL: ${FIREFLY_BASE_URL}"
bashio::log.info "MCP Server Port: ${MCP_PORT}"
bashio::log.info "Log Level: ${LOGGING_LEVEL}"
bashio::log.info "MCP Endpoint: http://<HOME_ASSISTANT_HOST>:${MCP_PORT}/mcp"
bashio::log.info "Connect from Claude Code on your laptop:"
bashio::log.info "  claude mcp add --transport http firefly http://homeassistant.local:${MCP_PORT}/mcp"
bashio::log.info "Starting LamPyrid MCP server..."

# Run LamPyrid
exec python3 -m lampyrid
