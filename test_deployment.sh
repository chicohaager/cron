#!/bin/bash
# =============================================================================
# cron Deployment Test Script
# Tests all 8 feature phases against a live ZimaOS instance
# Usage: ./test_deployment.sh [BASE_URL]
# =============================================================================

set +e

BASE="${1:-http://192.168.50.27}"
API="$BASE/cron"

PASS=0
FAIL=0
TASK_IDS=()

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { ((PASS++)); echo -e "  ${GREEN}PASS${NC} $1"; }
fail() { ((FAIL++)); echo -e "  ${RED}FAIL${NC} $1: $2"; }

# Helper: create task, capture ID
create_task() {
    local resp
    resp=$(curl -sf -X POST "$API/tasks" \
        -H "Content-Type: application/json" \
        -d "$1" 2>&1) || { echo ""; return 1; }
    local id
    id=$(echo "$resp" | jq -r '.id // empty')
    if [ -n "$id" ]; then
        echo "$id"
    else
        echo ""
        return 1
    fi
}

# Track task ID for cleanup
track() {
    TASK_IDS+=("$1")
}

# Helper: get JSON field from task
get_task() {
    curl -sf "$API/tasks/$1" 2>/dev/null
}

echo ""
echo "============================================="
echo " cron Deployment Tests"
echo " Target: $BASE"
echo "============================================="
echo ""

# ----- Phase 8: Health Endpoint -----
echo -e "${YELLOW}[Phase 8] Health Endpoint${NC}"

HEALTH=$(curl -sf "$API/health" 2>/dev/null || echo "FAILED")
if echo "$HEALTH" | jq -e '.status == "healthy"' > /dev/null 2>&1; then
    VERSION=$(echo "$HEALTH" | jq -r '.version')
    UPTIME=$(echo "$HEALTH" | jq -r '.uptime_seconds')
    pass "Health OK (v$VERSION, uptime ${UPTIME}s)"
else
    fail "Health endpoint" "$HEALTH"
fi

# ----- Phase 1: Persistence (Create + Verify) -----
echo ""
echo -e "${YELLOW}[Phase 1] Persistence${NC}"

ID1=$(create_task '{"name":"Test Persistence","command":"echo hello","type":"interval","interval_min":60}')
if [ -n "$ID1" ]; then
    track "$ID1"
    pass "Create interval task (ID: $ID1)"
else
    fail "Create interval task" "no ID returned"
fi

# Verify task appears in list
LIST=$(curl -sf "$API/tasks" 2>/dev/null)
if echo "$LIST" | jq -e ".[] | select(.id == \"$ID1\")" > /dev/null 2>&1; then
    pass "Task appears in task list"
else
    fail "Task in list" "not found"
fi

# ----- Phase 2: Timeout, Retry, Environment -----
echo ""
echo -e "${YELLOW}[Phase 2] Timeout, Retry, Environment${NC}"

ID2=$(create_task '{
    "name":"Test Timeout+Retry",
    "command":"echo $MY_VAR",
    "type":"interval",
    "interval_min":60,
    "timeout_sec":30,
    "retry_count":2,
    "retry_delay_sec":5,
    "env":{"MY_VAR":"hello_from_env"}
}')
if [ -n "$ID2" ]; then
    track "$ID2"
    TASK2=$(get_task "$ID2")
    T_SEC=$(echo "$TASK2" | jq '.timeout_sec')
    R_CNT=$(echo "$TASK2" | jq '.retry_count')
    R_DLY=$(echo "$TASK2" | jq '.retry_delay_sec')
    ENV_VAL=$(echo "$TASK2" | jq -r '.env.MY_VAR // empty')
    [ "$T_SEC" = "30" ] && pass "Timeout set to 30s" || fail "Timeout" "expected 30, got $T_SEC"
    [ "$R_CNT" = "2" ] && pass "Retry count set to 2" || fail "Retry count" "expected 2, got $R_CNT"
    [ "$R_DLY" = "5" ] && pass "Retry delay set to 5s" || fail "Retry delay" "expected 5, got $R_DLY"
    [ "$ENV_VAL" = "hello_from_env" ] && pass "Env variable stored" || fail "Env" "expected hello_from_env, got $ENV_VAL"
