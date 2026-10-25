#!/bin/bash
# Local test of the static_sites image (amd64 build, run under plain docker).
#   static_sites/tests/test.sh
# Builds the image, then runs it twice against a temporary /config:
#   1. with the real nginx.conf, to prove a non-ingress client is refused;
#   2. with the ingress allow-list opened, to test serving behaviour.
# Exit status is the number of failed checks.
set -u
cd "$(dirname "$0")/.."
IMG=static-sites-test
PORT=${PORT:-18099}
TMP=$(mktemp -d)
FAILS=0
cleanup() { docker rm -f static-sites-test-a static-sites-test-b >/dev/null 2>&1; rm -rf "$TMP"; }
trap cleanup EXIT

ok()   { echo "PASS $*"; }
bad()  { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
hdr()  { curl -s -D - -o /dev/null "$1" | tr -d '\r' | awk -v h="$2" 'tolower($0) ~ "^" tolower(h) ":" {sub(/^[^:]*: */, ""); print}'; }
body() { curl -s "$1"; }
expect() {  # name, want, got
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want '$2', got '$3')"; fi
}

docker build -q --build-arg BUILD_FROM=ghcr.io/home-assistant/amd64-base:3.23 -t $IMG . >/dev/null || { echo "build failed"; exit 99; }

mkdir -p "$TMP/config/sites" && chmod 755 "$TMP" "$TMP/config" "$TMP/config/sites"
sed 's/^\(\s*\)deny all;\s*$/\1# deny all; (test)/; s/allow 172\.30\.32\.2;/allow all;/' \
    rootfs/etc/nginx/nginx.conf > "$TMP/nginx-open.conf"

# 1. Real config: only the Supervisor's ingress gateway may connect.
docker run -d --name static-sites-test-a -p 127.0.0.1:$PORT:8099 -v "$TMP/config:/config" $IMG >/dev/null
sleep 2
expect "non-ingress client refused" 403 "$(code http://127.0.0.1:$PORT/)"
docker rm -f static-sites-test-a >/dev/null

# 2. Open allow-list, to test serving.
docker run -d --name static-sites-test-b -p 127.0.0.1:$PORT:8099 -v "$TMP/config:/config" \
    -v "$TMP/nginx-open.conf:/etc/nginx/nginx.conf:ro" $IMG >/dev/null
sleep 2
U=http://127.0.0.1:$PORT
S="$TMP/config/sites/finances"

expect "front page served"            200 "$(code $U/)"
expect "front page no-cache"          no-cache "$(hdr $U/ Cache-Control)"
expect "site list empty"              "[]" "$(body $U/__sites.json | tr -d '[:space:]')"
expect "internal listing not public"  404 "$(code $U/__sitesdir/)"
expect "unknown site -> placeholder"  200 "$(code $U/finances/)"
body $U/finances/ | grep -q "Nothing has been published" && ok "placeholder text" || bad "placeholder text"
expect "unpublished index.html 404"  404 "$(code -I $U/finances/index.html)"
expect "bare name redirects"          301 "$(code $U/finances)"
expect "redirect is relative"         finances/ "$(hdr $U/finances Location)"

mkdir -p "$S/releases/r1" "$S/releases/r2"
echo '<p>R1</p>' > "$S/releases/r1/index.html"; echo '{"r":1}' > "$S/releases/r1/data.json"
echo 'x' > "$S/releases/r1/app.js"
echo '<p>R2</p>' > "$S/releases/r2/index.html"
ln -s releases/r1 "$S/current"
expect "site list names the site"     finances "$(body $U/__sites.json | grep -o '"name":"[^"]*"' | cut -d'"' -f4)"
echo '{"title":"Finances"}' > "$S/releases/r1/site.json"
expect "published index.html 200"    200 "$(code -I $U/finances/index.html)"
expect "site.json served"             Finances "$(body $U/finances/site.json | grep -o Finances)"
expect "release served"               "<p>R1</p>" "$(body $U/finances/)"
expect "index.html no-cache"          no-cache "$(hdr $U/finances/ Cache-Control)"
expect "json no-cache"                no-cache "$(hdr $U/finances/data.json Cache-Control)"
expect "js not forced no-cache"       "" "$(hdr $U/finances/app.js Cache-Control)"
hdr $U/finances/ Content-Security-Policy | grep -q "default-src 'self'" && ok "CSP present" || bad "CSP present"
expect "missing asset 404"            404 "$(code $U/finances/nope.js)"
expect "releases/ not reachable"      404 "$(code $U/finances/releases/r1/index.html)"
expect "POST refused"                 403 "$(code -X POST $U/finances/)"
expect "path escape refused"          400 "$(code --path-as-is $U/finances/../../etc/passwd)"

ln -s releases/r2 "$S/current.new" && mv -T "$S/current.new" "$S/current"
expect "swap seen without restart"    "<p>R2</p>" "$(body $U/finances/)"
ln -sfn releases/gone "$S/current.new" && mv -T "$S/current.new" "$S/current"
body $U/finances/ | grep -q "Nothing has been published" && ok "dangling current -> placeholder" || bad "dangling current -> placeholder"

expect "vendored echarts served"      200 "$(code $U/vendor/echarts-6.1.0/echarts.min.js)"
hdr $U/vendor/echarts-6.1.0/echarts.min.js Cache-Control | grep -q immutable && ok "vendor long-cached" || bad "vendor long-cached"
expect "no directory listing"         403 "$(code $U/vendor/)"

echo "$FAILS failed"
exit $FAILS
