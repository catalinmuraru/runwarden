#!/usr/bin/env bash
sleep "${1:-0}"
echo "context=$WARD_KUBE_CONTEXT source=${WARD_KUBE_SOURCE_CONTEXT-unset}"
if [ -e "$KUBECONFIG" ]; then
  echo "mode=$(stat -c %a "$KUBECONFIG")"
  echo "config=$(cat "$KUBECONFIG")"
else
  echo "no kubeconfig"
fi
