# Supervisor 2026.04+ no longer passes BUILD_FROM: name the base explicitly.
# influxdb:2.7.12 publishes both linux/amd64 and linux/arm64/v8 manifests
# under the same tag (verified on Docker Hub), so this one line covers both
# entries in config.yaml's `arch:` list with no per-arch build.yaml.
FROM influxdb:2.7.12

# jq: used by run.sh to read add-on options from /data/options.json.
RUN apt-get update && \
    apt-get install -y --no-install-recommends jq && \
    rm -rf /var/lib/apt/lists/*

COPY rootfs/ /
RUN chmod a+x /run.sh /usr/local/bin/influxdb2-backup.sh

# Replace the upstream entrypoint.sh/influxd default with our own run.sh,
# which sets up secrets and the backup loop, then calls /entrypoint.sh
# itself (still present in the image, unmodified) to do setup and run influxd.
ENTRYPOINT []
CMD ["/run.sh"]
