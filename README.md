# LedgerCore

LedgerCore is an open-source, synthetic financial ledger. The Go API posts balanced journals to PostgreSQL, enforces available funds under contention, and serves a Next.js operations dashboard. It has no payment, banking, or cryptocurrency integrations.

## Run locally

Requires Docker with Compose v2 and OpenSSL. [.env.example](.env.example) documents optional settings and the generated values. The stack uses local ports 13000 for the dashboard, 18080 for the API, and 15432 for PostgreSQL.

```bash
scripts/local-env.sh
docker compose up -d --build
curl -fsS http://127.0.0.1:18080/readyz
```

If your build network requires an organization-managed TLS CA, pass its trusted PEM bundle with the optional Compose override.

```bash
LEDGER_BUILD_CA_BUNDLE=/path/to/trusted-ca.pem \
  docker compose -f compose.yaml -f compose.cloud.yaml up -d --build
```

Open **http://127.0.0.1:13000** and use the `LEDGER_OPERATOR_TOKEN` from your local `.env`. The initial USD workspace contains Funding control, Operating, and Reserve accounts. Post a transfer of 275 minor units from Operating to Reserve with a unique memo. In **Journal**, filter by memo, inspect both entries, and note the posted sequence. Reverse the transaction and check the original remains visible. In **Overview**, use the original sequence for a historical balance. The viewer token permits inspection without posting privileges.

Read the [API reference](docs/api.md) for exact request examples and statement JSON. [Architecture and accounting](docs/architecture.md) explains the transaction boundary. [Recovery runbook](docs/recovery.md) covers restore, outage drills and migration rollout. See the [threat model](docs/threat-model.md), [benchmark report](docs/benchmark.md), and [verification evidence](docs/evidence.md).

## Verify

For host-side checks use Go 1.27.4 and Node 24.22.3. A running Compose stack supplies real PostgreSQL and the production dashboard build.

```bash
source scripts/host-env.sh
go vet ./...
REQUIRE_POSTGRES=1 go test -race -count=1 -v ./...
(cd dashboard && npm ci && npx playwright install chromium && npm test)
scripts/connection-drill.sh
scripts/backup-restore.sh
go run ./cmd/bench -n 1000 -workers 8
go run ./cmd/ledger audit
```

`docker compose down` stops the stack and retains PostgreSQL data. Only use `docker compose down --volumes` to discard a disposable local dataset. The repository uses the [Apache 2.0 license](LICENSE).


## Reviewer walkthrough

Follow one synthetic transfer from the dashboard through the API into the database. Inspect both journal lines, reverse it, and reconstruct the account balances through the original sequence. To see the locking behavior, run `REQUIRE_POSTGRES=1 go test -run TestContentionAndIdempotency -v ./integration` after sourcing the host settings. The test starts 24 competing writers against funds sufficient for ten. Run the [recovery drills](docs/recovery.md) to see a committed HTTP receipt survive both service restarts and a fresh PostgreSQL restore. Compare workloads with `scripts/benchmark-suite.sh` on a disposable stack.

See the [contribution guide](CONTRIBUTING.md), [security reporting](SECURITY.md), [troubleshooting guide](docs/troubleshooting.md), [changelog](CHANGELOG.md) and [draft release notes](docs/release-notes.md).
