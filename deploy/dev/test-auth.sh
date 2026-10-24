#!/usr/bin/env bash
# Starts the dev identity providers (Keycloak, OpenLDAP), runs the auth integration tests
# against them, and stops them again. Set KEEP_RUNNING=1 to leave the containers up.
set -euo pipefail
cd "$(dirname "$0")/../.."

compose=(docker compose -f deploy/dev/compose.yaml)
"${compose[@]}" up -d
if [ "${KEEP_RUNNING:-}" != "1" ]; then
  trap '"${compose[@]}" down -v' EXIT
fi

echo "Waiting for Keycloak..."
for _ in $(seq 90); do
  curl -fs http://localhost:8180/realms/runwarden/.well-known/openid-configuration >/dev/null && break
  sleep 2
done
echo "Waiting for OpenLDAP..."
for _ in $(seq 60); do
  "${compose[@]}" exec -T ldap ldapsearch -x -H ldap://localhost -D cn=admin,dc=example,dc=org \
    -w admin-password -b ou=people,dc=example,dc=org '(uid=alice)' dn >/dev/null 2>&1 && break
  sleep 1
done

go test -tags integration -count=1 -v -run Integration ./internal/auth/
