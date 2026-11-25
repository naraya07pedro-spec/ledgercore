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

## Measured run

Measured on 25 November 2026 from source revision `5967e42a92d98a704520e0fcae0f8dd6e39f0ae4`. The client, Go API and PostgreSQL ran in the same Linux x86-64 cloud environment. The host exposed five logical CPUs on an AMD EPYC 9V74 with a cgroup quota of four CPUs and a 32 GiB memory limit. The stack used Docker 29.9.2, Compose 2.40.3, Go 1.27.4 and PostgreSQL 17.9 (Debian). PostgreSQL used local Docker volume storage, 128 MiB shared buffers, 100 maximum connections, `wal_level=replica`, and `fsync`, `synchronous_commit`, and `full_page_writes` enabled. The API pool limit was 16.

The commands above used the clean verification project's API on port 28080 via `-url "$LEDGER_API_TEST_URL"`. A 200-request warm-up at eight workers preceded the recorded runs. Each row represents one run of 1,000 HTTP requests.

| Workers | Elapsed (s) | Commits/s | p50 (ms) | p95 (ms) | p99 (ms) | Failed | Lock-wait samples | Peak waiting connections |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1.647 | 607.3 | 1.53 | 2.24 | 4.09 | 0 / 1,000 | 0 / 82 | 0 |
| 8 | 1.054 | 949.0 | 5.75 | 21.55 | 32.48 | 0 / 1,000 | 51 / 52 | 7 |
| 32 | 1.344 | 743.9 | 31.80 | 85.68 | 127.56 | 0 / 1,000 | 66 / 67 | 15 |

Run IDs in row order

- `f57d581c-93fa-4910-ba1c-959a5e416fb3`
- `78d12553-89d8-470a-989c-c549e18cfc2c`
- `2370baef-d59f-4f94-a658-778d95da5760`

Thirty-two workers produced longer waits and lower throughput than eight workers for this shared-account workload. The short runs measure local contention with one API instance. Repeated trials, independent account sets, cross-workspace load, and sustained performance remain unmeasured.
