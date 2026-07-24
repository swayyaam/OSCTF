#!/usr/bin/env bash
# End-to-end API smoke test against a running stack (docs/v0.1/11-testing-ci.md).
# Exits non-zero on the first failing assertion. Usage: BASE_URL=... scripts/smoke.sh
set -uo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_EMAIL="${OSCTF_ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${OSCTF_ADMIN_PASSWORD:-devpassword123}"
ORIGIN="-H Origin:${BASE_URL}"
SUFFIX="$RANDOM$RANDOM"
JAR_A="$(mktemp)"; JAR_B="$(mktemp)"; JAR_ADMIN="$(mktemp)"
trap 'rm -f "$JAR_A" "$JAR_B" "$JAR_ADMIN"' EXIT

pass=0
step() { printf '  [%2d] %s ... ' "$((++pass))" "$1"; }
ok()   { echo "OK"; }
fail() { echo "FAIL: $1"; exit 1; }

# Extract a JSON string field with a tiny python helper (jq may be absent).
jget() { python3 -c 'import sys,json; d=json.load(sys.stdin); print(d'"$1"')' 2>/dev/null; }

echo "Smoke test against ${BASE_URL}"

step "/healthz is 200"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/healthz")" = 200 ] || fail "healthz"; ok

step "/readyz is 200"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/readyz")" = 200 ] || fail "readyz"; ok

step "register user A"
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/auth/register" \
  -H 'Content-Type: application/json' -d "{\"username\":\"smokeA$SUFFIX\",\"email\":\"a$SUFFIX@ex.com\",\"password\":\"supersecret1\"}")
[ "$code" = 201 ] || fail "register A got $code"; ok

step "GET /auth/me matches"
name=$(curl -s -b "$JAR_A" "$BASE_URL/api/v0/auth/me" | jget "['username']")
[ "$name" = "smokeA$SUFFIX" ] || fail "me = $name"; ok

step "create team A (invite code present)"
invite=$(curl -s -b "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/teams" \
  -H 'Content-Type: application/json' -d "{\"name\":\"Smoke $SUFFIX\"}" | jget "['invite_code']")
[ -n "$invite" ] || fail "no invite code"; ok

step "register user B and join with invite code"
curl -s -o /dev/null -c "$JAR_B" $ORIGIN -X POST "$BASE_URL/api/v0/auth/register" \
  -H 'Content-Type: application/json' -d "{\"username\":\"smokeB$SUFFIX\",\"email\":\"b$SUFFIX@ex.com\",\"password\":\"supersecret1\"}"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR_B" $ORIGIN -X POST "$BASE_URL/api/v0/teams/join" \
  -H 'Content-Type: application/json' -d "{\"invite_code\":\"$invite\"}")
[ "$code" = 200 ] || fail "join got $code"; ok

step "admin login and set event window to now +/- 1h"
curl -s -o /dev/null -c "$JAR_ADMIN" $ORIGIN -X POST "$BASE_URL/api/v0/auth/login" \
  -H 'Content-Type: application/json' -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}"
START=$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
END=$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR_ADMIN" $ORIGIN -X PATCH "$BASE_URL/api/v0/admin/event" \
  -H 'Content-Type: application/json' -d "{\"starts_at\":\"$START\",\"ends_at\":\"$END\"}")
[ "$code" = 200 ] || fail "event patch got $code"; ok

step "GET /challenges as A shows seeded examples"
n=$(curl -s -b "$JAR_A" "$BASE_URL/api/v0/challenges" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))')
[ "$n" -ge 1 ] || fail "no challenges seeded ($n)"; ok

step "submit wrong flag to sanity-check -> correct:false"
res=$(curl -s -b "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/challenges/sanity-check/submit" \
  -H 'Content-Type: application/json' -d '{"flag":"OSCTF{nope}"}' | jget "['correct']")
[ "$res" = "False" ] || fail "wrong flag returned $res"; ok

step "submit correct flag -> correct:true"
res=$(curl -s -b "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/challenges/sanity-check/submit" \
  -H 'Content-Type: application/json' -d '{"flag":"OSCTF{welcome_to_the_game}"}' | jget "['correct']")
[ "$res" = "True" ] || fail "correct flag returned $res"; ok

step "scoreboard shows the team with points > 0"
pts=$(curl -s "$BASE_URL/api/v0/scoreboard" | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((s['points'] for s in d['standings'] if s['name']=='Smoke $SUFFIX'),0))")
[ "$pts" -gt 0 ] || fail "team points = $pts"; ok

step "duplicate correct submit -> 409"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/challenges/sanity-check/submit" \
  -H 'Content-Type: application/json' -d '{"flag":"OSCTF{welcome_to_the_game}"}')
[ "$code" = 409 ] || fail "duplicate got $code"; ok

step "rapid submissions trip the rate limit (>=1 x 429)"
got429=0
for _ in $(seq 1 12); do
  c=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR_A" $ORIGIN -X POST "$BASE_URL/api/v0/challenges/base-what/submit" \
    -H 'Content-Type: application/json' -d '{"flag":"x"}')
  [ "$c" = 429 ] && got429=1
done
[ "$got429" = 1 ] || fail "no 429 seen"; ok

step "/metrics exposes osctf_submissions_total"
curl -s "$BASE_URL/metrics" | grep -q '^osctf_submissions_total' || fail "missing metric"; ok

echo "Smoke test passed ($pass assertions)."
