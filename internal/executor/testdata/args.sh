#!/usr/bin/env bash
# Prints each argument in brackets, the working directory and the full environment.
for arg in "$@"; do printf '[%s]\n' "$arg"; done
echo "pwd=$(pwd)"
env | sort | sed 's/^/env:/'
