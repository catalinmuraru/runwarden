#!/usr/bin/env bash
# Values provider for the "Instances" parameter: one value per line, depends on the environment.
set -euo pipefail

case "${1:-}" in
  production) printf '%s\n' web-1 web-2 web-3 worker-1 ;;
  staging)    printf '%s\n' web-1 worker-1 ;;
  *)          printf '%s\n' local ;;
esac
