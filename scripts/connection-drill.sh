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
scratch=$(mktemp -d)
key="restart-$(openssl rand -hex 16)"
posting='{"currency":"USD","memo":"Synthetic restart receipt","entries":[{"account":"10000000-0000-4000-8000-000000000002","amount":"-1"},{"account":"10000000-0000-4000-8000-000000000004","amount":"1"}]}'
post() {
  curl -fsS --max-time 6 -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" -H "Idempotency-Key: $key" \
    -H "Content-Type: application/json" -d "$posting" -o "$1" -w '%{http_code}' "$api/v1/journals"
}
trap 'rm -rf -- "$scratch"' EXIT
[[ $(post "$scratch/initial") == 201 ]]
docker compose restart api >/dev/null
wait_ready
[[ $(post "$scratch/api") == 200 ]]
cmp "$scratch/initial" "$scratch/api"
trap 'docker compose start db >/dev/null; rm -rf -- "$scratch"' EXIT
docker compose stop db >/dev/null
[[ $(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$api/healthz") == 200 ]]
[[ $(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$api/readyz") == 503 ]]
[[ $(curl -sS --max-time 6 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" "$api/v1/accounts") == 503 ]]
docker compose start db >/dev/null
wait_ready
curl -fsS --max-time 6 -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" "$api/v1/accounts" >/dev/null
[[ $(post "$scratch/database") == 200 ]]
cmp "$scratch/initial" "$scratch/database"
rm -rf -- "$scratch"
trap - EXIT
echo 'API and PostgreSQL restarts replayed the original receipt. During PostgreSQL interruption readiness was 503 and liveness remained 200.'
