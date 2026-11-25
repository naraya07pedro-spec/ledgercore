package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
	"github.com/naraya07pedro-spec/ledgercore/migrations"
)

func TestMonetaryBoundariesAndConcurrentReversal(t *testing.T) {
	f := newFixture(t, 100)
	for i, c := range []ledger.Posting{
		move(source, destination, 9000000000000001),
		move(source, destination, -9223372036854775808),
		move(source, source, 1),
		{Currency: "JPY", Entries: move(source, destination, 1).Entries},
		{Currency: "USD", Entries: []ledger.Entry{{Account: source, Amount: -1}, {Account: destination, Amount: 2}}},
	} {
		if _, _, err := f.store.Post(f.ctx, f.workspace, fmt.Sprint(i), c); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("case %d returned %v", i, err)
		}
	}
	ctx := f.ctx
	for _, prohibited := range []string{
		"DELETE FROM public.journals", "TRUNCATE public.entries", "UPDATE public.reconciliations SET report='{}'",
		"INSERT INTO public.entries VALUES(DEFAULT,DEFAULT,DEFAULT,DEFAULT,DEFAULT)", "SELECT * FROM public.memberships",
		"CREATE TABLE public.shadow(balance bigint)",
	} {
		_, err := f.store.Pool.Exec(ctx, prohibited)
		requirePG(t, err, "42501")
	}
	for _, through := range []int64{-1, 1 << 62} {
		if _, err := f.store.Accounts(ctx, f.workspace, &through); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("invalid prefix: %v", err)
		}
	}
	if got := f.balance(t, source, new(int64)); got != 0 {
		t.Fatalf("baseline should be zero, got %d", got)
	}
	j := f.post(t, "to-reverse", move(source, destination, 5))
	var wg sync.WaitGroup
	var successful, denied atomic.Int32
	for i := range 16 {
		wg.Go(func() {
			_, _, err := f.store.Reverse(ctx, f.workspace, fmt.Sprintf("reversal-%d", i), j.ID)
			switch {
			case err == nil:
				successful.Add(1)
			case errors.Is(err, ledger.ErrConflict):
				denied.Add(1)
			default:
				t.Errorf("reverse: %v", err)
			}
		})
	}
	wg.Wait()
	if successful.Load() != 1 || denied.Load() != 15 || f.balance(t, source, nil) != 100 || f.balance(t, destination, nil) != 0 {
		t.Fatal("reversal race changed the accounting outcome")
	}
	for _, boundary := range []struct {
		account            string
		starting, original int64
	}{
		{destination, 9223372036854775807, 0},
		{funding, -9223372036854775808, -100},
	} {
		var posting ledger.Posting
		if boundary.account == funding {
			posting = move(funding, destination, 1)
		} else {
			posting = move(source, destination, 1)
		}
		func() {
			_, err := f.admin.Exec(ctx, "UPDATE public.accounts SET balance=$3 WHERE workspace_id=$1 AND id=$2", f.workspace, boundary.account, boundary.starting)
			must(t, err)
			defer func() {
				_, err := f.admin.Exec(ctx, "UPDATE public.accounts SET balance=$3 WHERE workspace_id=$1 AND id=$2", f.workspace, boundary.account, boundary.original)
				must(t, err)
			}()
			beforeJ, beforeE := f.counts(t)
			_, _, err = f.store.Post(ctx, f.workspace, "overflow", posting)
			if !errors.Is(err, ledger.ErrRange) {
				t.Fatalf("projected BIGINT boundary returned %v", err)
			}
			afterJ, afterE := f.counts(t)
			if afterJ != beforeJ || afterE != beforeE || f.balance(t, source, nil) != 100 || f.balance(t, boundary.account, nil) != boundary.starting {
				t.Fatal("overflowing projection changed persisted state")
			}
		}()
	}

	must(t, ledger.Audit(ctx, f.admin))

	_, err := f.admin.Exec(ctx, "UPDATE public.accounts SET balance=balance+1 WHERE workspace_id=$1 AND id=$2", f.workspace, source)
	must(t, err)
	// Restore the intentionally modified synthetic projection even on assertion failure.
	defer func() {
		_, err := f.admin.Exec(ctx, "UPDATE public.accounts SET balance=100 WHERE workspace_id=$1 AND id=$2", f.workspace, source)
		must(t, err)
	}()
	if err := ledger.Audit(ctx, f.admin); err == nil || !strings.Contains(err.Error(), "projections=1") {
		t.Fatalf("audit missed owner corruption: %v", err)
	}
	_, err = f.admin.Exec(ctx, "UPDATE public.accounts SET balance=100 WHERE workspace_id=$1 AND id=$2", f.workspace, source)
	must(t, err)
	must(t, ledger.Audit(ctx, f.admin))
}

