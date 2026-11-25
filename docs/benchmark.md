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

## V1 release suite

Run on a disposable stack. On the application revision `4f0625f7974e4470ead30539fc66f5e6c5fc72cf`, we used a clean Compose project, the original pinned PostgreSQL 17.9 build, the durability settings above, and the same 16-connection API pool.

```bash
scripts/local-env.sh
docker compose up -d --build
source scripts/host-env.sh
scripts/benchmark-suite.sh /tmp/ledgercore-bench
python3 scripts/summarize-bench.py /tmp/ledgercore-bench
go run ./cmd/ledger audit
```

The suite sends 300 warm-up requests with eight workers. It then runs two 2,000-request trials for every combination of workload and four or 16 workers. Shared uses the same accounts and workspace. Independent uses one distinct account pair per worker in one workspace. Cross uses one account pair and workspace per worker. All amounts are one minor unit, and each attempt has a distinct key. The independent sustained run sends 20,000 requests with eight workers. The cross-workspace interruption run sends 6,000 requests with eight workers and stops the API after two seconds before starting it again after a one-second pause. The interruption client backs off on failures and requires a later acknowledgment. The separate restart drill verifies same-key replay.

Only 201 responses count as acknowledged commits. Status 0 means a transport failure. The audit compares the number of visible journals for each run with acknowledgments and checks the full synthetic ledger for imbalance, projection drift, and invalid reversals after each workload. PostgreSQL connection and lock samples use the same 20 ms cadence. Resource samples use each container's process CPU ticks and proportional set size (PSS). CPU rate uses the same process identities across adjacent samples, so transient processes may be omitted. Samples can include fixture setup and audit work. The runs share a single host and storage path.


### V1 results

Measured on 25 November 2026 on the same AMD EPYC 9V74 environment described above. Docker 29.9.2 used rootless cgroup driver `none`, which produced shared cgroup usage for both containers. Those figures were discarded. The suite was rerun on a fresh synthetic volume with the per-process sampler at tooling revision `3c31c3a157a656bb26700a4c6c62a7c849b22488`. The executable application source was unchanged. The host API port was 28083 and the archive used `GOFLAGS=-buildvcs=false` to build without Git metadata. [Per-trial results](benchmark-results.json)

The rows give the range across both 2,000-request runs. The two counts in the last columns correspond to trials one and two.

| Workload | Workers | Commits/s | p50 (ms) | p95 (ms) | p99 (ms) | Peak lock waiters | Peak busy DB sessions |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| shared | 4 | 776.6–795.6 | 4.04–4.10 | 9.32–9.37 | 15.84–21.82 | 3 / 3 | 4 / 4 |
| shared | 16 | 540.4–593.6 | 14.70–16.29 | 72.92–85.96 | 174.43–262.01 | 15 / 15 | 16 / 16 |
| independent | 4 | 817.6–860.9 | 3.80–3.85 | 8.40–9.81 | 14.67–17.29 | 3 / 4 | 4 / 4 |
| independent | 16 | 489.8–642.4 | 18.63–21.79 | 35.21–80.54 | 182.08–335.33 | 15 / 15 | 16 / 16 |
| cross | 4 | 1016.7–1056.5 | 2.70–3.09 | 6.64–7.43 | 12.38–14.66 | 0 / 0 | 4 / 4 |
| cross | 16 | 1262.5–1266.5 | 8.39–8.61 | 21.79–25.30 | 46.95–94.39 | 0 / 0 | 16 / 15 |

The 20,000-request independent run committed all 20,000 requests in 28.11 seconds (711.5 commits/s). Latency p50, p95 and p99 were 8.88, 19.54 and 46.52 ms. The run peaked at 8 busy API database sessions and 8 sampled lock waiters. Sampled process peaks were 0.64 API CPU cores and 26.34 MiB PSS, and 1.07 PostgreSQL CPU cores and 129.63 MiB PSS. These are sampled peaks, with CPU attribution limited to process identities present in adjacent samples.

The interruption trial recorded 5,488 acknowledged commits in 7.96 seconds and 512 transport failures during the deliberate API stop. Latency p50, p95 and p99 across all attempts were 4.48, 11.48 and 23.26 ms. The database recorded exactly 5,488 journals for the run. New commits resumed after the stop, and the post-suite audit and independent restore passed. The normal repeated and sustained trials recorded zero failures across 44,000 attempts.

With 16 workers, the shared and independent workloads reached 15 sampled lock waiters and had worse p99 latency than at four workers. Cross-workspace runs recorded zero sampled lock waiters. This aligns with serialization in one workspace's sealing phase, which remains even with disjoint accounts. The samples combine PostgreSQL lock types and could miss waits shorter than the sampling interval. These trials used tiny postings, a local synthetic database, one API process and 4 effective CPU cores. They establish no hosted capacity objective.
