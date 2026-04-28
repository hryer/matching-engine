#!/usr/bin/env bash
# scripts/smoke.sh
#
# End-to-end smoke test for the matching engine.
# Boots `cmd/server`, fires ~30 curl scenarios, asserts each response (HTTP
# status + key body fields), tears the server down. Exits non-zero on any
# failure so it can be wired into CI.
#
# Usage:
#   scripts/smoke.sh                 # boots its own server on :8080
#   PORT=9090 scripts/smoke.sh       # uses a different port
#   ADDR=http://host:8080 scripts/smoke.sh   # tests against an already-running server (no boot)

set -uo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${PORT:-8080}"
ADDR="${ADDR:-http://localhost:${PORT}}"
EXTERNAL_SERVER="${ADDR_EXTERNAL:-0}"
LOG_FILE="${ROOT_DIR}/.smoke.log"

PASS=0
FAIL=0
SERVER_PID=""

red()   { printf "\033[31m%s\033[0m\n" "$*"; }
green() { printf "\033[32m%s\033[0m\n" "$*"; }
blue()  { printf "\033[34m%s\033[0m\n" "$*"; }
gray()  { printf "\033[90m%s\033[0m\n" "$*"; }

cleanup() {
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -TERM "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    # `go run` spawns a compiled child under ~/Library/Caches/go-build/ that
    # outlives the `go run` parent when the parent is SIGTERMed. Target the
    # listener on our PORT directly — robust against caching path changes.
    if [[ "${EXTERNAL_SERVER:-0}" != "1" ]] && lsof -ti:"${PORT:-8080}" >/dev/null 2>&1; then
        lsof -ti:"${PORT:-8080}" | xargs kill -TERM 2>/dev/null || true
        sleep 0.2
        lsof -ti:"${PORT:-8080}" | xargs kill -KILL 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ---- Boot server (unless ADDR points at an external one) ----
if [[ "${ADDR}" == "http://localhost:${PORT}" && "${EXTERNAL_SERVER}" != "1" ]]; then
    # Reap any stale server holding the port. Without this, a leftover
    # process from a previous smoke run accepts our requests and the
    # idempotency cache returns its old state, producing spurious failures.
    if lsof -ti:"${PORT}" >/dev/null 2>&1; then
        gray "[boot] port :${PORT} is in use; reaping prior server"
        lsof -ti:"${PORT}" | xargs kill -TERM 2>/dev/null || true
        for _ in $(seq 1 10); do
            lsof -ti:"${PORT}" >/dev/null 2>&1 || break
            sleep 0.2
        done
        # Belt-and-braces force-kill if it didn't honor SIGTERM.
        if lsof -ti:"${PORT}" >/dev/null 2>&1; then
            lsof -ti:"${PORT}" | xargs kill -KILL 2>/dev/null || true
            sleep 0.2
        fi
    fi

    blue "[boot] starting matching engine on :${PORT}"
    ( cd "$ROOT_DIR" && go run ./cmd/server ) >"$LOG_FILE" 2>&1 &
    SERVER_PID=$!

    ok=0
    for _ in $(seq 1 60); do
        if curl -sS -o /dev/null -w "%{http_code}" "${ADDR}/orderbook" 2>/dev/null | grep -q 200; then
            ok=1
            break
        fi
        sleep 0.3
    done

    if (( ok != 1 )); then
        red "[boot] server did not come up; log:"
        cat "$LOG_FILE" >&2 || true
        exit 1
    fi
    green "[boot] server up at ${ADDR}"
else
    blue "[boot] using external server at ${ADDR}"
fi

# ---- Helpers ----
TMP_BODY=$(mktemp)
TMP_CODE=$(mktemp)
trap 'cleanup; rm -f "$TMP_BODY" "$TMP_CODE"' EXIT

# do_call <METHOD> <PATH> [<BODY>]
do_call() {
    local method="$1" path="$2" body="${3-}"
    if [[ -n "$body" ]]; then
        curl -sS -o "$TMP_BODY" -w "%{http_code}" \
            -X "$method" "${ADDR}${path}" \
            -H 'Content-Type: application/json' \
            --data-binary "$body" >"$TMP_CODE"
    else
        curl -sS -o "$TMP_BODY" -w "%{http_code}" \
            -X "$method" "${ADDR}${path}" >"$TMP_CODE"
    fi
}

# expect <NAME> <EXPECTED_STATUS> [<grep-pattern> ...]
expect() {
    local name="$1" want_status="$2"
    shift 2
    local got_status; got_status=$(cat "$TMP_CODE")
    local body; body=$(cat "$TMP_BODY")
    local ok=1
    if [[ "$got_status" != "$want_status" ]]; then ok=0; fi
    for pattern in "$@"; do
        if ! printf "%s" "$body" | grep -q -- "$pattern"; then ok=0; fi
    done
    if (( ok == 1 )); then
        green "[PASS] $name (HTTP $got_status)"
        gray  "       $body"
        PASS=$((PASS + 1))
    else
        red   "[FAIL] $name — wanted status $want_status with patterns ${*:-<none>}; got $got_status"
        red   "       body: $body"
        FAIL=$((FAIL + 1))
    fi
}

# ---- Scenarios ----
# 1-2: empty state
do_call GET /orderbook
expect "01 empty orderbook"             200 '"bids":\[\]' '"asks":\[\]'

do_call GET /trades
expect "02 empty trades"                200 '"trades":\[\]'

# 3-5: place two resting bids
do_call POST /orders '{"user_id":"alice","client_order_id":"alice-1","side":"buy","type":"limit","price":"100","quantity":"5"}'
expect "03 place limit buy"             201 '"id":"o-1"' '"status":"resting"'

do_call POST /orders '{"user_id":"alice","client_order_id":"alice-2","side":"buy","type":"limit","price":"99","quantity":"10"}'
expect "04 second resting bid"          201 '"id":"o-2"' '"status":"resting"'

do_call GET /orderbook
expect "05 two bids visible"            200 '"price":"100","quantity":"5"' '"price":"99","quantity":"10"'

# 6-8: crossing sell partially fills the top bid
do_call POST /orders '{"user_id":"bob","client_order_id":"bob-1","side":"sell","type":"limit","price":"100","quantity":"3"}'
expect "06 crossing sell fully filled"  201 '"status":"filled"' '"taker_side":"sell"' '"price":"100","quantity":"3"'

do_call GET /trades
expect "07 one trade in history"        200 '"id":"t-1"' '"taker_side":"sell"'

do_call GET /orderbook
expect "08 residual bid 100x2"          200 '"price":"100","quantity":"2"'

# 9: market with no liquidity → 201 + status:rejected (not 4xx)
do_call POST /orders '{"user_id":"carol","client_order_id":"carol-1","side":"buy","type":"market","quantity":"1"}'
expect "09 market w/o liquidity"        201 '"status":"rejected"' '"trades":\[\]'

# 10: STP self-match cancel-newest
do_call POST /orders '{"user_id":"alice","client_order_id":"alice-stp","side":"sell","type":"limit","price":"99","quantity":"1"}'
expect "10 STP cancel-newest"           201 '"status":"cancelled"' '"trades":\[\]'

# 11-12: stop placement + reject-already-satisfied
do_call POST /orders '{"user_id":"dave","client_order_id":"dave-stop","side":"buy","type":"stop","trigger_price":"110","quantity":"2"}'
expect "11 stop armed"                  201 '"status":"armed"'

do_call POST /orders '{"user_id":"dave","client_order_id":"dave-stop-bad","side":"buy","type":"stop","trigger_price":"50","quantity":"2"}'
expect "12 stop trigger satisfied → rejected" 201 '"status":"rejected"'

# 13-15: drive lastTradePrice up, fire the armed stop via cascade
do_call POST /orders '{"user_id":"eve","client_order_id":"eve-1","side":"sell","type":"limit","price":"110","quantity":"5"}'
expect "13 sell ask 110"                201 '"status":"resting"'

do_call POST /orders '{"user_id":"frank","client_order_id":"frank-1","side":"buy","type":"limit","price":"110","quantity":"1"}'
expect "14 trigger trade at 110"        201 '"status":"filled"' '"price":"110","quantity":"1"'

do_call GET /trades
expect "15 cascade trade emitted"       200 '"id":"t-3"' '"taker_order_id":"o-6"'

# 16-18: cancel
do_call DELETE /orders/o-2
expect "16 cancel resting bid"          200 '"status":"cancelled"' '"id":"o-2"'

do_call DELETE /orders/o-2
expect "17 cancel-already-gone"         404 '"code":"not_found"'

do_call DELETE /orders/does-not-exist
expect "18 cancel unknown id"           404 '"code":"not_found"'

# 19-20: idempotency
do_call POST /orders '{"user_id":"alice","client_order_id":"alice-1","side":"buy","type":"limit","price":"100","quantity":"5"}'
expect "19 idempotent same body"        201 '"id":"o-1"'

do_call POST /orders '{"user_id":"alice","client_order_id":"alice-1","side":"sell","type":"market","quantity":"999"}'
expect "20 idempotent diff body cached" 201 '"id":"o-1"'

# 21-25: validation
do_call POST /orders '{"user_id":"x","side":"buy","type":"limit","price":"100","quantity":"5"}'
expect "21 missing client_order_id"     400 '"code":"validation"' 'client_order_id'

do_call POST /orders '{"user_id":"x","client_order_id":"v1","side":"long","type":"limit","price":"100","quantity":"5"}'
expect "22 invalid side"                400 '"code":"validation"'

do_call POST /orders '{"user_id":"x","client_order_id":"v2","side":"buy","type":"limit","quantity":"5"}'
expect "23 limit missing price"         400 '"code":"validation"'

do_call POST /orders '{"user_id":"x","client_order_id":"v3","side":"buy","type":"limit","price":"100","quantity":"9999999999999999"}'
expect "24 quantity > 1e15"             400 '"code":"validation"'

do_call POST /orders '{"user_id":"x","client_order_id":"v4","side":"buy","type":"limit","price":"100","quantity":"1","whatever":"nope"}'
expect "25 unknown JSON field"          400 '"code":"validation"'

# 26: 64 KB body cap → 413
BIG_PAD=$(awk 'BEGIN { for (i = 0; i < 65500; i++) printf "a"; }')
BIG_BODY="{\"user_id\":\"${BIG_PAD}\",\"client_order_id\":\"x\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"quantity\":\"1\"}"
do_call POST /orders "$BIG_BODY"
expect "26 body too large"              413 '"code":"request_too_large"'

# 27-29: query params on GETs
do_call GET '/orderbook?depth=2'
expect "27 orderbook depth=2"           200 '"bids"' '"asks"'

do_call GET '/trades?limit=1'
expect "28 trades limit=1"              200 '"trades"'

do_call GET '/orderbook?depth=abc'
expect "29 orderbook depth=abc"         400 '"code":"validation"'

# ---- Summary ----
echo
echo "=================================="
printf "PASS: \033[32m%d\033[0m   FAIL: \033[31m%d\033[0m\n" "$PASS" "$FAIL"
echo "=================================="
if (( FAIL > 0 )); then
    exit 1
fi
