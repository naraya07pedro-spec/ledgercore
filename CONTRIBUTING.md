# Contributing

Use a disposable local Compose stack and synthetic data. Read [AGENTS.md](AGENTS.md) and the [accounting model](docs/architecture.md) before changing posting, migrations, or authorization.

1. Create a feature branch from the current development base.
2. Reuse the versioned migrations. Leave an applied migration untouched and add a new numbered file for schema changes.
3. Cover behavior changes with focused tests. Financial and concurrency changes need live PostgreSQL assertions.
4. Run the verification commands in the [README](README.md) and inspect the workflow results before requesting review.
5. Explain the change, migration compatibility, verification results and failure modes in the pull request. Keep real credentials and records out of examples, logs, and screenshots.