else
    fail "Create timeout/retry task" "no ID returned"
fi

# Run task and check env in output
if [ -n "$ID2" ]; then
    RUN_RESP=$(curl -sf -X POST "$API/tasks/$ID2/run" 2>/dev/null)
    sleep 2
    TASK2_AFTER=$(get_task "$ID2")
    LAST_MSG=$(echo "$TASK2_AFTER" | jq -r '.last_result.message // empty')
    if echo "$LAST_MSG" | grep -q "hello_from_env"; then
        pass "Env variable in command output"
    else
        fail "Env in output" "got: $LAST_MSG"
    fi
fi

# ----- Phase 3: Notifications -----
echo ""
echo -e "${YELLOW}[Phase 3] Notifications${NC}"

ID3=$(create_task '{
    "name":"Test Notifications",
    "command":"echo notify_test",
    "type":"interval",
    "interval_min":60,
    "notifications":[{"enabled":true,"type":"webhook","target":"https://httpbin.org/post","on_success":true,"on_failure":true}]
}')
if [ -n "$ID3" ]; then
    track "$ID3"
    TASK3=$(get_task "$ID3")
    NOTIF_COUNT=$(echo "$TASK3" | jq '.notifications | length')
    NOTIF_TYPE=$(echo "$TASK3" | jq -r '.notifications[0].type // empty')
    [ "$NOTIF_COUNT" = "1" ] && pass "Notification config stored" || fail "Notification count" "expected 1, got $NOTIF_COUNT"
    [ "$NOTIF_TYPE" = "webhook" ] && pass "Webhook type correct" || fail "Notification type" "expected webhook, got $NOTIF_TYPE"
else
    fail "Create notification task" "no ID returned"
fi

# ----- Phase 4: Categories and Tags -----
echo ""
echo -e "${YELLOW}[Phase 4] Categories and Tags${NC}"

ID4=$(create_task '{
    "name":"Test Categories",
    "command":"echo tagged",
    "type":"interval",
    "interval_min":60,
    "category":"backup",
    "tags":["critical","daily"],
    "priority":8
}')
if [ -n "$ID4" ]; then
    track "$ID4"
    TASK4=$(get_task "$ID4")
    CAT=$(echo "$TASK4" | jq -r '.category // empty')
    TAG_CNT=$(echo "$TASK4" | jq '.tags | length')
    PRIO=$(echo "$TASK4" | jq '.priority')
    [ "$CAT" = "backup" ] && pass "Category stored" || fail "Category" "expected backup, got $CAT"
    [ "$TAG_CNT" = "2" ] && pass "Tags stored (2)" || fail "Tags count" "expected 2, got $TAG_CNT"
    [ "$PRIO" = "8" ] && pass "Priority stored" || fail "Priority" "expected 8, got $PRIO"
else
    fail "Create category task" "no ID returned"
fi

# Test category filter
FILTERED=$(curl -sf "$API/tasks?category=backup" 2>/dev/null)
FILT_CNT=$(echo "$FILTERED" | jq 'length')
if [ "$FILT_CNT" -ge 1 ]; then
    pass "Category filter returns results"
else
    fail "Category filter" "expected >=1, got $FILT_CNT"
fi

# Test tag filter
TAG_FILT=$(curl -sf "$API/tasks?tag=critical" 2>/dev/null)
TAG_FILT_CNT=$(echo "$TAG_FILT" | jq 'length')
if [ "$TAG_FILT_CNT" -ge 1 ]; then
    pass "Tag filter returns results"
else
    fail "Tag filter" "expected >=1, got $TAG_FILT_CNT"
fi

# Test /categories and /tags endpoints
CATS=$(curl -sf "$API/categories" 2>/dev/null)
if echo "$CATS" | jq -e '.[] | select(. == "backup")' > /dev/null 2>&1; then
    pass "Categories endpoint lists 'backup'"
else
    fail "Categories endpoint" "$CATS"
fi

TAGS=$(curl -sf "$API/tags" 2>/dev/null)
if echo "$TAGS" | jq -e '.[] | select(. == "critical")' > /dev/null 2>&1; then
    pass "Tags endpoint lists 'critical'"
else
    fail "Tags endpoint" "$TAGS"
fi

