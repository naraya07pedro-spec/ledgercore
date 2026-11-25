#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/host-env.sh
api=$LEDGER_API_TEST_URL
wait_ready() {
  for _ in {1..30}; do
    if curl -fsS --max-time 2 "$api/readyz" >/dev/null 2>&1; then return; fi
    sleep 1
  done
  echo 'API did not become ready' >&2
  return 1
}
wait_ready
trap 'docker compose start db >/dev/null' EXIT
docker compose stop db >/dev/null
[[ $(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$api/healthz") == 200 ]]
[[ $(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$api/readyz") == 503 ]]
[[ $(curl -sS --max-time 6 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" "$api/v1/accounts") == 503 ]]
docker compose start db >/dev/null
wait_ready
curl -fsS --max-time 6 -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" "$api/v1/accounts" >/dev/null
trap - EXIT
echo 'Database outage returned 503, liveness remained 200, and the API recovered after restart.'
