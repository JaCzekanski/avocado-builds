#!/bin/sh

git fetch && git reset --hard origin/master
docker-compose up -d --build
