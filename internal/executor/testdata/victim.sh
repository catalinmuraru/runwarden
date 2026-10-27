#!/usr/bin/env bash
# A run holding secrets: an env variable (from the test), files in its HOME and its workspace.
echo "bob-token" > "$HOME/token"
echo "bob-notes" > "$RUNWARDEN_WORKSPACE/notes"
echo "ready pid=$$"
exec sleep 30
