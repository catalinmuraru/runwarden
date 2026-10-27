#!/usr/bin/env bash
tty
stty size
echo "via /dev/tty" > /dev/tty
echo "terminal ok"
