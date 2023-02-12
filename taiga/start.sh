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
export RABBITMQ_DEFAULT_PASS=$(cat /data/options.json | jq -r .rabbitmq_password) 
export POSTGRES_PASSWORD=$(cat /data/options.json | jq -r .postgres_password) 
export POSTGRES_HOST=$(cat /data/options.json | jq -r .postgres_advanced_options.host) 
export POSTGRES_USER=$(cat /data/options.json | jq -r .postgres_advanced_options.user) 
export POSTGRES_DB=$(cat /data/options.json | jq -r .postgres_advanced_options.database) 
export RABBITMQ_VHOST=$(cat /data/options.json | jq -r .rabbitmq_advanced_options.virtual_host) 
export MINUTES_BETWEEN_BACKUPS=$(cat /data/options.json | jq -r .minutes_between_backups) 

SLUG=$(echo $HOSTNAME | sed -e 's/-/_/g')
ADDON_INFO=$(curl  -H "Authorization: Bearer $SUPERVISOR_TOKEN" supervisor/addons/$SLUG/info)
export INGRESS_ENTRY=$(echo $ADDON_INFO | jq -r '.data.ingress_entry')

sed -i "s host_to_be_replaced host_to_be_replaced$INGRESS_ENTRY " /home/taiga/taiga-front-dist/dist/conf.json
sed -i "s base_url_to_be_replaced $INGRESS_ENTRY/ " /home/taiga/taiga-front-dist/dist/conf.json
sed -i 's,base href="/",base href="'"$INGRESS_ENTRY"'/",' /home/taiga/taiga-front-dist/dist/index.html

# ENVIRONMENT="INGRESS_ENTRY='$INGRESS_ENTRY' TAIGA_SECRET_KEY=$TAIGA_SECRET_KEY RABBITMQ_DEFAULT_PASS='$RABBITMQ_DEFAULT_PASS'"
VARIABLES="TAIGA_SECRET_KEY,INGRESS_ENTRY,RABBITMQ_DEFAULT_PASS,POSTGRES_PASSWORD,POSTGRES_HOST,POSTGRES_USER,POSTGRES_DB,RABBITMQ_VHOST,MINUTES_BETWEEN_BACKUPS"
# Carry out db migrations
su -w $VARIABLES  - taiga -c "bash -l /migrate.sh"

# Start the proxy server
nginx

echo "Test log message!" >> /home/taiga/logs/nginx.access.log
echo "Tailing logs..."

# print proxy errors to stdout so they appear in the logs in home assistant
# tail -f  /home/taiga/logs/nginx.access.log |  sed -e 's/^/nginx:: /' &
tail -f /home/taiga/logs/nginx.error.log | sed -e 's/^/nginx:: /' &
# tail -f  /var/log/nginx/access.log |  sed -e 's/^/nginx.root:: /' &
tail -f /var/log/nginx/error.log | sed -e 's/^/nginx.root:: /' &

# Start backup script
mkdir -p /share/$HOSTNAME
chown taiga /share/$HOSTNAME
su -w $VARIABLES - taiga -c "bash -l /backup.sh" &

# Launch Taiga
su -w $VARIABLES - taiga -c "bash -l /run.sh"

wait

