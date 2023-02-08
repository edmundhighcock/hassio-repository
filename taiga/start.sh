#!/usr/bin/env bash

# bashio::log.info "Starting Taiga migrations."
# export TAIGA_SECRET_KEY=$(bashio::config 'taiga_secret_key')
# echo bashio::addon.config $(bashio::addon.config)


STATIC_DIR=/home/taiga/taiga-back/static
# Setup data dirs
if ! [[ -L $STATIC_DIR ]] # if it is already a symlink take no action
then
  rm -r $STATIC_DIR
  mkdir /data/taiga-static
  ln -s /data/taiga-static $STATIC_DIR
  chown taiga /data/taiga-static
fi
MEDIA_DIR=/home/taiga/taiga-back/media
# Setup data dirs
if ! [[ -L $MEDIA_DIR ]]
then
  rm -r $MEDIA_DIR
  mkdir /data/taiga-media
  ln -s /data/taiga-media $MEDIA_DIR
  chown taiga /data/taiga-media
fi

export TAIGA_SECRET_KEY=$(cat /data/options.json | jq -r .taiga_secret_key) 

SLUG=$(echo $HOSTNAME | sed -e 's/-/_/g')
ADDON_INFO=$(curl  -H "Authorization: Bearer $SUPERVISOR_TOKEN" supervisor/addons/$SLUG/info)
INGRESS_ENTRY=$(echo $ADDON_INFO | jq -r '.data.ingress_entry')

sed -i "s host_to_be_replaced host_to_be_replaced$INGRESS_ENTRY " /home/taiga/taiga-front-dist/dist/conf.json
sed -i "s base_url_to_be_replaced $INGRESS_ENTRY/ " /home/taiga/taiga-front-dist/dist/conf.json
sed -i 's,base href="/",base href="'"$INGRESS_ENTRY"'/",' /home/taiga/taiga-front-dist/dist/index.html

# Carry out db migrations
su - taiga -c "INGRESS_ENTRY='$INGRESS_ENTRY' TAIGA_SECRET_KEY=$TAIGA_SECRET_KEY bash -l /migrate.sh"

# Start the proxy server
nginx

echo "Test log message!" >> /home/taiga/logs/nginx.access.log
echo "Tailing logs..."

# print proxy errors to stdout so they appear in the logs in home assistant
# tail -f  /home/taiga/logs/nginx.access.log |  sed -e 's/^/nginx:: /' &
tail -f /home/taiga/logs/nginx.error.log | sed -e 's/^/nginx:: /' &
# tail -f  /var/log/nginx/access.log |  sed -e 's/^/nginx.root:: /' &
tail -f /var/log/nginx/error.log | sed -e 's/^/nginx.root:: /' &


# Launch Taiga
su - taiga -c "INGRESS_ENTRY='$INGRESS_ENTRY' TAIGA_SECRET_KEY=$TAIGA_SECRET_KEY bash -l /run.sh"

wait

