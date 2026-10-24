#!/usr/bin/env bash
# Prints output, writes a file and a symlink, and exits with code 2.
echo hello
echo "file body" > out.txt
ln -sf /etc/hostname link.txt
exit 2
