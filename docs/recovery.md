# Recovery runbook

## Logical backup and restore

Run `scripts/backup-restore.sh` against an idle local stack with no concurrent writers. It creates a restricted temporary custom-format archive, restores it into a separate disposable PostgreSQL container and volume, compares deterministic data fingerprints, checks accounting and runtime grants, verifies workspace scope fails closed, replays the opening receipt, checks historical balances, and posts and replays a new transaction there. It removes the archive and the disposable cluster after the check. It leaves the source database intact. The containers share one host. Off-host failover, point-in-time recovery and recovery objectives remain unmeasured.

For a retained backup, set `umask 077` and run `docker compose exec -T db pg_dump -U postgres -Fc ledger > ledger.dump`. Store it as protected data. On another PostgreSQL 17 cluster, provision the database owner and runtime role, create an empty target database, and run `pg_restore --single-transaction --exit-on-error`. A database dump excludes cluster-global roles and credentials. Before routing traffic, run integrity comparisons equivalent to the drill, seed or rotate credentials deliberately, and verify a test posting in a disposable restored database.

## Interrupted posting or database outage

Use `scripts/connection-drill.sh` on the disposable stack. It verifies replay across an API restart, then stops PostgreSQL, checks that liveness stays up while readiness and authenticated database access report 503, starts PostgreSQL, and verifies that authenticated access and replay recover. The integration suite sends SIGKILL while a child process holds uncommitted lines and discards a committed HTTP response in a separate test. For a live ambiguous write, recover the original request and retry with the same idempotency key.

Scrape `/metrics`, use the liveness and readiness endpoints for distinct signals, and correlate `X-Trace-ID` with JSON server logs and optional OTLP traces. Set `OTEL_EXPORTER_OTLP_ENDPOINT` for the API when a trusted OTLP HTTP collector is available. The database pool caps connections at 16 per API process. The initial Compose profile runs one process.

## Migration compatibility and rollback

The migration command applies embedded files in lexical order inside a transaction under an advisory lock. Reapplication verifies each stored checksum. Migration 002 restricts runtime grants and introduces the posting function. Migration 003 adds nullable reconciliation receipt fields and preserves earlier reports. Apply pending migrations before running the matching API. For this version, stop API writes, take a backup, apply migrations, restart the matching API, then check readiness and replay a known request. Run `source scripts/host-env.sh && go run ./cmd/ledger audit` for a read-only owner snapshot that fails on unbalanced journals, invalid reversals or projection drift. A failed transactional migration leaves its prior version in place. Once the migration commits, prefer a forward fix. A schema rollback requires restoring the pre-migration archive and running the corresponding API version. Plan to capture and reconcile any accepted writes newer than that archive before using such a restore outside the demonstration.