# ----- Phase 5: Dependencies -----
echo ""
echo -e "${YELLOW}[Phase 5] Dependencies${NC}"

# Create a task that depends on ID1
ID5=$(create_task "{
    \"name\":\"Test Dependency\",
    \"command\":\"echo dep_ok\",
    \"type\":\"interval\",
    \"interval_min\":60,
    \"depends_on\":[\"$ID1\"]
}")
if [ -n "$ID5" ]; then
    track "$ID5"
    TASK5=$(get_task "$ID5")
    DEP_CNT=$(echo "$TASK5" | jq '.depends_on | length')
    [ "$DEP_CNT" = "1" ] && pass "DependsOn stored" || fail "DependsOn" "expected 1, got $DEP_CNT"
else
    fail "Create dependency task" "no ID returned"
fi

# ----- Phase 6: Log Management -----
echo ""
echo -e "${YELLOW}[Phase 6] Log Management${NC}"

# Run task to generate logs
if [ -n "$ID1" ]; then
    curl -sf -X POST "$API/tasks/$ID1/run" > /dev/null 2>&1
    sleep 2

    # Get logs
    LOGS=$(curl -sf "$API/tasks/$ID1/logs" 2>/dev/null)
    LOG_CNT=$(echo "$LOGS" | jq 'length')
    if [ "$LOG_CNT" -ge 1 ]; then
        pass "Logs available ($LOG_CNT entries)"
    else
        fail "Logs" "expected >=1 entries, got $LOG_CNT"
    fi

    # Search logs
    SEARCH=$(curl -sf "$API/tasks/$ID1/logs?search=hello" 2>/dev/null)
    SEARCH_CNT=$(echo "$SEARCH" | jq 'length')
    if [ "$SEARCH_CNT" -ge 1 ]; then
        pass "Log search finds 'hello'"
    else
        fail "Log search" "expected >=1, got $SEARCH_CNT"
    fi

    # CSV export
    CSV=$(curl -sf "$API/tasks/$ID1/logs?format=csv" 2>/dev/null)
    if echo "$CSV" | head -1 | grep -q "time,duration_ms,success,message"; then
        pass "CSV export has correct header"
    else
        fail "CSV export" "wrong header: $(echo "$CSV" | head -1)"
    fi

    # Clear logs
    curl -sf -X POST "$API/tasks/$ID1/logs/clear" > /dev/null 2>&1
    LOGS_AFTER=$(curl -sf "$API/tasks/$ID1/logs" 2>/dev/null)
    AFTER_CNT=$(echo "$LOGS_AFTER" | jq 'length')
    if [ "$AFTER_CNT" = "0" ]; then
        pass "Clear logs works"
    else
        fail "Clear logs" "expected 0, got $AFTER_CNT"
    fi
fi

# ----- Phase 7: Cron Validation -----
echo ""
echo -e "${YELLOW}[Phase 7] Cron Validation${NC}"

# Valid expression
VALID=$(curl -sf -X POST "$API/cron/validate" \
    -H "Content-Type: application/json" \
    -d '{"expr":"*/5 * * * *"}' 2>/dev/null)
if echo "$VALID" | jq -e '.valid == true' > /dev/null 2>&1; then
    NRUNS=$(echo "$VALID" | jq '.next_runs | length')
    pass "Valid cron accepted (next_runs: $NRUNS)"
else
    fail "Valid cron" "$VALID"
fi

# Invalid expression
INVALID=$(curl -sf -X POST "$API/cron/validate" \
    -H "Content-Type: application/json" \
    -d '{"expr":"60 * * * *"}' 2>/dev/null)
if echo "$INVALID" | jq -e '.valid == false' > /dev/null 2>&1; then
    ERR_FIELD=$(echo "$INVALID" | jq -r '.errors[0].field // empty')
    pass "Invalid cron rejected (field: $ERR_FIELD)"
else
    fail "Invalid cron" "$INVALID"
fi

# Create cron task
ID7=$(create_task '{"name":"Test Cron","command":"echo cron_ok","type":"cron","cron_expr":"0 3 * * *"}')
if [ -n "$ID7" ]; then
    track "$ID7"
    TASK7=$(get_task "$ID7")
    NEXT=$(echo "$TASK7" | jq '.next_run_at')
    if [ "$NEXT" != "0" ] && [ "$NEXT" != "null" ]; then
        pass "Cron task scheduled (next_run_at: $NEXT)"
    else
        fail "Cron scheduling" "next_run_at is $NEXT"
    fi
