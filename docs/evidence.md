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
