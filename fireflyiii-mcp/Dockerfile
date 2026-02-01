ARG BUILD_FROM
FROM ${BUILD_FROM}

# Install system dependencies including git and build tools
RUN apk add --no-cache \
    jq \
    curl \
    git \
    gcc \
    musl-dev \
    python3-dev

# Install LamPyrid from custom fork with rules management feature
RUN pip3 install --no-cache-dir \
    git+https://github.com/edmundhighcock/LamPyrid.git@feat/add-rules-management

# Copy start script
COPY start.sh /
RUN chmod a+x /start.sh

# Health check - verify lampyrid can be imported
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s \
    CMD python3 -c "import lampyrid; print('OK')" || exit 1

CMD ["/start.sh"]
