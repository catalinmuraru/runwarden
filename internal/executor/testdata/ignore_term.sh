#!/usr/bin/env bash
# Ignores SIGTERM, so only SIGKILL ends it.
trap '' TERM
echo ready
while true; do sleep 0.1; done
