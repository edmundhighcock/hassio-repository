#============================#
#  ALEXBELGIUM'S DOCKERFILE  #
#============================#
#           _.------.
#       _.-`    ('>.-`"""-.
# '.--'`       _'`   _ .--.)
#    -'         '-.-';`   `
#    ' -      _.'  ``'--.
#        '---`    .-'""`
#               /`
#=== Home Assistant Addon ===#
#
# Forked by Fin (Edmund's financial-advisor agent) from alexbelgium's
# fireflyiii_data_importer add-on (https://github.com/alexbelgium/hassio-addons).
# Changes from the upstream recipe, relative to alexbelgium's original
# Dockerfile for this add-on, are confined to the "0 Account-scoped dedup
# patch" section below plus the BUILD_FROM pin passed in via build.yaml;
# everything else — the module/app download machinery, the entrypoint,
# the labels, the healthcheck — is alexbelgium's unchanged.

#################
# 1 Build Image #
#################

ARG BUILD_FROM
ARG BUILD_VERSION
ARG BUILD_UPSTREAM="2.3.5"
FROM ${BUILD_FROM}

##################
# 2 Modify Image #
##################

USER root

# Set S6 wait time
ENV S6_CMD_WAIT_FOR_SERVICES=1 \
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 \
    S6_SERVICES_GRACETIME=0

