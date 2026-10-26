#!/usr/bin/env bash
# Values provider: targets depend on the environment.
[ -n "${COUNT_FILE:-}" ] && echo call >> "$COUNT_FILE"
case "$1" in
  dev)  echo t-dev-1 ;;
  prod) printf '%s\n' t-prod-1 t-prod-2 ;;
esac
