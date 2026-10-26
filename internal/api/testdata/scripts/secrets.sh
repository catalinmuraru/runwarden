#!/usr/bin/env bash
# Prints the secrets it received (the run output masks their values) and their lengths.
echo "db=${DB_PASSWORD-unset} len=${#DB_PASSWORD}"
echo "source=${SOURCE_DB_PASSWORD-unset} len=${#SOURCE_DB_PASSWORD}"
echo "token=${PRIVATE_TOKEN-unset} other=${OTHER_SECRET-unset}"
