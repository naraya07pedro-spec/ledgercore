# Changelog

## 1.0.0 (draft)

- PostgreSQL-enforced double-entry posting, immutable history, bounded concurrency, compensating reversals, prefix balances, and synthetic statement reconciliation.
- Workspace-scoped tokens and roles. The Next.js dashboard supports posting, inspection, and same-key recovery for ambiguous writes.
- Reconciliation gains optional idempotency receipts. Existing report rows remain readable. Apply migration 003 before running the new API.
- Verification covers PostgreSQL contention, cancellation, restart, migration rollback, independent-container logical restore, and browser interaction.
- Reproducible load workloads cover shared accounts, independent account pairs, separate workspaces, and temporary API interruption.