#####################################
# 0 Account-scoped dedup patch #
#####################################
# Fin's addition: apply a git-diff patch against upstream tag v2.3.5
# (paths a/app/..., b/config/...) to the application tree the base image
# ships at /var/www/html, BEFORE any of alexbelgium's own modifications
# run, so later steps operate on the already-patched tree.
#
# patches/account-scoped-dedup.patch is produced and reviewed outside this
# Dockerfile. An EMPTY file (the placeholder committed alongside this
# Dockerfile) means "no patch yet" and the step is skipped, so the image
# still builds. Once a real patch lands, this becomes a hard gate: `patch`
# exits non-zero on a hunk that does not apply cleanly, and under this
# shell's default errexit that fails the build rather than degrading
# silently. --forward guards against re-applying (or applying a reversed
# version of) an already-applied patch.
RUN if ! command -v patch > /dev/null 2>&1; then \
        (apt-get update && apt-get install -yqq --no-install-recommends patch && rm -rf /var/lib/apt/lists/*) \
        || apk add --no-cache patch; \
    fi

COPY patches/account-scoped-dedup.patch /tmp/account-scoped-dedup.patch
RUN if [ -s /tmp/account-scoped-dedup.patch ]; then \
        echo "Applying patches/account-scoped-dedup.patch to /var/www/html" && \
        patch -p1 --forward -d /var/www/html < /tmp/account-scoped-dedup.patch; \
    else \
        echo "patches/account-scoped-dedup.patch is empty (placeholder) -- skipping patch step"; \
    fi && \
    rm -f /tmp/account-scoped-dedup.patch

#ENV FIREFLY_III_PATH="/data/firefly"
#WORKDIR $FIREFLY_III_PATH

#RUN \
#    mkdir -p /scripts && \
#    mv /etc/entrypoint.d/11-execute-things.sh /scripts/

#RUN \
#    # Change data location
#    grep -rl "/var/www/html" /etc/ | xargs sed -i 's|/var/www/html|/data/firefly|g'

##################
# 3 Install apps #
##################

# Copy local files
COPY rootfs/ /
RUN find . -type f \( -name "*.sh" -o -name "run" -o -name "finish" \) -print -exec chmod +x {} \; \
    && chmod 0755 /usr/local/bin/importer-locked-run

# Uses /bin for compatibility purposes
# hadolint ignore=DL4005
RUN if [ ! -f /bin/sh ] && [ -f /usr/bin/sh ]; then ln -s /usr/bin/sh /bin/sh; fi && \
    if [ ! -f /bin/bash ] && [ -f /usr/bin/bash ]; then ln -s /usr/bin/bash /bin/bash; fi

# Modules
ARG MODULES="00-banner.sh 01-custom_script.sh 00-global_var.sh 01-config_yaml.sh"

# Automatic modules download
COPY ha_automodules.sh /ha_automodules.sh
RUN chmod 744 /ha_automodules.sh && /ha_automodules.sh "$MODULES" && rm /ha_automodules.sh

# Manual apps
ENV PACKAGES="cron bash sudo"

# Automatic apps & bashio
COPY ha_autoapps.sh /ha_autoapps.sh
RUN chmod 744 /ha_autoapps.sh && /ha_autoapps.sh "$PACKAGES" && rm /ha_autoapps.sh

################
# 4 Entrypoint #
################

# Add entrypoint
#ENV S6_STAGE2_HOOK=/ha_entrypoint.sh
COPY ha_entrypoint.sh /ha_entrypoint.sh
RUN chmod 777 /ha_entrypoint.sh

# Install bashio
COPY bashio-standalone.sh /usr/local/lib/bashio-standalone.sh
RUN chmod 0755 /usr/local/lib/bashio-standalone.sh

# Adapt shebang
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN \
    # Run as www-data
    sed -i "s|/usr|exec sudo -Eu www-data /usr|g" /etc/s6*/s6*/php-fpm/run && \
    # Move execute things
    mkdir -p /scripts && \
    mv /etc/entrypoint.d/11-execute-things.sh /scripts/ && \
    # Allow passwordless root
    echo "www-data ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers && \
    # Change output lots
    sed -i "s|/proc/self/fd/2|/log_php|g" /etc/entrypoint.d/1-log-output-level.sh && \
    grep -rl "/proc/self/fd/2" /usr/local/etc | while read -r files; do sed -i "s|/proc/self/fd/2|/log_php|g" "$files"; done && \
    touch /log_php && chown www-data:www-data /log_php && \
    # Adapt files
    for files in /etc/cont-init.d/*; do sed -i "s|/usr/bin/with-contenv bashio|/usr/bin/env bashio|g" "$files"; done
    #sed -i "s|/command/with-contenv bashio|/bin/bash bashio|g" /ha_entrypoint.sh && \

    # Execute entrypoint
    #sed -i "s|/usr/bin/env sh|/usr/bin/env bashio|g" /etc/entrypoint.d/11-execute-things.sh && \
    #chmod 777 /ha_entrypoint.sh && \
    #sed -i "1a sudo /./ha_entrypoint.sh" /etc/entrypoint.d/11-execute-things.sh
#RUN cat /ha_entrypoint.sh && \
#    sed -i "1i \#\!/bin/bash" /ha_entrypoint.sh

# Entrypoint
#RUN sed -i "1a \#\!/bin/bash bashio" /ha_entrypoint.sh
#RUN echo "www-data ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers && \
#    chown -R www-data:www-data /etc/cont-init.d/* && \
#    chmod 777 /ha_entrypoint.sh && \
#    sed -i "s| sh| bashio|g" /etc/entrypoint.d/11-execute-things.sh && \
#    sed -i "1a source /ha_entrypoint.sh" /etc/entrypoint.d/11-execute-things.sh

#ENV ha_entry_source="true"

#USER www-data

#ENTRYPOINT [ "/usr/bin/env" ]
#CMD [ "/ha_entrypoint.sh" ]

############
# 5 Labels #
############

ARG BUILD_ARCH
ARG BUILD_DATE
ARG BUILD_DESCRIPTION
ARG BUILD_NAME
ARG BUILD_REF
ARG BUILD_REPOSITORY
ARG BUILD_VERSION
ENV BUILD_VERSION="${BUILD_VERSION}"
LABEL \
    io.hass.name="${BUILD_NAME}" \
    io.hass.description="${BUILD_DESCRIPTION}" \
    io.hass.arch="${BUILD_ARCH}" \
    io.hass.type="addon" \
    io.hass.version=${BUILD_VERSION} \
    maintainer="alexbelgium (https://github.com/alexbelgium)" \
    org.opencontainers.image.title="${BUILD_NAME}" \
    org.opencontainers.image.description="${BUILD_DESCRIPTION}" \
    org.opencontainers.image.vendor="Home Assistant Add-ons" \
    org.opencontainers.image.authors="alexbelgium (https://github.com/alexbelgium)" \
    org.opencontainers.image.licenses="MIT" \
    org.opencontainers.image.url="https://github.com/alexbelgium" \
    org.opencontainers.image.source="https://github.com/${BUILD_REPOSITORY}" \
    org.opencontainers.image.documentation="https://github.com/${BUILD_REPOSITORY}/blob/main/README.md" \
    org.opencontainers.image.created=${BUILD_DATE} \
    org.opencontainers.image.revision=${BUILD_REF} \
    org.opencontainers.image.version=${BUILD_VERSION}

#################
# 6 Healthcheck #
#################

# Avoid spamming logs
# hadolint ignore=SC2016
RUN \
    # Handle Apache configuration
    if [ -d /etc/apache2/sites-available ]; then \
        for file in /etc/apache2/sites-*/*.conf; do \
            sed -i '/<VirtualHost/a \ \n    # Match requests with the custom User-Agent "HealthCheck" \n    SetEnvIf User-Agent "HealthCheck" dontlog \n    # Exclude matching requests from access logs \n    CustomLog ${APACHE_LOG_DIR}/access.log combined env=!dontlog' "$file"; \
        done; \
    fi && \
    \
    # Handle Nginx configuration
    if [ -f /etc/nginx/nginx.conf ]; then \
        awk '/http \{/{print; print "map $http_user_agent $loggable {\n  default 1;\n  \"~*HealthCheck\" 0;\n}\naccess_log /var/log/nginx/access.log combined if=$loggable;"; next}1' /etc/nginx/nginx.conf > /etc/nginx/nginx.conf.new && \
        mv /etc/nginx/nginx.conf.new /etc/nginx/nginx.conf; \
    fi

ENV HEALTH_PORT="8080" \
    HEALTH_URL="/healthcheck"
HEALTHCHECK \
    --interval=5s \
    --retries=5 \
    --start-period=30s \
    --timeout=25s \
    CMD curl -A "HealthCheck: Docker/1.0" -s -f "http://127.0.0.1:${HEALTH_PORT}${HEALTH_URL}" &>/dev/null || exit 1
