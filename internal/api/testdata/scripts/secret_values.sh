#!/usr/bin/env bash
# Values provider that depends on the environment's secret: one value per secret length.
echo "len-${#DB_PASSWORD}"
echo "$DB_PASSWORD"
