#!/usr/bin/env bash
# Values provider: targets depend on the environment.
case "$1" in
  dev)  echo t-dev-1 ;;
  prod) printf '%s\n' t-prod-1 t-prod-2 ;;
esac
