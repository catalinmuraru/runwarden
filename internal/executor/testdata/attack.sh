#!/usr/bin/env bash
# Tries what another user's run must not be able to do, plus what every run must be able to do.
# Usage: attack.sh <victim pid> <victim HOME> <base dir>. Prints attack:<name>=open|blocked.
pid=$1 home=$2 base=$3
try() {
	local name=$1
	shift
	if "$@" >/dev/null 2>&1; then echo "attack:$name=open"; else echo "attack:$name=blocked"; fi
}
try other-home cat "$home/token"
try other-environ grep -q BOB_SECRET "/proc/$pid/environ"
try other-mem head -c1 "/proc/$pid/mem"
try other-signal kill -0 "$pid"
try data-dir cat "$base/data/history.jsonl"
try secrets cat "$base/secrets/prod/DB_PASSWORD"
try other-workspace-read cat "$base/ws/bob/notes"
try other-workspace-write touch "$base/ws/bob/evil"
try shared-write touch "$base/ws/shared/evil"
try own-home sh -c 'echo x > "$HOME/f" && cat "$HOME/f"'
try own-tmp sh -c 'echo x > "$TMPDIR/f" && cat "$TMPDIR/f"'
try own-workspace sh -c 'echo x > "$RUNWARDEN_WORKSPACE/f" && cat "$RUNWARDEN_WORKSPACE/f"'
try shared-read cat "$RUNWARDEN_SHARED/tool"
try process-substitution bash -c 'cat <(echo hi)'
try own-children bash -c 'sleep 5 & kill $!'
echo "attacks done"
