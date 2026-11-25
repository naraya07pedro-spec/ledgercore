# LedgerCore engineering standards

- Use the existing checkout inside Codex Cloud. Keep real identities, credentials, funds, and payment integrations out of this synthetic project.
- Represent money in signed integer minor units. Encode int64 amounts and sequence values as JSON strings at the API boundary. Keep supported currencies and their scales explicit.
- Preserve immutable posted history. Correct an error with a compensating transaction. Keep all account projections and journal entries in one database transaction.
- Acquire account locks in ascending UUID order before insertion. Keep workspace sequence allocation in the sealing phase. Preserve payload-bound idempotency and replay behavior across API changes.
- Scope database operations to the authenticated workspace within the same transaction. Apply read/write role checks at the API. Preserve runtime database grants and keep migration credentials out of request handling.
- Add new numbered SQL migrations for schema changes after release. Run migrations against existing data and a clean database. Plan compatibility before changing database boundaries.
- Run live PostgreSQL tests for transaction behavior, including rollback, concurrency, and failure recovery. Preserve all assertions and report skips separately.
- Keep benchmarks reproducible and label results with their revision, configuration, workload and sample size. Capture screenshots from running software with synthetic records.
- Keep authorization headers, tokens, database URLs, and arbitrary request bodies out of logs, screenshots, fixtures, and version control.
