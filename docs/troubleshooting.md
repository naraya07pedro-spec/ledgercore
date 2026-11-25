# Troubleshooting

| Symptom | Check |
| --- | --- |
| API readiness is 503 | Run `docker compose ps -a` and inspect `docker compose logs migrate db`. Confirm the migration job exited successfully and the database is reachable. Liveness and readiness have separate meanings. |
| Compose reports missing variables | Run `scripts/local-env.sh` from the checkout. For an existing persisted database, keep its original passwords or rotate them deliberately within PostgreSQL. |
| Local port already in use | Override `LEDGER_DB_PORT`, `LEDGER_API_PORT` and `LEDGER_DASHBOARD_PORT` in the ignored `.env`. Source `scripts/host-env.sh` again before host-side checks. |
| Dependency download rejects a managed TLS certificate | Supply the correct trusted PEM bundle with `compose.cloud.yaml`. Keep TLS and package checksum verification enabled. |
| Token is rejected | Use the operator or viewer token for the current stack. A viewer can inspect balances and reports. An operator can also post. |
| Transfer timed out | Keep its key and exact payload. Use **Retry same request** in the dashboard or replay the API request for the durable result. |
| Restore fingerprint differs | Stop synthetic writers and repeat the drill. Confirm the source and disposable restore use the intended volumes and roles. |
