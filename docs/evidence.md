# Verification evidence

The Playwright suite captures the running dashboard backed by PostgreSQL. Set `LEDGER_SCREENSHOT_DIR` to regenerate screenshots during the browser test.

- [Operator overview](screenshots/overview.png)
- [Journal with expanded debit and credit](screenshots/journal.png)
- [Persisted reconciliation finding](screenshots/reconciliation.png)
- [Mobile viewer](screenshots/mobile.png)

## Executed checks

The engine and dashboard were built from the application source recorded in `5967e42a92d98a704520e0fcae0f8dd6e39f0ae4`. The test database started in a fresh Compose project, and the screenshots came from that project's Next.js production build. The recovery drill ran after the backup script correction in that revision. Both migrations also ran against the earlier development database that already contained posted history.

| Check | Result |
| --- | --- |
| Go race-enabled suite with real PostgreSQL | Eight integration tests and six validation cases passed. The process helper skipped when invoked as a top-level test and ran as a child in the SIGKILL test. |
| Contention and replay | Exactly ten of 24 spend attempts committed, 14 were denied for insufficient funds, and 12 matching retry attempts produced one posting. |
| Operational fault injection | SIGKILL during sealing, lost HTTP acknowledgment, PostgreSQL lock timeout, actual deadlock with bounded retry, and a database restart all behaved as asserted. |
| Backup and restore | The 44,631-byte archive restored, fingerprints matched, and journal, projection, and role checks passed. The drill compares the sequence's durable value and called flag. |
| Observability | Prometheus exposition responded. A real OTLP HTTP receiver received the trace spans with the same trace ID as the API header and structured logs. |
| Node and browser | Clean install and TypeScript checks passed. Both Playwright workflows passed with Chromium 156.0.8078.79. npm audit reported zero vulnerabilities at scan time. |
| Go dependency checks | Module verification and vet passed. `govulncheck` v1.10.0 reported no findings with a local index over 4,885 upstream Go OSV advisories at [vulndb revision d8a8b1a](https://github.com/golang/vulndb/tree/d8a8b1ad05e834fdd40b66c80c618ec7a557ba53). This workspace blocked the live vulnerability endpoint. |
| Load | The 200-request warm-up and 3,000 measured requests completed successfully. See the [benchmark report](benchmark.md). |

The test suite and local drills are reproducible from the commands in the [README](../README.md). The [threat model](threat-model.md) and [recovery runbook](recovery.md) describe the deployment and backup boundaries.

## V1 release verification

The local release suite used application revision `4f0625f7974e4470ead30539fc66f5e6c5fc72cf`, a fresh source archive extracted under `umask 077`, new synthetic credentials, and a fresh PostgreSQL volume. The evidence commit adds reports and genuine screenshots to that source. The screenshots above were recaptured from this run.

| Gate | Executed result |
| --- | --- |
| Go quality | Formatting, module checksums, `go vet`, and `REQUIRE_POSTGRES=1 go test -race -count=1 -v ./...` passed. Eleven integration tests and six validation cases passed. The subprocess helper skipped at the top level and was executed by the SIGKILL test. |
| Accounting and authorization | The seeded 120-posting test compared balances at every published sequence. Integration checks covered PostgreSQL projection overflow and underflow, rollback, illegal direct SQL, future and negative historical cutoffs, real lock contention, idempotency and workspace scoping. One of 16 concurrent reversals committed and 15 conflicted. An intentionally corrupted owner projection was detected and restored. |
| Reconciliation and migration | A lock interrupted the report before persistence. Sixteen same-key retries yielded one matched report, and a different payload with the same key conflicted. A DDL conflict in an isolated database rolled back migration artifacts, then the corrected migration applied and reapplied. The development database also upgraded in place with prior records. |
| Browser and startup | The production Compose build, API readiness, TypeScript checks, and all four Playwright tests passed. Chromium 156.0.8078.79 exercised keyboard focus, mobile layout, posting, reversal, reconciliation, cross-origin rejection, body size and concurrency limits, and retry after a simulated lost acknowledgment. |
| Dependencies | `npm audit --audit-level=moderate` returned zero known findings. The cloud blocked the public Go advisory endpoint, so local `govulncheck` v1.10.0 used the upstream Go OSV snapshot dated 24 November 2026 at [d8a8b1a](https://github.com/golang/vulndb/tree/d8a8b1ad05e834fdd40b66c80c618ec7a557ba53). It reported no findings over that snapshot. Results apply to the advisories available at scan time. |
| Service interruption | `scripts/connection-drill.sh` replayed the identical durable posting receipt across API and PostgreSQL restarts. During database interruption readiness returned 503 while liveness stayed 200. Existing tests also exercised a killed posting process, a dropped acknowledgment, a timeout, and an actual deadlock. |
| Independent restore | The 47,589-byte fresh-suite backup and the 3,429,285-byte post-benchmark backup restored into separate PostgreSQL containers. Journal/entry, workspace, role, projection, statement and sequence fingerprints matched. The runtime checked historical balances, workspace policies, replay and fresh posting. |
| Load and post-load verification | 44,000 normal measured attempts committed, plus 300 warm-up commits. The deliberate interruption run returned 512 transport failures and 5,488 commits with matching persisted counts and post-restart success. The owner audit passed after the load. See the [benchmark report](benchmark.md). |
| PostgreSQL catalog review | The only application `SECURITY DEFINER` functions were authenticate, post and seal. All were owned by postgres with a fixed qualified search path and no PUBLIC execute. The runtime could call authenticate and post, and had no superuser, bypass-RLS, create-role or create-database privileges. All four workspace data tables had row-level security enabled. |


### Findings and repairs

| Reproduced issue | Root cause and correction |
| --- | --- |
| Retrying a reconciliation could persist another report | Reconciliations originally lacked durable receipts. Migration 003 adds a workspace-scoped payload digest and key. The interruption and retry-storm test verifies exactly one persisted result. |
| Duplicate reversals could be rejected as insufficient funds | The unique constraint was reached after updating projections. The API transaction now serializes by original journal and checks prior reversal before touching balances. The concurrent test requires 15 conflicts. |
| Slow or cross-origin browser calls could reach the proxy without an ingress budget | The old deadline began after body buffering. The proxy now enforces origin, request size, deadlines, bearer shape, and at most 64 in-flight calls per process. The browser suite fills the budget and verifies recovery. |
| Duplicate submission and ambiguous outcomes could confuse operators | A synchronous submission guard blocks overlap. The UI keeps the original payload and key for uncertain outcomes. Playwright dropped an actual committed response and verified a 200 replay with the same key and one journal. |
| Fresh setup could fail when PostgreSQL could not read a mode-0700 bind-mounted init script | Docker now copies the init script with a fixed executable mode into the pinned PostgreSQL build. The clean extraction under `umask 077` passed. |
| The browser assertion could select an earlier reconciliation report | The test scopes the exact mismatch to the new report and passed against the clean build. |
| The earlier backup drill shared PostgreSQL with its source | The revised drill provisions an independent disposable container and volume, verifies the restored data, and exercises workspace policies, historical reads, durable replay, and new writes. |

A local clean build first failed under disk pressure. After clearing disposable build caches, the release suite passed on the fresh stack. One earlier hosted run failed in the combined browser setup/test step and skipped recovery. Its public annotation showed exit code 1, with detailed logs unavailable from this environment. The parallel run on that revision passed. The workflow now reports type checking, browser installation, and browser tests separately. The rootless Docker resource figures from the first load suite were discarded after both containers reported the same cgroup. The completed process-sampled rerun is the V1 report. Off-host failover, point-in-time recovery, container image CVE scanning, and hosted capacity were outside the executed checks.
