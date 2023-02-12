#!/bin/bash


echo Hello Rabbits 

export RABBITMQ_DEFAULT_PASS=$(cat /data/options.json | jq -r .password) 

rabbitmq-server &

rabbitmqctl wait -P $!

rabbitmqctl add_vhost taiga_local
rabbitmqctl set_permissions -p taiga_local taiga ".*" ".*" ".*"

wait
