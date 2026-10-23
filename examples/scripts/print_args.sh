#!/usr/bin/env bash
# Prints its arguments, PARAM_* environment variables and stdin. Used by the example runners.
set -euo pipefail

echo "Arguments ($#):"
for arg in "$@"; do
  printf '  [%s]\n' "$arg"
done

echo "Environment:"
env | grep -E '^(PARAM_|STORAGE_TOKEN=|EXECUTION_ID=)' | sort | sed 's/^/  /' || true

if [ ! -t 0 ]; then
  echo "Stdin:"
  sed 's/^/  /'
fi
