#!/bin/bash
# Runs until stopped; reports SIGTERM.
trap 'echo "got TERM"; exit 3' TERM
echo "started"
while true; do sleep 0.1; done
