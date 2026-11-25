#!/usr/bin/env bash
set -euo pipefail
psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv app_password LEDGER_DB_PASSWORD
CREATE ROLE ledger_app LOGIN PASSWORD :'app_password';
SQL
