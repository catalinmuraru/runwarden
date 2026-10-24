#!/usr/bin/env bash
# Prints each argument in brackets, then the content of the file given with --file.
for arg in "$@"; do printf '[%s]\n' "$arg"; done
echo "user=$RUNWARDEN_USER"
while [ $# -gt 0 ]; do
  if [ "$1" = "--file" ]; then echo "content=$(cat "$2")"; fi
  shift
done
