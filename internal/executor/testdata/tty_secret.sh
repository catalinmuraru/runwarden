#!/usr/bin/env bash
# Reads a password; the terminal echoes what is typed.
read -r -p "Password: " pw
echo "got ${#pw} chars"
