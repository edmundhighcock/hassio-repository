#!/bin/bash


echo Hello Rabbits 

export RABBITMQ_DEFAULT_PASS=$(cat /data/options.json | jq -r .password) 

rabbitmq-server
