#!/usr/bin/env bash
# End-to-end verification, from a clean database: build, run the rogue
# scenario, print its timeline, roll it back, print the timeline again, run
# the guarded scenario (proving the policy gate blocks), and run go test.
#
# Exits non-zero on the first real failure (build, vet, a server that never
# comes up, or a failing test). The demo drivers themselves are expected to
# report blocked/committed counts, not fail the script.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

ROGUE_ADDR=":18080"
GUARDED_ADDR=":18081"
ROGUE_DB="verify-rogue.db"
GUARDED_DB="verify-guarded.db"
ROGUE_LOG="verify-rogue.log"
GUARDED_LOG="verify-guarded.log"
BIN="./unwind.verify.exe"

cleanup() {
  set +e
  [ -n "${ROGUE_PID:-}" ] && kill "$ROGUE_PID" 2>/dev/null
  [ -n "${GUARDED_PID:-}" ] && kill "$GUARDED_PID" 2>/dev/null
  # Give Windows a moment to release the file handles before deleting.
  sleep 0.5
  rm -f "$ROGUE_DB" "$ROGUE_DB-shm" "$ROGUE_DB-wal"
  rm -f "$GUARDED_DB" "$GUARDED_DB-shm" "$GUARDED_DB-wal"
  rm -f "$ROGUE_LOG" "$GUARDED_LOG" "$BIN"
}
trap cleanup EXIT

wait_for() {
  local url="$1"
  for _ in $(seq 1 50); do
    if curl -s -o /dev/null "$url"; then return 0; fi
    sleep 0.2
  done
  echo "FAIL: $url never came up" >&2
  return 1
}

section() { echo; echo "=== $* ==="; }

section "clean slate"
rm -f "$ROGUE_DB" "$ROGUE_DB-shm" "$ROGUE_DB-wal"
rm -f "$GUARDED_DB" "$GUARDED_DB-shm" "$GUARDED_DB-wal"

section "build"
go build -o "$BIN" .

section "vet"
go vet ./...

section "start rogue server ($ROGUE_ADDR, $ROGUE_DB, default policy.yaml)"
"$BIN" serve --addr "$ROGUE_ADDR" --db "$ROGUE_DB" --policy policy.yaml > "$ROGUE_LOG" 2>&1 &
ROGUE_PID=$!
wait_for "http://localhost${ROGUE_ADDR}/v1/policy"

section "rogue scenario"
ROGUE_OUT="$("$BIN" demo --scenario=rogue --server "http://localhost${ROGUE_ADDR}")"
echo "$ROGUE_OUT"
ROGUE_SID="$(echo "$ROGUE_OUT" | tail -1)"
echo "session: $ROGUE_SID"

section "timeline before rollback"
"$BIN" timeline "$ROGUE_SID" --db "$ROGUE_DB"

section "rollback"
"$BIN" rollback "$ROGUE_SID" --db "$ROGUE_DB"

section "timeline after rollback"
"$BIN" timeline "$ROGUE_SID" --db "$ROGUE_DB"

section "start guarded server ($GUARDED_ADDR, $GUARDED_DB, policy.guarded.yaml)"
"$BIN" serve --addr "$GUARDED_ADDR" --db "$GUARDED_DB" --policy policy.guarded.yaml > "$GUARDED_LOG" 2>&1 &
GUARDED_PID=$!
wait_for "http://localhost${GUARDED_ADDR}/v1/policy"

section "guarded scenario (expect blocks partway through)"
GUARDED_OUT="$("$BIN" demo --scenario=guarded --server "http://localhost${GUARDED_ADDR}")"
echo "$GUARDED_OUT"
GUARDED_SID="$(echo "$GUARDED_OUT" | tail -1)"
echo "session: $GUARDED_SID"

section "go test ./..."
go test ./...

section "done"
echo "all checks passed"
