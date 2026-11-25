#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077
archive=$(mktemp)
target="ledger_restore_${BASHPID}_${RANDOM}"
db() { docker compose exec -T db "$@"; }
created=0
cleanup() {
  rm -f "$archive"
  if [[ $created == 1 ]]; then
    db dropdb -U postgres "$target" >/dev/null
  fi
}
trap cleanup EXIT
fingerprint() {
  db psql -XAt -v ON_ERROR_STOP=1 -U postgres -d "$1" <<'SQL' | sha256sum | cut -d ' ' -f 1
SET timezone = 'UTC';
COPY (SELECT * FROM public.schema_migrations ORDER BY version) TO STDOUT;
COPY (SELECT * FROM public.workspaces ORDER BY id) TO STDOUT;
COPY (SELECT * FROM public.memberships ORDER BY token_hash) TO STDOUT;
COPY (SELECT * FROM public.accounts ORDER BY workspace_id,id) TO STDOUT;
COPY (SELECT * FROM public.journals ORDER BY sequence) TO STDOUT;
COPY (SELECT * FROM public.entries ORDER BY workspace_id,journal_id,account_id) TO STDOUT;
COPY (SELECT * FROM public.reconciliations ORDER BY workspace_id,id) TO STDOUT;
COPY (SELECT last_value,is_called FROM public.journals_sequence_seq) TO STDOUT;
SQL
}
expected=$(fingerprint ledger)
db pg_dump -U postgres --format=custom ledger > "$archive"
db createdb -U postgres "$target"
created=1
db pg_restore --single-transaction --exit-on-error -U postgres -d "$target" < "$archive"
actual=$(fingerprint "$target")
[[ "$expected" == "$actual" ]] || { echo "Restore fingerprint mismatch. Pause writers before the drill." >&2; exit 1; }
db psql -XAt -v ON_ERROR_STOP=1 -U postgres -d "$target" <<'SQL'
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM public.journals j LEFT JOIN public.entries e
      ON (e.workspace_id,e.journal_id,e.currency)=(j.workspace_id,j.id,j.currency)
    GROUP BY j.workspace_id,j.id HAVING count(e.amount)<2 OR sum(e.amount)<>0
  ) OR EXISTS (
    SELECT 1 FROM public.accounts a LEFT JOIN public.entries e
      ON (a.workspace_id,a.id)=(e.workspace_id,e.account_id)
    GROUP BY a.workspace_id,a.id HAVING a.balance<>coalesce(sum(e.amount),0)
  ) OR NOT has_function_privilege('ledger_app','public.post_ledger(uuid,uuid,text,text,bytea,text,uuid,text[],bigint[])','EXECUTE')
    OR has_table_privilege('ledger_app','public.entries','INSERT')
    OR has_table_privilege('ledger_app','public.entries','UPDATE') THEN
    RAISE EXCEPTION 'restore invariant or permissions check failed';
  END IF;
END;
$$;
SQL
printf 'Restored %s archive bytes. Fingerprint matched and journal, projection, and role checks passed.\n' "$(wc -c < "$archive")"