func TestInterruptedReconciliationAndRetryStorm(t *testing.T) {
	f := newFixture(t, 10)
	j := f.post(t, "statement-line", move(source, destination, 2))
	statement := ledger.Statement{From: j.Sequence, Through: j.Sequence, Lines: []ledger.StatementLine{{Journal: j.ID, Currency: "USD", Credits: "2"}}}
	lock, err := f.admin.Begin(f.ctx)
	must(t, err)
	defer lock.Rollback(f.ctx)
	// A shared lock lets the report read its stable prefix but prevents INSERT.
	_, err = lock.Exec(f.ctx, "LOCK TABLE public.reconciliations IN SHARE MODE")
	must(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 350*time.Millisecond)
	defer cancel()
	_, _, err = f.store.ReconcileWithKey(ctx, f.workspace, "retry-report", statement)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interrupted report returned %v", err)
	}
	must(t, lock.Rollback(f.ctx))
	before, err := f.store.Reconciliations(f.ctx, f.workspace)
	must(t, err)
	if len(before) != 0 {
		t.Fatal("interrupted report persisted")
	}
	var wg sync.WaitGroup
	var fresh atomic.Int32
	ids := make(chan string, 16)
	for range 16 {
		wg.Go(func() {
			report, replay, err := f.store.ReconcileWithKey(f.ctx, f.workspace, "retry-report", statement)
			if err != nil || report.Matched != 1 || len(report.Differences) != 0 {
				t.Errorf("retry did not produce the matched report: %v", err)
				return
			}
			if !replay {
				fresh.Add(1)
			}
			ids <- report.ID
		})
	}
	wg.Wait()
	close(ids)
	saved, err := f.store.Reconciliations(f.ctx, f.workspace)
	must(t, err)
	if fresh.Load() != 1 || len(saved) != 1 || len(ids) != 16 {
		t.Fatal("report retry storm did not yield one receipt")
	}
	for id := range ids {
		if id != saved[0].ID {
			t.Fatal("report replay changed identity")
		}
	}
	statement.Lines[0].Credits = "3"
	_, _, err = f.store.ReconcileWithKey(f.ctx, f.workspace, "retry-report", statement)
	if !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("changed report payload: %v", err)
	}
}

func TestMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	f := newFixture(t, 1)
	dbName := "lc_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	name := pgx.Identifier{dbName}.Sanitize()
	_, err := f.admin.Exec(f.ctx, "CREATE DATABASE "+name)
	must(t, err)
	defer func() { _, err := f.admin.Exec(f.ctx, "DROP DATABASE "+name); must(t, err) }()
	config := f.admin.Config().Copy()
	config.Database = dbName
	db, err := pgx.ConnectConfig(f.ctx, config)
	must(t, err)
	defer db.Close(context.Background())
	_, err = db.Exec(f.ctx, "CREATE TABLE public.accounts (conflicting integer)")
	must(t, err)
	must(t, requireMigrationFailure(f.ctx, db))
	var clean bool
	must(t, db.QueryRow(f.ctx, "SELECT to_regclass('public.workspaces') IS NULL AND to_regclass('public.schema_migrations') IS NULL").Scan(&clean))
	if !clean {
		t.Fatal("failed migration left schema artifacts")
	}
	_, err = db.Exec(f.ctx, "DROP TABLE public.accounts")
	must(t, err)
	must(t, migrations.Apply(f.ctx, db))
	must(t, migrations.Apply(f.ctx, db))
	must(t, ledger.Audit(f.ctx, db))
}

func requireMigrationFailure(ctx context.Context, db *pgx.Conn) error {
	err := migrations.Apply(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "001_ledger.sql") {
		return fmt.Errorf("expected migration DDL to fail: %v", err)
	}
	return nil
}
