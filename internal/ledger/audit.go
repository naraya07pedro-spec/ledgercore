package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Audit uses one database snapshot and reports counts without exposing record
// identifiers. Run with the database owner on a disposable copy or idle ledger.
func Audit(ctx context.Context, db *pgx.Conn) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, "SET LOCAL row_security=off"); err != nil {
		return err
	}
	var invalid, projection, reversals int64
	err = tx.QueryRow(ctx, `WITH totals AS (
		SELECT j.workspace_id,j.id,count(e.amount) AS lines,coalesce(sum(e.amount),0) AS total
		FROM public.journals j LEFT JOIN public.entries e
			ON (j.workspace_id,j.id,j.currency)=(e.workspace_id,e.journal_id,e.currency)
		GROUP BY j.workspace_id,j.id
	), balances AS (
		SELECT workspace_id,account_id,sum(amount) AS total FROM public.entries GROUP BY workspace_id,account_id
	)
	SELECT (SELECT count(*) FROM totals WHERE lines NOT BETWEEN 2 AND 100 OR total<>0),
	       (SELECT count(*) FROM public.accounts a LEFT JOIN balances b
	         ON (a.workspace_id,a.id)=(b.workspace_id,b.account_id)
	         WHERE a.balance<>coalesce(b.total,0) OR (NOT a.allow_overdraft AND a.balance<0)),
	       (SELECT count(*) FROM public.journals j JOIN public.journals original
	         ON (j.workspace_id,j.reversal_of)=(original.workspace_id,original.id)
	         WHERE original.reversal_of IS NOT NULL OR EXISTS (
	           SELECT account_id,amount FROM public.entries e WHERE (e.workspace_id,e.journal_id)=(j.workspace_id,j.id)
	           EXCEPT SELECT account_id,-amount FROM public.entries e WHERE (e.workspace_id,e.journal_id)=(j.workspace_id,original.id)
	         ) OR (SELECT lines FROM totals t WHERE (t.workspace_id,t.id)=(j.workspace_id,j.id))
	             <> (SELECT lines FROM totals t WHERE (t.workspace_id,t.id)=(j.workspace_id,original.id)))`).Scan(&invalid, &projection, &reversals)
	if err != nil {
		return err
	}
	if invalid != 0 || projection != 0 || reversals != 0 {
		return fmt.Errorf("invariant failure: journals=%d projections=%d reversals=%d", invalid, projection, reversals)
	}
	return nil
}
