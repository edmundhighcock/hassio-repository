# Supervisor 2026.04+ no longer passes BUILD_FROM: name the base explicitly.
# python:3.12-slim-bookworm publishes linux/amd64 and linux/arm64 manifests
# under one tag, so this covers both `arch:` entries with no build.yaml.
ARG PYTHON_IMAGE=python:3.12-slim-bookworm

# ---- build stage: fetch the pinned tag, verify its commit, install from uv.lock
FROM ${PYTHON_IMAGE} AS build
ARG TAIGA_MCP_TAG=v0.1.1
ARG TAIGA_MCP_COMMIT=da80cb346434faed75fc206aa6309812486cffaf
COPY --from=ghcr.io/astral-sh/uv:0.12.6 /uv /usr/local/bin/uv
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never \
    UV_PYTHON=/usr/local/bin/python UV_PROJECT_ENVIRONMENT=/opt/venv
RUN git clone --quiet --depth 1 --branch "${TAIGA_MCP_TAG}" \
        https://github.com/edmundhighcock/taiga-mcp.git /src && \
    test "$(git -C /src rev-parse HEAD)" = "${TAIGA_MCP_COMMIT}" || \
        { echo "taiga-mcp ${TAIGA_MCP_TAG} is not commit ${TAIGA_MCP_COMMIT}; refusing to build" >&2; exit 1; }
# --frozen: install exactly what the tag's uv.lock says (fails if it is out of date).
RUN cd /src && uv sync --frozen --no-dev --no-editable

# ---- runtime stage
FROM ${PYTHON_IMAGE}
COPY --from=build /opt/venv /opt/venv
COPY run.py /usr/local/bin/run.py
RUN useradd --system --no-create-home --uid 10001 --shell /usr/sbin/nologin taigamcp
ENV PATH=/opt/venv/bin:$PATH PYTHONUNBUFFERED=1
# The container starts as root only so run.py can read /data/options.json
# (not guaranteed world-readable); it drops to the unprivileged `taigamcp`
# user before it starts the server. The server itself never runs as root.
CMD ["/opt/venv/bin/python", "/usr/local/bin/run.py"]
