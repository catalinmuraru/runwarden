#!/bin/bash
# A dropdown helper: its arguments if any, then one option per line, the last one a secret
# (masked by the executor).
if [ $# -gt 0 ]; then echo "arg: $*"; fi
echo "one"
echo "two"
echo "$DB_PASSWORD"
