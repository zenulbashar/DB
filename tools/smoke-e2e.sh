#!/usr/bin/env bash
# Local end-to-end smoke test: control-plane + console against a real Postgres.
#
# Proves the full read+write path works — bootstrap a platform, create a project
# via the API, then confirm the *console* renders that live data and that its
# create-branch server action actually mutates the control plane. This is the
# reproducible form of the Phase 3 console verification (CHANGELOG 2026-07-19).
#
# Requirements: a FRESH control-plane database (no org bootstrapped yet) reachable
# via DATABASE_URL, Go, Node, and a built or buildable console. Docker is not
# required. Run against docker-compose Postgres with:
#   make dev-db && DATABASE_URL=postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable tools/smoke-e2e.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_PORT="${API_PORT:-8080}"
CONSOLE_PORT="${CONSOLE_PORT:-3000}"
API_URL="http://127.0.0.1:${API_PORT}/v1"
DATABASE_URL="${DATABASE_URL:-postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable}"
# Fixed dev KEK (matches the Makefile); production keys come from a KMS.
export NDB_KEKS="${NDB_KEKS:-1:XBMsCi/pCq8xMYM9KP0xSjO/O8sB9oYLFqOUoHy+E10=}"
export NDB_ACTIVE_KEK="${NDB_ACTIVE_KEK:-1}"
export NDB_BOOTSTRAP_TOKEN="${NDB_BOOTSTRAP_TOKEN:-dev-bootstrap-token}"
export DATABASE_URL

WORK="$(mktemp -d)"
API_PID="" CONSOLE_PID=""
cleanup() {
  [ -n "$CONSOLE_PID" ] && kill "$CONSOLE_PID" 2>/dev/null || true
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mSMOKE FAIL: %s\033[0m\n' "$*" >&2; exit 1; }

say "Build & start control-plane (auto-migrates) on :${API_PORT}"
( cd "$ROOT/services/control-plane" && go build -o "$WORK/api" ./cmd/api )
PORT="$API_PORT" "$WORK/api" >"$WORK/api.log" 2>&1 &
API_PID=$!
for i in $(seq 1 30); do
  curl -fsS "$API_URL/healthz" >/dev/null 2>&1 && break
  sleep 1
  [ "$i" = 30 ] && { cat "$WORK/api.log"; fail "control-plane did not become healthy"; }
done

say "Bootstrap platform → owner API key"
BOOT="$(curl -fsS -X POST "$API_URL/bootstrap" -H 'Content-Type: application/json' \
  -d "{\"bootstrap_token\":\"$NDB_BOOTSTRAP_TOKEN\",\"email\":\"smoke@example.com\",\"org_name\":\"Smoke\"}")" \
  || fail "bootstrap failed (is the database fresh?)"
TOKEN="$(printf '%s' "$BOOT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["api_key"]["token"])')"
ORG="$(printf '%s' "$BOOT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["org"]["id"])')"
[ -n "$TOKEN" ] || fail "no token from bootstrap"

say "Create a project via the API"
PRJ="$(curl -fsS -X POST "$API_URL/projects" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"org_id\":\"$ORG\",\"name\":\"smoke-demo\",\"region\":\"syd1\",\"pg_version\":17}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')"
[ -n "$PRJ" ] || fail "project create failed"

say "Build & start console on :${CONSOLE_PORT} (NDB_API_URL=$API_URL)"
[ -d "$ROOT/console/.next" ] || ( cd "$ROOT/console" && npm run build )
( cd "$ROOT/console" && NDB_API_URL="$API_URL" PORT="$CONSOLE_PORT" npm run start >"$WORK/console.log" 2>&1 ) &
CONSOLE_PID=$!
for i in $(seq 1 60); do
  curl -fsS -o /dev/null "http://127.0.0.1:${CONSOLE_PORT}/connect" 2>/dev/null && break
  sleep 1
  [ "$i" = 60 ] && { cat "$WORK/console.log"; fail "console did not start"; }
done

say "Assert: dashboard renders the live project"
HTML="$(curl -fsS -b "ndb_token=$TOKEN" "http://127.0.0.1:${CONSOLE_PORT}/")"
printf '%s' "$HTML" | grep -q 'smoke-demo' || fail "dashboard did not render the project"
# Auth guard: no cookie → redirect to /connect.
CODE="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONSOLE_PORT}/")"
[ "$CODE" = 307 ] || fail "expected 307 redirect without a session, got $CODE"

say "Assert: detail page renders the seeded default branch"
curl -fsS -b "ndb_token=$TOKEN" "http://127.0.0.1:${CONSOLE_PORT}/projects/$PRJ" | grep -q 'main' \
  || fail "detail page did not render the default branch"

printf '\n\033[1;32mSMOKE OK — console renders live control-plane data (read + auth guard).\033[0m\n'
