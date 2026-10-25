#!/usr/bin/env bash
# Builds the images, deploys the Helm chart into a local kind cluster and checks that Runwarden
# works there: scripts run, output streams over WebSocket, the security context is enforced and
# history survives a pod restart. KEEP=1 leaves the release and cluster running.
set -euo pipefail
cd "$(dirname "$0")/../.."

CLUSTER=runwarden
NS=runwarden
PORT=18080
B=http://127.0.0.1:$PORT

step() { echo; echo "== $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

step "Cluster"
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --config deploy/kind/kind-config.yaml
kubectl config use-context "kind-$CLUSTER" >/dev/null

step "Images"
docker build -q --build-arg VERSION=v0.0.0-kind -t runwarden:dev . >/dev/null
docker build -q -f examples/Dockerfile --build-arg BASE=runwarden:dev -t runwarden-examples:dev . >/dev/null
kind load docker-image runwarden-examples:dev --name "$CLUSTER"

step "Install"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic runwarden-env --from-literal=RUNNER_ENV_GREETING=hello \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
helm upgrade --install runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml --wait --timeout 3m
kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m

PF=
forward() {
  [ -n "$PF" ] && kill "$PF" 2>/dev/null || true
  kubectl -n "$NS" port-forward svc/runwarden "$PORT:80" >/dev/null 2>&1 &
  PF=$!
  for _ in $(seq 50); do curl -fs "$B/readyz" >/dev/null && return; sleep 0.2; done
  fail "service not reachable"
}
cleanup() {
  [ -n "$PF" ] && kill "$PF" 2>/dev/null || true
  if [ "${KEEP:-}" != "1" ]; then
    helm uninstall runwarden -n "$NS" >/dev/null 2>&1 || true
    kubectl delete namespace "$NS" --wait=false >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
forward

step "API"
scripts=$(curl -fs "$B/api/scripts" | jq -c '[.scripts[].name]')
echo "scripts: $scripts"
echo "$scripts" | grep -q "Restart Service" || fail "example scripts missing"
curl -fs "$B/api/scripts/%F0%9F%97%82%EF%B8%8F%20Rotate%20Logs/parameters/Instances/values?Environment=production" \
  | jq -e '[.values[].value] | index("web-3")' >/dev/null || fail "dynamic values helper did not run"

step "Run a script"
ID=$(curl -fs -X POST "$B/api/scripts/Restart%20Service/runs" -H 'Content-Type: application/json' \
  -d '{"values":{"Service":"api"}}' | jq -r .id)
echo "run $ID"
result=$(timeout 30 websocat -t --no-close "ws://127.0.0.1:$PORT/api/runs/$ID/ws" </dev/null | jq -c 'select(.type=="result")')
echo "result: $result"
echo "$result" | jq -e '.status == "finished"' >/dev/null || fail "run did not finish"
curl -fs "$B/api/runs/$ID/output" | grep -q '\[--service\]' || fail "output missing"
curl -fs "$B/api/runs/$ID" | jq -e '.terminal == true' >/dev/null || fail "run did not get a terminal"
echo "ran on a terminal"

step "Audit log (ECS) in the pod log"
kubectl -n "$NS" logs deploy/runwarden | jq -ec --arg id "$ID" \
  'select(.event.dataset == "runwarden.audit" and .event.action == "run-end" and .runwarden.run.id == $id)
   | {action: .event.action, outcome: .event.outcome, user: .user.name, exit: .process.exit_code, script: .runwarden.script.name}' \
  || fail "no run-end audit event for $ID"

step "Workspace"
kubectl -n "$NS" exec deploy/runwarden -- sh -c 'touch /var/lib/runwarden-work/probe && echo "workspace writable"' \
  || fail "workspace not writable"

step "Security context"
kubectl -n "$NS" exec deploy/runwarden -- id | grep -q 'uid=10001' || fail "not running as 10001"
if kubectl -n "$NS" exec deploy/runwarden -- touch /opt/runwarden/x 2>/dev/null; then fail "root filesystem is writable"; fi
kubectl -n "$NS" exec deploy/runwarden -- touch /tmp/x /home/runwarden/x || fail "tmp/home not writable"
kubectl -n "$NS" exec deploy/runwarden -- sh -c 'test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token' \
  || fail "service account token mounted"
echo "uid 10001, read-only root, writable /tmp and HOME, no API token"

step "History survives a pod restart"
kubectl -n "$NS" delete pod -l app.kubernetes.io/name=runwarden --wait=true >/dev/null
kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m >/dev/null
forward
curl -fs "$B/api/runs/$ID" | jq -e '.status == "finished"' >/dev/null || fail "run lost after restart"
echo "run $ID still in history"

echo
echo "PASS: Runwarden runs in kind"
