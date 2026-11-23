#!/usr/bin/env bash
# Values provider that depends on the environment's cluster: its current context, or "none".
if [ -e "$KUBECONFIG" ]; then
  echo "ctx-$WARD_KUBE_CONTEXT"
else
  echo "none"
fi
