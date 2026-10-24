#!/usr/bin/env bash
# Prints 1 MiB of output, then a final line.
head -c 1048576 /dev/zero | tr '\0' 'x'
echo
echo done
