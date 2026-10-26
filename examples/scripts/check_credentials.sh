#!/usr/bin/env bash
# Shows which secrets the run received without revealing them: length and a short fingerprint.
# Printing a value directly is safe too: Runwarden masks every secret in the output.
set -euo pipefail

for name in DB_PASSWORD API_TOKEN SOURCE_DB_PASSWORD; do
  value="${!name-}"
  if [ -z "$value" ]; then
    echo "$name: not given"
    continue
  fi
  fingerprint=$(printf '%s' "$value" | sha256sum | cut -c1-12)
  echo "$name: ${#value} characters, fingerprint $fingerprint"
done
echo "printed as is: ${DB_PASSWORD-}"
