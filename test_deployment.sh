#!/bin/bash
# End-to-end checks against a running cron instance behind the ZimaOS gateway.
#
#   ZIMA_USER=admin ZIMA_PASS=… ./test_deployment.sh http://<zimaos-host>
#
# Credentials come from the environment only (never from argv, which is
# visible in `ps`). Every check prints PASS/FAIL with the measured value;
# the script exits non-zero if any check failed. Test tasks are removed at
# the end, including on abort.
set -uo pipefail

BASE="${1:?usage: ZIMA_USER=… ZIMA_PASS=… $0 http://<host>}"
BASE="${BASE%/}"
: "${ZIMA_USER:?set ZIMA_USER}"
: "${ZIMA_PASS:?set ZIMA_PASS}"

pass=0; fail=0; created=()
ok()   { pass=$((pass+1)); printf '  PASS  %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; }
check() { # check <label> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1 = $3"; else bad "$1: expected '$2', got '$3'"; fi
}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1" 2>/dev/null; }

cleanup() {
  for id in "${created[@]:-}"; do
    [ -n "$id" ] && curl -s -o /dev/null -X DELETE -H "$AUTH" "$BASE/cron/tasks/$id"
  done
}
trap cleanup EXIT

echo "== login =="
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ZIMA_USER\",\"password\":\"$ZIMA_PASS\"}" "$BASE/v1/users/login" \
  | json "d['data']['token']['access_token']")
[ -n "$TOKEN" ] || { echo "login failed"; exit 2; }
AUTH="Authorization: Bearer $TOKEN"
ok "session token obtained"

echo "== auth (finding 4) =="
check "GET /cron/tasks without token"  401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/cron/tasks")"
check "GET /cron/health without token" 200 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/cron/health")"
check "GET /cron/tasks with token"     200 "$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$BASE/cron/tasks")"
check "POST with foreign Origin"       403 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$AUTH" -H 'Origin: http://evil.example' -H 'Content-Type: application/json' -d '{}' "$BASE/cron/tasks")"

echo "== version =="
VERSION=$(curl -s "$BASE/cron/health" | json "d['version']")
echo "  running version: $VERSION"

echo "== scheduler (finding 1): weekday-bound task fires on that weekday =="
BODY=$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"e2e weekday","command":"true","type":"cron","cron_expr":"0 4 * * 0","category":"e2e"}' "$BASE/cron/tasks")
ID1=$(echo "$BODY" | json "d['id']"); created+=("$ID1")
NEXT=$(echo "$BODY" | json "d['next_run_at']")
WD=$(python3 -c "import datetime,sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1])/1000).strftime('%a %H:%M'))" "$NEXT")
check "next run of '0 4 * * 0'" "Sun 04:00" "$WD"

BODY=$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"e2e monthly","command":"true","type":"cron","cron_expr":"0 5 1 * *","category":"e2e"}' "$BASE/cron/tasks")
ID2=$(echo "$BODY" | json "d['id']"); created+=("$ID2")
DOM=$(python3 -c "import datetime,sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1])/1000).strftime('%d %H:%M'))" "$(echo "$BODY" | json "d['next_run_at']")")
check "next run of '0 5 1 * *'" "01 05:00" "$DOM"

echo "== api units (finding 6) =="
BODY=$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"e2e interval","command":"echo e2e-ok","type":"interval","interval_min":30,"category":"e2e","tags":["e2e"]}' "$BASE/cron/tasks")
ID3=$(echo "$BODY" | json "d['id']"); created+=("$ID3")
check "interval_min" 30 "$(echo "$BODY" | json "d['interval_min']")"
check "interval_ms"  1800000 "$(echo "$BODY" | json "d['interval_ms']")"

echo "== run, result code, history =="
curl -s -o /dev/null -X POST -H "$AUTH" "$BASE/cron/tasks/$ID3/run"
sleep 2
check "last_result.code" completed "$(curl -s -H "$AUTH" "$BASE/cron/tasks/$ID3" | json "d['last_result']['code']")"
check "history entries" 1 "$(curl -s -H "$AUTH" "$BASE/cron/tasks/$ID3/logs" | json "len(d)")"
check "history csv header" "time,duration_ms,success,code,message" "$(curl -s -H "$AUTH" "$BASE/cron/tasks/$ID3/logs?format=csv" | head -1 | tr -d '\r')"
curl -s -o /dev/null -X POST -H "$AUTH" "$BASE/cron/tasks/$ID3/logs/clear"
check "history after clear" 0 "$(curl -s -H "$AUTH" "$BASE/cron/tasks/$ID3/logs" | json "len(d)")"

echo "== validation errors carry codes =="
check "missing name -> code" name_required "$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' -d '{"command":"true","type":"interval","interval_min":1}' "$BASE/cron/tasks" | json "d['code']")"
check "bad cron -> code" cron_invalid "$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"x","command":"true","type":"cron","cron_expr":"99 * * * *"}' "$BASE/cron/tasks" | json "d['code']")"

echo "== edit (PUT) =="
check "PUT status" 200 "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"e2e interval edited","command":"true","type":"interval","interval_min":45,"category":"e2e"}' "$BASE/cron/tasks/$ID3")"
check "edited interval_min" 45 "$(curl -s -H "$AUTH" "$BASE/cron/tasks/$ID3" | json "d['interval_min']")"

echo "== toggle =="
check "toggle -> paused"  paused  "$(curl -s -X POST -H "$AUTH" "$BASE/cron/tasks/$ID3/toggle" | json "d['status']")"
check "toggle -> running" running "$(curl -s -X POST -H "$AUTH" "$BASE/cron/tasks/$ID3/toggle" | json "d['status']")"

echo "== export/import round trip (finding 5) =="
EXPORT=$(curl -s -H "$AUTH" "$BASE/cron/export")
N_E2E=$(echo "$EXPORT" | json "len([t for t in d['tasks'] if t.get('category')=='e2e'])")
check "e2e tasks in export" 3 "$N_E2E"
IMPORT=$(echo "$EXPORT" | python3 -c "import json,sys; d=json.load(sys.stdin); d['tasks']=[t for t in d['tasks'] if t.get('category')=='e2e']; [t.update(name=t['name']+' (import)') for t in d['tasks']]; print(json.dumps(d))")
RES=$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' -d "$IMPORT" "$BASE/cron/import")
check "imported" 3 "$(echo "$RES" | json "d['imported']")"
check "skipped"  0 "$(echo "$RES" | json "len(d['skipped'])")"
for id in $(curl -s -H "$AUTH" "$BASE/cron/tasks?category=e2e" | json "' '.join(t['id'] for t in d if t['name'].endswith('(import)'))"); do created+=("$id"); done

echo "== filters =="
check "category filter count" 6 "$(curl -s -H "$AUTH" "$BASE/cron/tasks?category=e2e" | json "len(d)")"
check "categories contain e2e" True "$(curl -s -H "$AUTH" "$BASE/cron/categories" | json "'e2e' in d")"

echo "== templates & settings =="
check "templates" 7 "$(curl -s -H "$AUTH" "$BASE/cron/templates" | json "len(d)")"
check "settings readable" 200 "$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$BASE/cron/settings")"

echo
echo "$pass passed, $fail failed (version $VERSION at $BASE)"
[ "$fail" -eq 0 ]
