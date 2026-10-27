#!/usr/bin/env bash
# Builds the images, deploys the Helm chart into a local kind cluster and checks that Runwarden
# works there: scripts run, output streams over WebSocket, the security context is enforced and
# history survives a pod restart, runs are pinned to their environment's cluster, and the built-in
# administrator works. KEEP=1 leaves the release and cluster running.
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
# Test-only layer: kubectl and a runner with "kubernetes": true.
E2E=$(mktemp -d)
cp "$(command -v kubectl)" "$E2E/kubectl"
cat > "$E2E/kube_whoami.json" <<'EOF'
{ "name": "Kube Whoami", "group": "Test", "script_path": "/opt/runwarden/e2e/kube_whoami.sh",
  "working_directory": ".", "requires_terminal": false, "tags": ["devops"], "kubernetes": true,
  "environment_parameters": { "Environment": "target", "Source": "source" },
  "parameters": [
    { "name": "Environment", "type": "list", "required": true, "values": ["development", "acceptance", "production"] },
    { "name": "Source", "type": "list", "values": ["development", "acceptance", "production"] },
    { "name": "Wait", "type": "text", "default": "0", "env_var": "WAIT" } ] }
EOF
cat > "$E2E/kube_whoami.sh" <<'EOF'
#!/usr/bin/env bash
sleep "${WAIT:-0}"
echo "context=$(kubectl config current-context) var=$RUNWARDEN_KUBE_CONTEXT source=${RUNWARDEN_KUBE_SOURCE_CONTEXT:-}"
echo "marker=$(kubectl get configmap marker -o jsonpath='{.data.env}')"
for ns in rw-development rw-acceptance rw-production; do
  if kubectl get configmaps -n "$ns" >/dev/null 2>&1; then echo "list $ns: allowed"; else echo "list $ns: denied"; fi
done
if [ -n "${RUNWARDEN_KUBE_SOURCE_CONTEXT:-}" ]; then
  echo "source marker=$(kubectl --context "$RUNWARDEN_KUBE_SOURCE_CONTEXT" get configmap marker -o jsonpath='{.data.env}')"
fi
EOF
# Isolation between runs: a victim holding a secret and private files, and an attacker.
cat > "$E2E/isolation_victim.json" <<'EOF'
{ "name": "Isolation Victim", "group": "Test", "script_path": "/opt/runwarden/e2e/isolation_victim.sh",
  "working_directory": ".", "requires_terminal": false, "tags": ["devops"], "secrets": ["DB_PASSWORD"],
  "environment_parameters": { "Environment": "target" },
  "parameters": [ { "name": "Environment", "type": "list", "required": true, "values": ["production"] } ] }
EOF
cat > "$E2E/isolation_victim.sh" <<'EOF'
#!/usr/bin/env bash
echo "victim-home" > "$HOME/token"
echo "victim-notes" > "$RUNWARDEN_WORKSPACE/notes"
echo "ready pid=$$ home=$HOME"
exec sleep 20
EOF
cat > "$E2E/isolation_attack.json" <<'EOF'
{ "name": "Isolation Attack", "group": "Test", "script_path": "/opt/runwarden/e2e/isolation_attack.sh",
  "working_directory": ".", "requires_terminal": false, "tags": ["devops"],
  "parameters": [
    { "name": "Pid", "type": "int", "required": true, "env_var": "VICTIM_PID" },
    { "name": "Home", "type": "text", "required": true, "env_var": "VICTIM_HOME" } ] }
