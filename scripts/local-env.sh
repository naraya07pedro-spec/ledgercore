#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -e .env ]]; then
  echo 'Using existing .env'
  exit 0
fi
umask 077
set -o noclobber
{
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 32)"
  printf 'LEDGER_DB_PASSWORD=%s\n' "$(openssl rand -hex 32)"
  printf 'LEDGER_OPERATOR_TOKEN=%s\n' "$(openssl rand -hex 32)"
  printf 'LEDGER_VIEWER_TOKEN=%s\n' "$(openssl rand -hex 32)"
} > .env
echo 'Created .env with local synthetic credentials'
