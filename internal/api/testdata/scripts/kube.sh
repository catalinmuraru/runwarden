#!/usr/bin/env bash
# Prints the cluster the run is pinned to (no kubectl needed): context variables, file mode,
# current context and servers of $KUBECONFIG.
sleep "${WAIT:-0}"
echo "context=$RUNWARDEN_KUBE_CONTEXT source=${RUNWARDEN_KUBE_SOURCE_CONTEXT-unset}"
echo "mode=$(stat -c %a "$KUBECONFIG")"
grep -E 'current-context:|server:|namespace:|token:' "$KUBECONFIG" | tr -s ' ' | sed 's/^ //'