EOF
cat > "$E2E/isolation_attack.sh" <<'EOF'
#!/usr/bin/env bash
# Prints attack:<name>=open|blocked for what a run must not do, and what it must still do.
try() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "attack:$name=open"; else echo "attack:$name=blocked"; fi
}
try other-home cat "$VICTIM_HOME/token"
try other-environ cat "/proc/$VICTIM_PID/environ"
try other-mem head -c1 "/proc/$VICTIM_PID/mem"
try other-signal kill -0 "$VICTIM_PID"
try data-dir ls /var/lib/runwarden
try secrets ls /runwarden/secrets/production
try kubeconfigs ls /runwarden/kube
try runner-configs ls /runwarden/runners
try workspace-root ls /var/lib/runwarden-work
try other-workspace cat /var/lib/runwarden-work/mallory/notes
try shared-write touch /var/lib/runwarden-work/shared/evil
try own-home sh -c 'echo x > "$HOME/f" && cat "$HOME/f"'
try own-workspace sh -c 'echo x > "$RUNWARDEN_WORKSPACE/f" && cat "$RUNWARDEN_WORKSPACE/f"'
try system cat /etc/os-release
echo "attacks done"
EOF
cat > "$E2E/Dockerfile" <<'EOF'
FROM runwarden-examples:dev
COPY --chmod=0755 kubectl /usr/local/bin/kubectl
COPY kube_whoami.json isolation_victim.json isolation_attack.json /runwarden/runners/
COPY --chmod=0755 kube_whoami.sh isolation_victim.sh isolation_attack.sh /opt/runwarden/e2e/
EOF
docker build -q -t runwarden-e2e:dev "$E2E" >/dev/null
rm -rf "$E2E"
kind load docker-image runwarden-e2e:dev --name "$CLUSTER"

step "Install"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic runwarden-env --from-literal=RUNNER_ENV_GREETING=hello \
  --from-literal=SESSION_KEY="$(openssl rand -base64 32)" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
