#!/bin/bash
# Prints its arguments, a secret, an uploaded file and one line of input.
echo "args: $*"
echo "secret: $DB_PASSWORD"
echo "file: $(cat "$1" 2>/dev/null)"
read -r line
echo "input: $line"
