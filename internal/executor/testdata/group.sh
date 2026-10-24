#!/usr/bin/env bash
# Starts a background child and waits for it, so stopping must reach the whole group.
sleep 300 &
echo "child=$!"
echo "self=$$"
wait
