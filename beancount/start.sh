#!usr/bin/with-contenv bashio

echo SUPERVISOR_TOKEN $SUPERVISOR_TOKEN
SLUG=$(echo $HOSTNAME | sed -e 's/-/_/g')
echo SLUG $SLUG
ADDON_INFO=$(curl -sSL -H "Authorization: Bearer $SUPERVISOR_TOKEN" http://supervisor/addons/$SLUG/info)
echo ADDON_INFO $ADDON_INFO
export INGRESS_ENTRY=$(echo $ADDON_INFO | jq -r '.data.ingress_entry')
export FAVA_HOST="0.0.0.0"
export PYTHONPATH=/usr/local/lib/python3.11/site-packages

sed -i "s path_to_be_replaced $INGRESS_ENTRY " /etc/nginx/conf.d/taiga.conf
cat /etc/nginx/conf.d/taiga.conf

# Start the proxy server
nginx

echo "Test log message!" >> /home/taiga/logs/nginx.access.log
echo "Tailing logs..."

# print proxy errors to stdout so they appear in the logs in home assistant
# tail -f  /home/taiga/logs/nginx.access.log |  sed -e 's/^/nginx:: /' &
tail -f /var/nginx.error.log | sed -e 's/^/nginx:: /' &
# tail -f  /var/log/nginx/access.log |  sed -e 's/^/nginx.root:: /' &
tail -f /var/error.log | sed -e 's/^/nginx.root:: /' &

fava --prefix  "$INGRESS_ENTRY"  -d /tmp/example.beancount
