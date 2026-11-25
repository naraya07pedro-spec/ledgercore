#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/host-env.sh
out=${1:-$(mktemp -d)}
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
go build -o "$work/bench" ./cmd/bench
api_container=$(docker compose ps -q api)
db_container=$(docker compose ps -q db)
run() {
  local name=$1 pid sampler status=0
  shift
  "$work/bench" -url "$LEDGER_API_TEST_URL" "$@" > "$out/$name.json" &
  pid=$!
  (
    while kill -0 "$pid" 2>/dev/null; do
      docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' "$api_container" "$db_container" || break
    done
  ) > "$out/$name-resources.csv" &
  sampler=$!
  if [[ $name == outage ]]; then
    sleep 2
    if ! docker compose stop -t 0 api >/dev/null; then status=1; fi
    sleep 1
    if ! docker compose start api >/dev/null; then status=1; fi
  fi
  wait "$pid" || status=$?
  wait "$sampler" || true
  [[ $status == 0 ]] || return "$status"
  printf 'Recorded %s\n' "$name"
}
run warmup -workload shared -n 300 -workers 8
for workload in shared independent cross; do
  for workers in 4 16; do
    for trial in 1 2; do
      run "$workload-$workers-$trial" -workload "$workload" -n 2000 -workers "$workers"
    done
  done
done
run sustained -workload independent -n 20000 -workers 8
run outage -workload cross -n 6000 -workers 8 -interruption
printf 'Benchmark artifacts are in %s\n' "$out"