DEV_DB=dev-db-password
PROD_DB=prod-db-password-2026
secret() { kubectl -n "$NS" create secret generic "$1" "${@:2}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null; }
secret runwarden-secrets-common --from-literal=API_TOKEN=common-api-token
secret runwarden-secrets-development --from-literal=DB_PASSWORD="$DEV_DB"
secret runwarden-secrets-production --from-literal=DB_PASSWORD="$PROD_DB"
# One namespace + ServiceAccount per environment; each may read only its own ConfigMaps.
CA=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
for env in development acceptance production; do
  ns=rw-$env
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ns" create serviceaccount runner --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ns" create role read-configmaps --verb=get,list --resource=configmaps --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ns" create rolebinding runner --role=read-configmaps --serviceaccount="$ns:runner" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ns" create configmap marker --from-literal=env="$env" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  token=$(kubectl -n "$ns" create token runner --duration=2h)
  printf '%s\n' "apiVersion: v1" "kind: Config" "current-context: kind-$env" \
    "clusters: [{name: kind, cluster: {server: 'https://kubernetes.default.svc', certificate-authority-data: '$CA'}}]" \
    "users: [{name: runner, user: {token: '$token'}}]" \
    "contexts: [{name: kind-$env, context: {cluster: kind, user: runner, namespace: $ns}}]" \
    | kubectl -n "$NS" create secret generic "runwarden-kube-$env" --from-file=config=/dev/stdin --dry-run=client -o yaml \
    | kubectl apply -f - >/dev/null
done
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

step "Secrets per environment"
credentials() { # environment → output of a Check Credentials run
  local id
  id=$(curl -fs -X POST "$B/api/scripts/Check%20Credentials/runs" -H 'Content-Type: application/json' \
    -d "{\"values\":{\"Environment\":\"$1\"}}" | jq -r .id) || fail "Check Credentials did not start in $1"
  timeout 30 websocat -t --no-close "ws://127.0.0.1:$PORT/api/runs/$id/ws" </dev/null >/dev/null || true
  curl -fs "$B/api/runs/$id/output"
}
fp() { printf '%s' "$1" | sha256sum | cut -c1-12; }
dev=$(credentials development)
prod=$(credentials production)
echo "$dev" | grep -q "DB_PASSWORD: ${#DEV_DB} characters, fingerprint $(fp "$DEV_DB")" || fail "development secret wrong: $dev"
echo "$prod" | grep -q "DB_PASSWORD: ${#PROD_DB} characters, fingerprint $(fp "$PROD_DB")" || fail "production secret wrong: $prod"
echo "$prod" | grep -q "API_TOKEN: 16 characters" || fail "common secret missing: $prod"
echo "$dev$prod" | grep -q 'printed as is: \*\*\*\*\*\*' || fail "printed secret not masked"
if echo "$dev$prod" | grep -qE "$DEV_DB|$PROD_DB|common-api-token"; then fail "secret value in the output"; fi
code=$(curl -s -o /tmp/rw-acc.json -w '%{http_code}' -X POST "$B/api/scripts/Check%20Credentials/runs" \
  -H 'Content-Type: application/json' -d '{"values":{"Environment":"acceptance"}}')
[ "$code" = "409" ] && grep -q 'DB_PASSWORD is not available in acceptance' /tmp/rw-acc.json || fail "missing secret answered $code: $(cat /tmp/rw-acc.json)"
curl -fs "$B/api/admin/secrets" | jq -e '(.scripts[] | select(.name == "Check Credentials") | .missing.acceptance) == ["DB_PASSWORD"]' >/dev/null \
  || fail "admin secrets page does not show the gap"
[ "$(kubectl -n "$NS" exec deploy/runwarden -- stat -L -c %a /runwarden/secrets/production/DB_PASSWORD)" = "440" ] || fail "secret file mode"
if kubectl -n "$NS" exec deploy/runwarden -- env | grep -q "$PROD_DB"; then fail "secret in Runwarden's own environment"; fi
echo "each environment's value reaches its runs (common as fallback), masked; missing secret refused; files 0440, not in Runwarden's env"

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

step "Kubernetes clusters per environment"
upgrade() {
  helm upgrade runwarden deploy/helm/runwarden -n "$NS" -f deploy/kind/values-kind.yaml "$@" --wait --timeout 3m >/dev/null
  kubectl -n "$NS" rollout status deploy/runwarden --timeout 2m >/dev/null
  forward
}
upgrade --set limits.maxRunsPerUser=0
kube_run() { curl -fs -X POST "$B/api/scripts/Kube%20Whoami/runs" -H 'Content-Type: application/json' -d "{\"values\":$1}" | jq -r .id; }
kube_out() { timeout 60 websocat -t --no-close "ws://127.0.0.1:$PORT/api/runs/$1/ws" </dev/null >/dev/null || true; curl -fs "$B/api/runs/$1/output"; }
dev_id=$(kube_run '{"Environment":"development","Wait":"5"}') || fail "development run did not start"
prod_id=$(kube_run '{"Environment":"production","Wait":"5"}') || fail "production run did not start"
curl -fs "$B/api/runs?status=running" | jq -e --arg a "$dev_id" --arg b "$prod_id" '[.runs[].id] | (index($a) != null) and (index($b) != null)' >/dev/null \
  || fail "development and production runs were not running at the same time"
dev=$(kube_out "$dev_id")
prod=$(kube_out "$prod_id")
for env in development production; do
  out=$dev; [ "$env" = production ] && out=$prod
  echo "$out" | grep -q "context=$env var=$env source=$" || fail "$env run not pinned: $out"
  echo "$out" | grep -q "marker=$env" || fail "$env run landed in the wrong namespace: $out"
  for other in development acceptance production; do
    want=denied; [ "$other" = "$env" ] && want=allowed
    echo "$out" | grep -q "list rw-$other: $want" || fail "$env run: listing rw-$other not $want: $out"
  done
done
pair_id=$(kube_run '{"Environment":"development","Source":"production"}') || fail "two-environment run did not start"
pair=$(kube_out "$pair_id")
echo "$pair" | grep -q "context=development var=development source=production" && echo "$pair" | grep -q "source marker=production" \
  || fail "two-environment run: $pair"
curl -fs "$B/api/runs/$pair_id" | jq -e '[.clusters[].environment] == ["development","production"] and .clusters[0].server == "https://kubernetes.default.svc"' >/dev/null \
  || fail "history does not show the clusters"
if echo "$dev$prod$pair" | grep -q 'eyJ'; then fail "a token reached the run output"; fi
admin=$(curl -fs "$B/api/admin/secrets")
echo "$admin" | jq -e '.kubernetes.configured and ([.kubernetes.clusters[] | select((.error // "") == "") | .environment] | sort) == ["acceptance","development","production"]' >/dev/null \
  || fail "admin overview does not list the three clusters"
if echo "$admin" | grep -q 'eyJ'; then fail "a token reached the admin overview"; fi
[ "$(kubectl -n "$NS" exec deploy/runwarden -- stat -L -c %a /runwarden/kube/production/config)" = "440" ] || fail "kubeconfig file mode"
echo "concurrent development + production runs pinned to their own namespace and ServiceAccount; source context in two-environment runs; history and admin show servers, never tokens"

step "Isolation between runs (Landlock)"
# Still maxRunsPerUser=0: the victim and the attacker run at the same time. Sign-in is off, so
# both runs belong to the same user; another user's workspace is planted next to theirs.
ready=$(kubectl -n "$NS" exec deploy/runwarden -- bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081; printf "GET /readyz HTTP/1.0\r\n\r\n" >&3; cat <&3')
echo "$ready" | grep -q 'isolation: sandboxed (Landlock ABI' || fail "readiness does not report the sandbox: $ready"
iso=$(curl -fs "$B/api/admin/secrets" | jq -c .isolation)
echo "$iso" | jq -e '.active and .mode == "required" and .abi >= 1' >/dev/null || fail "admin overview does not show the isolation: $iso"
signal=open
echo "$iso" | jq -e .signal_scoping >/dev/null && signal=blocked
kubectl -n "$NS" exec deploy/runwarden -- sh -c 'mkdir -p -m 700 /var/lib/runwarden-work/mallory /var/lib/runwarden-work/shared && echo mallory-notes > /var/lib/runwarden-work/mallory/notes'
victim=$(curl -fs -X POST "$B/api/scripts/Isolation%20Victim/runs" -H 'Content-Type: application/json' -d '{"values":{"Environment":"production"}}' | jq -r .id) \
  || fail "victim did not start"
line=
for _ in $(seq 20); do line=$(curl -fs "$B/api/runs/$victim/output" | grep '^ready pid=') && break; sleep 0.5; done
[ -n "$line" ] || fail "victim not ready"
vpid=$(echo "$line" | sed 's/.*pid=\([0-9]*\).*/\1/')
vhome=$(echo "$line" | sed 's/.*home=//')
attack=$(curl -fs -X POST "$B/api/scripts/Isolation%20Attack/runs" -H 'Content-Type: application/json' \
  -d "{\"values\":{\"Pid\":$vpid,\"Home\":\"$vhome\"}}" | jq -r .id) || fail "attacker did not start"
out=$(kube_out "$attack")
echo "$out" | grep -q 'attacks done' || fail "attack script did not finish: $out"
for a in other-home other-environ other-mem data-dir secrets kubeconfigs runner-configs workspace-root other-workspace shared-write; do
  echo "$out" | grep -q "attack:$a=blocked" || fail "attack $a not blocked: $out"
done
echo "$out" | grep -q "attack:other-signal=$signal" || fail "other-signal not $signal: $out"
for a in own-home own-workspace system; do
  echo "$out" | grep -q "attack:$a=open" || fail "a run cannot do $a: $out"
done
if echo "$out" | grep -qE "$PROD_DB|victim-home|mallory-notes"; then fail "the attacker read another run's data: $out"; fi
kube_out "$victim" >/dev/null
curl -fs "$B/api/runs/$victim" | jq -e '.status == "finished" and .exit_code == 0' >/dev/null || fail "victim did not survive the attack"
upgrade
echo "the attacker could not read the victim's HOME, environ or memory, read DATA_DIR, secrets, kubeconfigs, runner configs or another user's workspace (signals: $signal)"

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
rev=$(curl -fs -b "$JAR" "$B/api/admin/policy" | jq .policy.revision)
curl -fs -b "$JAR" -X PUT "$B/api/admin/policy/users/alice" -H 'Content-Type: application/json' \
  -d "{\"revision\":$rev,\"roles\":[\"devops\"]}" >/dev/null || fail "devops role not given to the first user"
echo "the built-in administrator gave the devops role to alice (before her first sign-in)"

upgrade --set-json 'auth.methods=["ldap"]' --set auth.ldap.url=ldaps://ldap.invalid,auth.ldap.userBaseDN=dc=example,auth.cookieSecure=false
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$B/api/me")
[ "$code" = "401" ] || fail "built-in admin session still valid after disabling ($code)"
kubectl -n "$NS" exec deploy/runwarden -- test ! -e /var/lib/runwarden/initial-admin-password || fail "password file not removed"
rm -f "$JAR"
kubectl -n "$NS" exec deploy/runwarden -- cat /var/lib/runwarden/policy.json \
  | jq -e '(.rules | length == 1) and ([.users[] | select(.roles | index("devops")) | .name] == ["alice"])' >/dev/null \
  || fail "policy (rule, devops user) not kept on the data volume"
if kubectl -n "$NS" logs deploy/runwarden | grep -q 'nobody has the devops role'; then fail "no-devops warning although alice is devops"; fi
upgrade
echo "disabled: password file removed, admin session refused; policy and devops user kept across restarts"

echo
echo "PASS: Runwarden runs in kind"
