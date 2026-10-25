#!/usr/bin/env bash
echo "tmp=$TMPDIR"
touch "$TMPDIR/scratch" && echo "writable"
