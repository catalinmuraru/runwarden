#!/usr/bin/env bash
# Builds the images, deploys the Helm chart into a local kind cluster and checks that Runwarden
# works there: scripts run, output streams over WebSocket, the security context is enforced and
# history survives a pod restart, and the built-in administrator works. KEEP=1 leaves the release and cluster running.
set -euo pipefail
cd "$(dirname "$0")/../.."

CLUSTER=runwarden
NS=runwarden
PORT=18080
HPORT=18081
B=http://127.0.0.1:$PORT
H=http://127.0.0.1:$HPORT

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
  --from-literal=SESSION_KEY="$(openssl rand -base64 32)" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
helm upgrade --install runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml --wait --timeout 3m
kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m

PF=
HF=
forward() {
  [ -n "$PF" ] && kill "$PF" "$HF" 2>/dev/null || true
  # The web port through the Service; the health port only exists on the pod.
  kubectl -n "$NS" port-forward svc/runwarden "$PORT:80" >/dev/null 2>&1 &
  PF=$!
  kubectl -n "$NS" port-forward deploy/runwarden "$HPORT:8081" >/dev/null 2>&1 &
  HF=$!
  for _ in $(seq 50); do curl -fs "$H/readyz" >/dev/null && curl -fs "$B/auth/config" >/dev/null && return; sleep 0.2; done
  fail "service not reachable"
}
cleanup() {
  [ -n "$PF" ] && kill "$PF" "$HF" 2>/dev/null || true
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

step "Health endpoints only on the health port"
[ "$(kubectl -n "$NS" get svc runwarden -o jsonpath='{.spec.ports[*].name}')" = "http" ] || fail "service exposes more than http"
code=$(curl -s -o /dev/null -w '%{http_code}' "$B/readyz")
[ "$code" = "404" ] || fail "/readyz on the service answered $code"
echo "probes on :8081, service exposes only http, /readyz on the service is 404"

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

step "Private HOME per run, one run per user"
curl -fs "$B/api/runs/$ID/output" | grep -q 'HOME=/var/lib/runwarden/tmp/'"$ID"'/home' || fail "run did not get a private HOME"
curl -fs "$B/api/runs/$ID/output" | grep -q 'KUBECONFIG=/var/lib/runwarden/tmp/'"$ID"'/home/.kube/config' || fail "KUBECONFIG not private"
first=$(curl -fs -X POST "$B/api/scripts/Restart%20Service/runs" -H 'Content-Type: application/json' -d '{"values":{"Service":"api"}}' | jq -r .id)
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/scripts/Restart%20Service/runs" -H 'Content-Type: application/json' -d '{"values":{"Service":"api"}}')
[ "$code" = "429" ] || fail "second concurrent run by the same user answered $code"
timeout 30 websocat -t --no-close "ws://127.0.0.1:$PORT/api/runs/$first/ws" </dev/null >/dev/null || true
echo "HOME and KUBECONFIG inside the run's own directory; a second concurrent run is refused"

step "Audit log (ECS) in the pod log"
kubectl -n "$NS" logs deploy/runwarden | jq -ec --arg id "$ID" \
  'select(.event.dataset == "runwarden.audit" and .event.action == "run-end" and .runwarden.run.id == $id)
   | {action: .event.action, outcome: .event.outcome, user: .user.name, exit: .process.exit_code, script: .runwarden.script.name}' \
  || fail "no run-end audit event for $ID"

step "Running runs, timeouts and admin pages"
curl -fs "$B/api/runs?status=running" | jq -e '.runs | type == "array"' >/dev/null || fail "running list"
curl -fs "$B/api/scripts/Backup%20Database" | jq -e '.timeout_seconds == 7200' >/dev/null || fail "script timeout not applied"
curl -fs "$B/api/admin/activity?action=run-start" | jq -e --arg id "$ID" '[.events[].run_id] | index($id)' >/dev/null \
  || fail "run-start missing from the activity log"
curl -fs "$B/api/admin/users" | jq -e '.users | length > 0' >/dev/null || fail "users index empty"
echo "running list, per-script timeout, activity log and users index work"

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

step "NetworkPolicy"
api_ip=$(kubectl get svc kubernetes -n default -o jsonpath='{.spec.clusterIP}')
probe() { kubectl -n "$NS" exec deploy/runwarden -- timeout 3 bash -c "echo > /dev/tcp/$api_ip/443" >/dev/null 2>&1; }
probe || fail "API server not reachable before the policy (test precondition)"
helm upgrade runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml --set networkPolicy.enabled=true --wait --timeout 2m >/dev/null
blocked=
for _ in $(seq 30); do if ! probe; then blocked=1; break; fi; sleep 1; done
[ -n "$blocked" ] || fail "egress outside the allowlist still works"
curl -fs "$B/api/scripts" >/dev/null || fail "web port blocked by the policy"
ID2=$(curl -fs -X POST "$B/api/scripts/Restart%20Service/runs" -H 'Content-Type: application/json' -d '{"values":{"Service":"web"}}' | jq -r .id)
timeout 30 websocat -t --no-close "ws://127.0.0.1:$PORT/api/runs/$ID2/ws" </dev/null | jq -e 'select(.type=="result") | .status == "finished"' >/dev/null \
  || fail "run failed under the policy"
helm upgrade runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml --wait --timeout 2m >/dev/null
echo "egress outside the allowlist blocked, web port and runs still work"

step "History survives a pod restart"
kubectl -n "$NS" delete pod -l app.kubernetes.io/name=runwarden --wait=true >/dev/null
kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m >/dev/null
forward
curl -fs "$B/api/runs/$ID" | jq -e '.status == "finished"' >/dev/null || fail "run lost after restart"
echo "run $ID still in history"
curl -fs "$B/api/admin/activity?action=run-end" | jq -e --arg id "$ID" '[.events[].run_id] | index($id)' >/dev/null \
  || fail "activity log lost after restart"
curl -fs "$B/api/admin/users" | jq -e '.users | length > 0' >/dev/null || fail "users index lost after restart"
echo "activity log and users index survive the restart"

step "Built-in administrator"
upgrade() {
  helm upgrade runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml "$@" --wait --timeout 3m >/dev/null
  kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m >/dev/null
  forward
}
upgrade --set-json 'auth.methods=[]' --set auth.initialAdmin=true,auth.cookieSecure=false
JAR=$(mktemp)
code=$(curl -s -o /dev/null -w '%{http_code}' "$B/api/scripts")
[ "$code" = "401" ] || fail "API without a session answered $code"
curl -fs "$B/runs/x" | grep -q '<title>Sign in' || fail "signed-out page is not the sign-in page"
pw=$(kubectl -n "$NS" exec deploy/runwarden -- cat /var/lib/runwarden/initial-admin-password)
[ "${#pw}" -ge 40 ] || fail "password file missing or short"
if kubectl -n "$NS" logs deploy/runwarden | grep -qF "$pw"; then fail "password in the pod log"; fi
curl -fs -c "$JAR" -X POST "$B/auth/login" -H 'Content-Type: application/json' \
  -d "{\"method\":\"local\",\"password\":\"$pw\"}" >/dev/null || fail "built-in admin sign-in failed"
curl -fs -b "$JAR" "$B/api/me" | jq -e '.name == "admin@local" and .admin' >/dev/null || fail "not signed in as admin@local"
echo "password only in the file (not in the log); admin@local signed in"

step "Access policy"
pol=$(curl -fs -b "$JAR" "$B/api/admin/policy")
echo "$pol" | jq -e '[.policy.environments[].name] == ["development","acceptance","production"]' >/dev/null || fail "environments not seeded from the values"
rev=$(echo "$pol" | jq .policy.revision)
curl -fs -b "$JAR" -X POST "$B/api/admin/policy/rules" -H 'Content-Type: application/json' \
  -d "{\"revision\":$rev,\"role\":\"developer\",\"scripts\":[\"tag:developer\"],\"environments\":[\"development\"],\"access\":\"run\"}" >/dev/null \
  || fail "rule not created"
curl -fs -b "$JAR" "$B/api/admin/policy/effective?role=developer" \
  | jq -e '.scripts[] | select(.name == "🗂️ Rotate Logs") | .access.development.access == "run" and (.access.production == null)' >/dev/null \
  || fail "developer access not as the rule says"
curl -fs -b "$JAR" "$B/api/admin/activity?action=policy-change" | jq -e '.events | length == 1' >/dev/null || fail "policy change not audited"
curl -fs -b "$JAR" "$B/api/admin/policy/export" | grep -q 'tag:developer' || fail "export misses the rule"
echo "environments seeded, rule created, developer access computed, change audited, export works"

upgrade --set-json 'auth.methods=["ldap"]' --set auth.ldap.url=ldaps://ldap.invalid,auth.ldap.userBaseDN=dc=example,auth.cookieSecure=false
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$B/api/me")
[ "$code" = "401" ] || fail "built-in admin session still valid after disabling ($code)"
kubectl -n "$NS" exec deploy/runwarden -- test ! -e /var/lib/runwarden/initial-admin-password || fail "password file not removed"
rm -f "$JAR"
kubectl -n "$NS" exec deploy/runwarden -- cat /var/lib/runwarden/policy.json | jq -e '.rules | length == 1' >/dev/null \
  || fail "policy not kept on the data volume"
upgrade
echo "disabled: password file removed, admin session refused; policy kept across restarts"

echo
echo "PASS: Runwarden runs in kind"
