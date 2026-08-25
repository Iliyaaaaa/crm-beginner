#!/usr/bin/env bash
# Demonstrates task 3: the log pipeline must survive a database outage with
# no crash and no lost logs.
#
# What it does:
#   1. Sends a small batch of logs every second, continuously.
#   2. Midway through, pauses the Postgres container for 10 seconds -
#      simulating a real outage (frozen, not gracefully stopped).
#   3. Unpauses it.
#   4. Keeps sending a bit longer, then stops and compares:
#      total logs SENT vs total logs actually IN the database.
#
# Run from the project root, with `docker compose up -d` already running.
set -euo pipefail

SERVICE_TAG="outage-demo-$$"   # unique per run, so counts are clean
TOTAL_SENT=0

send_batch() {
  local ts n resp accepted
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  resp=$(
    for n in 1 2 3; do
      printf '{"level":"info","message":"tick %s-%s","service":"%s","timestamp":"%s"}\n' \
        "$(date +%s)" "$n" "$SERVICE_TAG" "$ts"
    done | grpcurl -plaintext -d @ localhost:50051 log.LogService/IngestLogs 2>&1
  )
  accepted=$(echo "$resp" | grep -o '"accepted": *"[0-9]*"' | grep -o '[0-9]*' || echo 0)
  TOTAL_SENT=$((TOTAL_SENT + accepted))
  echo "  sent batch of 3, server accepted: ${accepted:-0} (running total: $TOTAL_SENT)"
}

echo "=== Phase 1: normal traffic (5s) ==="
for i in 1 2 3 4 5; do send_batch; sleep 1; done

echo
echo "=== Phase 2: simulating a database outage (connection actually dropped) ==="
echo ">>> docker compose stop postgres"
docker compose stop postgres
echo "postgres is DOWN - sending logs anyway..."
for i in 1 2 3 4 5 6 7 8 9 10; do send_batch; sleep 1; done
echo ">>> docker compose start postgres"
docker compose start postgres
echo -n "waiting for postgres to report healthy again"
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose ps -q postgres)" 2>/dev/null)" = "healthy" ]; do
  echo -n "."
  sleep 1
done
echo
echo "postgres is back and healthy."

echo
echo "=== Phase 3: a few more seconds so the backlog can drain ==="
for i in 1 2 3 4 5; do send_batch; sleep 1; done

echo
echo "=== waiting for any remaining retries/flushes to settle ==="
sleep 5

echo
echo "=== RESULTS ==="
IN_DB=$(docker compose exec -T postgres psql -U crm -d crm -t -c \
  "SELECT count(*) FROM logs WHERE service = '$SERVICE_TAG';" | tr -d '[:space:]')

echo "Total logs the server ACCEPTED : $TOTAL_SENT"
echo "Total logs actually IN Postgres : $IN_DB"
if [ "$TOTAL_SENT" = "$IN_DB" ]; then
  echo "✅ MATCH - zero logs lost across the outage."
else
  echo "❌ MISMATCH - something was lost (expected if BufferSize was too small for this run)."
fi
