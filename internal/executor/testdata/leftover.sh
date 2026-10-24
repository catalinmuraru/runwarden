#!/usr/bin/env bash
# Exits while a background child still holds the output pipe.
sleep 300 &
echo "child=$!"
