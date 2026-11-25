# LedgerCore 1.0.0 draft release notes

LedgerCore demonstrates how a synthetic financial ledger preserves value and history when HTTP clients retry, transactions contend, or processes restart. The first release includes a local Compose stack, a Go API with PostgreSQL-enforced invariants, an operations dashboard, and recovery and benchmark tooling.

For an existing checkout, back up the database, stop writes, apply migration 003 with the migration service, and restart the matching API and dashboard. For a fresh checkout, follow the [README](../README.md).

The scope is a single-primary synthetic ledger with static currency scales and workspace-scoped tokens. The [threat model](threat-model.md), [recovery runbook](recovery.md), [benchmarks](benchmark.md), and [verification evidence](evidence.md) document the measured behavior and boundaries.
