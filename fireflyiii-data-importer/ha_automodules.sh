#!/bin/sh
# shellcheck disable=SC2015
set -e

##############################
# Automatic modules download #
##############################

MODULES="$1"
MODULES="$MODULES 00-banner.sh 01-custom_script.sh 01-config_yaml.sh 00-global_var.sh"
echo "To install : $MODULES"

# Fin's change: these used to be downloaded at build time from
# alexbelgium/hassio-addons' master branch (raw.githubusercontent.com),
# which is unpinned. They are now vendored verbatim under
# vendor/.templates/ (see vendor/README.md for the upstream commit they
# were copied from) and COPY'd into the image before this script runs, so
# the build has no network dependency on that repository.
VENDOR_DIR="${VENDOR_DIR:-/vendor/.templates}"

# Install bash if not available
if ! command -v bash > /dev/null 2> /dev/null; then
    (apt-get update && apt-get install -yqq --no-install-recommends bash || apk add --no-cache bash) > /dev/null
fi

# Install curl if not available
if ! command -v curl > /dev/null 2> /dev/null; then
    (apt-get update && apt-get install -yqq --no-install-recommends curl || apk add --no-cache curl) > /dev/null
fi

# Install ca-certificates if not available
(apt-get update && apt-get install -yqq --no-install-recommends ca-certificates || apk add --no-cache ca-certificates) > /dev/null 2>&1 || true

# Create folder for scripts
mkdir -p /etc/cont-init.d

# Install scripts from the vendored copy (was: download from
# alexbelgium/hassio-addons at build time; see note above)
for scripts in $MODULES; do
    echo "$scripts"
    if [ -f "$VENDOR_DIR/$scripts" ] \
        && cp "$VENDOR_DIR/$scripts" /etc/cont-init.d/"$scripts" \
        && [ "$(sed -n '/\/bin/p;q' /etc/cont-init.d/"$scripts")" != "" ]; then
        :
    else
        echo "vendored script missing or invalid: $scripts (expected at $VENDOR_DIR/$scripts)"
        exit 1
    fi
done

chmod -R 755 /etc/cont-init.d