else
    fail "Create cron task" "no ID returned"
fi

# ----- Phase 8: Bulk Operations -----
echo ""
echo -e "${YELLOW}[Phase 8] Bulk Operations${NC}"

# Bulk run
NIDS=${#TASK_IDS[@]}
if [ "$NIDS" -ge 2 ]; then
    IDS_JSON=$(printf '"%s",' "${TASK_IDS[@]:0:2}")
    IDS_JSON="[${IDS_JSON%,}]"
    BULK_RUN=$(curl -sf -X POST "$API/tasks/bulk/run" \
        -H "Content-Type: application/json" \
        -d "{\"ids\":$IDS_JSON}" 2>/dev/null)
    TRIGGERED=$(echo "$BULK_RUN" | jq '.triggered')
    if [ "$TRIGGERED" -ge 2 ] 2>/dev/null; then
        pass "Bulk run triggered $TRIGGERED tasks"
    else
        fail "Bulk run" "expected >=2, got $TRIGGERED"
    fi

    sleep 1

    # Bulk toggle (pause)
    TOGGLE_RESP=$(curl -sf -o /dev/null -w "%{http_code}" -X POST "$API/tasks/bulk/toggle" \
        -H "Content-Type: application/json" \
        -d "{\"ids\":$IDS_JSON}" 2>/dev/null)
    if [ "$TOGGLE_RESP" = "204" ]; then
        pass "Bulk toggle returned 204"
    else
        fail "Bulk toggle" "expected 204, got $TOGGLE_RESP"
    fi
else
    fail "Bulk ops" "not enough tasks ($NIDS)"
fi

# ----- Phase 8: Export/Import -----
echo ""
echo -e "${YELLOW}[Phase 8] Export / Import${NC}"

EXPORT=$(curl -sf "$API/export" 2>/dev/null)
EXP_CNT=$(echo "$EXPORT" | jq 'length')
if [ "$EXP_CNT" -ge 1 ]; then
    pass "Export returns $EXP_CNT tasks"
else
    fail "Export" "expected >=1 tasks, got $EXP_CNT"
fi

# Import a test task
IMPORT_RESP=$(curl -sf -X POST "$API/import" \
    -H "Content-Type: application/json" \
    -d '[{"name":"Imported Task","command":"echo imported","type":"interval","interval_min":30,"category":"test"}]' 2>/dev/null)
IMPORTED=$(echo "$IMPORT_RESP" | jq '.imported')
if [ "$IMPORTED" = "1" ]; then
    pass "Import created 1 task"
    # Find imported task and track for cleanup
    IMPORT_ID=$(curl -sf "$API/tasks" 2>/dev/null | jq -r '.[] | select(.name == "Imported Task") | .id')
    [ -n "$IMPORT_ID" ] && TASK_IDS+=("$IMPORT_ID")
else
    fail "Import" "expected 1, got $IMPORTED"
fi

# ----- Cleanup -----
echo ""
echo -e "${YELLOW}[Cleanup]${NC}"

CLEANUP_COUNT=0
for TID in "${TASK_IDS[@]}"; do
    if [ -z "$TID" ]; then continue; fi
    DEL_CODE=$(curl -sf -o /dev/null -w "%{http_code}" -X DELETE "$API/tasks/$TID" 2>/dev/null)
    if [ "$DEL_CODE" = "204" ]; then
        ((CLEANUP_COUNT++))
    else
        echo -e "  ${RED}Failed to delete $TID (HTTP $DEL_CODE)${NC}"
    fi
done
echo -e "  Deleted $CLEANUP_COUNT test tasks"

# Verify cleanup
REMAINING=$(curl -sf "$API/tasks" 2>/dev/null | jq 'length')
echo -e "  Tasks remaining: $REMAINING"

# ----- Summary -----
echo ""
echo "============================================="
TOTAL=$((PASS + FAIL))
echo -e " Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} out of $TOTAL tests"
echo "============================================="
echo ""

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
