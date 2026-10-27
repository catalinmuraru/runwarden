#!/usr/bin/env bash
# A dropdown helper: prints what it can read (shared workspace) and what it cannot (data dir).
cat "$RUNWARDEN_SHARED/tool" 2>/dev/null || echo "shared:blocked"
cat "$1" 2>/dev/null || echo "data:blocked"
