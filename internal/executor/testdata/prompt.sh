#!/usr/bin/env bash
# Asks two questions on stdin.
printf 'Continue? '
read -r first
echo "first=$first"
printf 'Name? '
read -r second
echo "second=$second"
