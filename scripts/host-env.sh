#!/usr/bin/env bash
# Source from the repository root for host-side tests.
set -a
source .env
set +a
export ADMIN_DATABASE_URL="postgres://postgres:${POSTGRES_PASSWORD}@127.0.0.1:${LEDGER_DB_PORT:-15432}/ledger?sslmode=disable"
export DATABASE_URL="postgres://ledger_app:${LEDGER_DB_PASSWORD}@127.0.0.1:${LEDGER_DB_PORT:-15432}/ledger?sslmode=disable"
export LEDGER_API_TEST_URL="http://127.0.0.1:${LEDGER_API_PORT:-18080}"
export LEDGER_DASHBOARD_URL="http://127.0.0.1:${LEDGER_DASHBOARD_PORT:-13000}"
