# Benchmark

The benchmark client posts one minor unit in alternating directions between Operating and Reserve in the local demo workspace. All workers share both accounts. Each request uses its own payload-bound key, and workers issue their next request only after receiving the preceding response. The client reports nearest-rank HTTP latency over all attempts. PostgreSQL lock contention is sampled with `pg_stat_activity` every 20 ms. A sample counts API connections with `wait_event_type = 'Lock'`. The metric can miss short waits and includes both row-lock and advisory-lock waits.

Run on an idle disposable instance with both demo balances at their initial levels. The benchmark persists real synthetic journals. Use the same Go and Docker versions for comparisons.

```bash
source scripts/host-env.sh
go build -o /tmp/ledger-bench ./cmd/bench
/tmp/ledger-bench -n 200 -workers 8
/tmp/ledger-bench -n 1000 -workers 1
/tmp/ledger-bench -n 1000 -workers 8
/tmp/ledger-bench -n 1000 -workers 32
```
