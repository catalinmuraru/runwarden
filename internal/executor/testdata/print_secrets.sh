#!/usr/bin/env bash
# Prints values a redaction pattern must hide, one split across two writes.
echo "token=ghp_abcdef123456"
printf 'second token=ghp_'
sleep 0.05
printf 'zyx987\n'
echo "harmless line"
