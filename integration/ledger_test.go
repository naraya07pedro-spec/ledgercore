package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/naraya07pedro-spec/ledgercore/internal/httpapi"
	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
	"github.com/naraya07pedro-spec/ledgercore/migrations"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const funding = ledger.DemoTreasury
const source = ledger.DemoOperating
const destination = ledger.DemoReserve
const euro = "10000000-0000-4000-8000-000000000005"

type capturedLogs struct {
	mutex sync.Mutex
	data  bytes.Buffer
}

func (l *capturedLogs) Write(p []byte) (int, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.data.Write(p)
}

func (l *capturedLogs) String() string {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.data.String()
}

type fixture struct {
	ctx                         context.Context
	admin                       *pgx.Conn
	store                       ledger.Store
	workspace, operator, viewer string
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T, opening int64) fixture {
	t.Helper()
	adminURL, dsn := os.Getenv("ADMIN_DATABASE_URL"), os.Getenv("DATABASE_URL")
	if adminURL == "" || dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES") == "1" {
			t.Fatal("both PostgreSQL connection URLs are required")
		}
		t.Skip("set ADMIN_DATABASE_URL and DATABASE_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, adminURL)
	must(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	config, err := pgxpool.ParseConfig(dsn)
	must(t, err)
	config.MaxConns = 16
	config.ConnConfig.RuntimeParams["application_name"] = "ledger-integrity-test"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "1500ms"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	must(t, err)
	t.Cleanup(pool.Close)
	f := fixture{ctx: ctx, admin: admin, store: ledger.Store{Pool: pool}, workspace: uuid.NewString(),
		operator: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""),
		viewer:   strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")}
	_, err = admin.Exec(ctx, "INSERT INTO public.workspaces VALUES ($1,'isolated fixture')", f.workspace)
	must(t, err)
	for i, id := range []string{funding, source, destination, euro} {
		currency := "USD"
		if id == euro {
			currency = "EUR"
		}
		_, err = admin.Exec(ctx, `INSERT INTO public.accounts (workspace_id,id,name,currency,allow_overdraft)
			VALUES ($1,$2,$3,$4,$5)`, f.workspace, id, fmt.Sprintf("Account %d", i), currency, id == funding)
		must(t, err)
	}
	for _, credential := range []struct{ token, role string }{{f.operator, "operator"}, {f.viewer, "viewer"}} {
		digest := sha256.Sum256([]byte(credential.token))
		_, err = admin.Exec(ctx, "INSERT INTO public.memberships VALUES ($1,$2,$3)", digest[:], f.workspace, credential.role)
		must(t, err)
	}
	f.post(t, "opening", move(funding, source, opening))
	return f
}

func move(from, to string, amount int64) ledger.Posting {
	return ledger.Posting{Currency: "USD", Entries: []ledger.Entry{{Account: from, Amount: -amount}, {Account: to, Amount: amount}}}
}

func (f fixture) post(t *testing.T, key string, p ledger.Posting) ledger.Journal {
	t.Helper()
	j, replay, err := f.store.Post(f.ctx, f.workspace, key, p)
	must(t, err)
	if replay || j.ID == "" {
		t.Fatal("expected a newly posted journal")
	}
	return j
}

func (f fixture) counts(t *testing.T) (int, int) {
	t.Helper()
	var journals, entries int
	must(t, f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM public.journals WHERE workspace_id=$1),
		(SELECT count(*) FROM public.entries WHERE workspace_id=$1)`, f.workspace).Scan(&journals, &entries))
	return journals, entries
}

func (f fixture) balance(t *testing.T, id string, through *int64) int64 {
	t.Helper()
	accounts, err := f.store.Accounts(f.ctx, f.workspace, through)
	must(t, err)
	for _, a := range accounts {
		if a.ID == id {
			n, err := strconv.ParseInt(a.Balance, 10, 64)
			must(t, err)
			return n
		}
	}
	t.Fatal("account not found")
	return 0
}

func TestContentionAndIdempotency(t *testing.T) {
	f := newFixture(t, 100)
	var wg sync.WaitGroup
	var committed, insufficient atomic.Int32
	start := make(chan struct{})
	for i := range 24 {
		wg.Go(func() {
			<-start
			_, _, err := f.store.Post(f.ctx, f.workspace, fmt.Sprint(i), move(source, destination, 10))
			switch {
			case err == nil:
				committed.Add(1)
			case errors.Is(err, ledger.ErrFunds):
				insufficient.Add(1)
			default:
				t.Errorf("unexpected contender outcome: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if committed.Load() != 10 || insufficient.Load() != 14 || f.balance(t, source, nil) != 0 || f.balance(t, destination, nil) != 100 {
		t.Fatalf("invalid contention result: committed=%d insufficient=%d", committed.Load(), insufficient.Load())
	}
	results := make(chan ledger.Journal, 12)
	var fresh atomic.Int32
	for range 12 {
		wg.Go(func() {
			j, replay, err := f.store.Post(f.ctx, f.workspace, "same-key", move(destination, source, 1))
			if err != nil {
				t.Error(err)
				return
			}
			if !replay {
				fresh.Add(1)
			}
			results <- j
		})
	}
	wg.Wait()
	close(results)
	var identity string
	for j := range results {
		if identity == "" {
			identity = j.ID
		}
		if j.ID != identity {
			t.Fatal("retry returned a different journal")
		}
	}
	if fresh.Load() != 1 || f.balance(t, source, nil) != 1 {
		t.Fatal("retries changed balances more than once")
	}
	_, _, err := f.store.Post(f.ctx, f.workspace, "same-key", move(destination, source, 2))
	if !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	reordered := move(destination, source, 1)
	slices.Reverse(reordered.Entries)
	if _, replay, err := f.store.Post(f.ctx, f.workspace, "same-key", reordered); err != nil || !replay {
		t.Fatalf("canonical replay failed: replay=%v error=%v", replay, err)
	}
}

func TestRollbackAndDatabaseGuards(t *testing.T) {
	f := newFixture(t, 5)
	beforeJ, beforeE := f.counts(t)
	// The funding projection is touched first, then the insufficient account aborts sealing.
	posting := ledger.Posting{Currency: "USD", Entries: []ledger.Entry{{Account: funding, Amount: 1}, {Account: source, Amount: -6}, {Account: destination, Amount: 5}}}
	_, _, err := f.store.Post(f.ctx, f.workspace, "will-rollback", posting)
	if !errors.Is(err, ledger.ErrFunds) {
		t.Fatalf("want funds error, got %v", err)
	}
	if j, e := f.counts(t); j != beforeJ || e != beforeE || f.balance(t, funding, nil) != -5 || f.balance(t, source, nil) != 5 {
		t.Fatal("failed journal left partial state")
	}
	_, _, err = f.store.Post(f.ctx, f.workspace, "mixed", move(source, euro, 1))
	if !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("currency mismatch: %v", err)
	}
	journalID := uuid.NewString()
	tx, err := f.admin.Begin(f.ctx)
	must(t, err)
	defer tx.Rollback(f.ctx)
	for _, line := range []ledger.Entry{{Account: source, Amount: -2}, {Account: destination, Amount: 1}} {
		_, err = tx.Exec(f.ctx, "INSERT INTO public.entries VALUES ($1,$2,$3,'USD',$4)", f.workspace, journalID, line.Account, line.Amount)
		must(t, err)
	}
	_, err = tx.Exec(f.ctx, `INSERT INTO public.journals (workspace_id,id,currency,idempotency_key,payload_hash,memo)
		  VALUES ($1,$2,'USD','direct-unbalanced',decode(repeat('00',32),'hex'),'')`, f.workspace, journalID)
	requirePG(t, err, "LC002")
	must(t, tx.Rollback(f.ctx))
	j := f.post(t, "will-rollback", move(source, destination, 1))
	_, err = f.admin.Exec(f.ctx, "DELETE FROM public.journals WHERE workspace_id=$1 AND id=$2", f.workspace, j.ID)
	requirePG(t, err, "LC001")
	_, err = f.admin.Exec(f.ctx, "UPDATE public.entries SET amount=2 WHERE workspace_id=$1 AND journal_id=$2", f.workspace, j.ID)
	requirePG(t, err, "LC001")
	_, err = f.admin.Exec(f.ctx, "INSERT INTO public.entries VALUES ($1,$2,$3,'USD',1)", f.workspace, j.ID, funding)
	requirePG(t, err, "LC001")
	_, err = f.store.Pool.Exec(f.ctx, "UPDATE public.accounts SET balance=100")
	requirePG(t, err, "42501")
}

func requirePG(t *testing.T, err error, code string) {
	t.Helper()
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != code {
		t.Fatalf("want database error %s, got %v", code, err)
	}
}

func TestSeededHistoryReversalAndReconciliation(t *testing.T) {
	f := newFixture(t, 1000)
	rng := rand.New(rand.NewPCG(31841, 27131))
	fromBalance, toBalance := int64(1000), int64(0)
	var journals []ledger.Journal
	for i := range 120 {
		amount := rng.Int64N(20) + 1
		p := move(source, destination, amount)
		if i > 0 && (fromBalance < amount || (toBalance >= amount && rng.IntN(2) == 0)) {
			p = move(destination, source, amount)
			amount *= -1
		}
		j := f.post(t, fmt.Sprintf("seeded-%d", i), p)
		fromBalance -= amount
		toBalance += amount
		journals = append(journals, j)
		if got := f.balance(t, source, &j.Sequence); got != fromBalance {
			t.Fatalf("seed 31841/27131 step %d: source=%d want %d", i, got, fromBalance)
		}
		if got := f.balance(t, destination, &j.Sequence); got != toBalance {
			t.Fatalf("seed 31841/27131 step %d: destination=%d want %d", i, got, toBalance)
		}
	}
	last := journals[len(journals)-1]
	reversal, replay, err := f.store.Reverse(f.ctx, f.workspace, "reverse", last.ID)
	must(t, err)
	if replay || reversal.ReversalOf != last.ID || reversal.Sequence <= last.Sequence {
		t.Fatal("invalid reversal identity")
	}
	for i, line := range last.Entries {
		if reversal.Entries[i].Account != line.Account || reversal.Entries[i].Amount != -line.Amount {
			t.Fatal("reversal is not exact")
		}
	}
	if got := f.balance(t, source, &last.Sequence); got != fromBalance {
		t.Fatal("reversal rewrote history")
	}
	_, _, err = f.store.Reverse(f.ctx, f.workspace, "double-reverse", last.ID)
	if !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("second reversal: %v", err)
	}
	var failures int
	must(t, f.admin.QueryRow(f.ctx, `SELECT count(*) FROM (
	 SELECT j.id FROM public.journals j LEFT JOIN public.entries e
	 ON (j.workspace_id,j.id,j.currency)=(e.workspace_id,e.journal_id,e.currency)
	 WHERE j.workspace_id=$1 GROUP BY j.id,j.currency HAVING count(e.amount)<2 OR sum(e.amount)<>0
	 ) bad`, f.workspace).Scan(&failures))
	if failures != 0 {
		t.Fatal("persisted journals do not balance")
	}
	a, b, c := journals[0], journals[1], journals[2]
	line := ledger.StatementLine{Journal: b.ID, Currency: "EUR", Credits: "999"}
	report, err := f.store.Reconcile(f.ctx, f.workspace, ledger.Statement{From: a.Sequence, Through: c.Sequence, Lines: []ledger.StatementLine{
		line, line, {Journal: uuid.NewString(), Currency: "USD", Credits: "1"},
	}})
	must(t, err)
	counts := map[string]int{}
	for _, difference := range report.Differences {
		counts[difference.Kind]++
	}
	if counts["missing"] != 2 || counts["duplicate"] != 1 || counts["mismatch"] != 2 || counts["unexpected"] != 1 || report.Matched != 0 {
		t.Fatalf("wrong findings: %v", counts)
	}
	saved, err := f.store.Reconciliations(f.ctx, f.workspace)
	must(t, err)
	if len(saved) != 1 || saved[0].ID != report.ID || len(saved[0].Differences) != 6 {
		t.Fatal("report not persisted")
	}
}

func apiRequest(t *testing.T, client *http.Client, base, method, path, token, key string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	must(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	must(t, err)
	return resp, raw
}

func TestHTTPIsolationPermissionsAndLostAcknowledgment(t *testing.T) {
	f, foreign := newFixture(t, 20), newFixture(t, 20)
	foreignJournal := foreign.post(t, "foreign-secret", move(source, destination, 1))
	var logs capturedLogs
	handler := httpapi.New(f.store, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := httptest.NewServer(handler)
	defer h.Close()
	body, err := json.Marshal(move(source, destination, 2))
	must(t, err)
	for _, check := range []struct {
		method, path, token string
		code                int
	}{
		{"GET", "/v1/accounts", "bad", 401},
		{"POST", "/v1/journals", f.viewer, 403},
		{"POST", "/v1/journals/" + foreignJournal.ID + "/reversal", f.operator, 404},
		{"GET", "/v1/journals?q=" + foreignJournal.ID, f.operator, 200},
		{"GET", "/metrics", "", 200},
	} {
		resp, raw := apiRequest(t, h.Client(), h.URL, check.method, check.path, check.token, "check", body)
		if resp.StatusCode != check.code {
			t.Fatalf("%s returned %d: %s", check.path, resp.StatusCode, raw)
		}
		if strings.Contains(check.path, "?q=") && string(raw) != "[]\n" {
			t.Fatal("foreign journal disclosed")
		}
		if check.path == "/metrics" && !bytes.Contains(raw, []byte("ledger_http_requests_total")) {
			t.Fatal("missing Prometheus metric")
		}
	}
	for _, invalid := range []string{
		`{"currency":"USD","Currency":"EUR","entries":[]}`,
		`{"currency":"USD","entries":[{"account":"a","amount":5}]}`,
		`{"currency":"USD","unexpected":true}`,
		string(body) + " null",
	} {
		resp, _ := apiRequest(t, h.Client(), h.URL, "POST", "/v1/journals", f.operator, "invalid", []byte(invalid))
		if resp.StatusCode != 400 {
			t.Fatalf("malformed posting returned %d", resp.StatusCode)
		}
	}

	var foreignCount int
	tx, err := f.store.Pool.Begin(f.ctx)
	must(t, err)
	_, err = tx.Exec(f.ctx, "SELECT set_config('ledger.workspace',$1,true)", f.workspace)
	must(t, err)
	must(t, tx.QueryRow(f.ctx, "SELECT count(*) FROM public.journals WHERE workspace_id=$1", foreign.workspace).Scan(&foreignCount))
	must(t, tx.Rollback(f.ctx))
	if foreignCount != 0 {
		t.Fatal("row-level security disclosed foreign rows")
	}

	// Record the origin response inside the fault proxy, then close the downstream
	// socket without forwarding its status, headers, or body.
	origin := make(chan int, 1)
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		origin <- rec.Code
		socket, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = socket.Close()
		}
	}))
	defer lost.Close()
	req, err := http.NewRequest("POST", lost.URL+"/v1/journals", bytes.NewReader(body))
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+f.operator)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "lost-response")
	resp, err := lost.Client().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected HTTP acknowledgment to be lost")
	}
	if code := <-origin; code != 201 {
		t.Fatalf("origin status was %d", code)
	}
	resp, raw := apiRequest(t, h.Client(), h.URL, "POST", "/v1/journals", f.operator, "lost-response", body)
	if resp.StatusCode != 200 || resp.Header.Get("Idempotency-Replayed") != "true" || f.balance(t, destination, nil) != 2 {
		t.Fatalf("lost acknowledgment duplicated effect: %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(logs.String(), f.operator) || strings.Contains(logs.String(), f.viewer) {
		t.Fatal("credential leaked in logs")
	}
}

func TestPostingProcessWorker(t *testing.T) {
	workspace := os.Getenv("LEDGER_TEST_CHILD_WORKSPACE")
	if workspace == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	must(t, err)
	config.ConnConfig.RuntimeParams["application_name"] = "ledger-crash-worker"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	must(t, err)
	defer pool.Close()
	_, _, err = (ledger.Store{Pool: pool}).Post(ctx, workspace, "crash", move(source, destination, 1))
	must(t, err)
	t.Fatal("worker should have been terminated before posting")
}

func TestKilledPostingProcessAndLockTimeout(t *testing.T) {
	f := newFixture(t, 5)
	lock, err := f.admin.Begin(f.ctx)
	must(t, err)
	defer lock.Rollback(f.ctx)
	_, err = lock.Exec(f.ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", f.workspace)
	must(t, err)
	cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestPostingProcessWorker$")
	cmd.Env = append(os.Environ(), "LEDGER_TEST_CHILD_WORKSPACE="+f.workspace)
	must(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		must(t, lock.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
		 WHERE application_name='ledger-crash-worker' AND wait_event='advisory'
		 AND query LIKE '%public.post_ledger(%')`).Scan(&waiting))
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never reached post-entry sealing lock")
		}
		_, err = lock.Exec(f.ctx, "SELECT pg_stat_clear_snapshot()")
		must(t, err)
		time.Sleep(20 * time.Millisecond)
	}
	must(t, cmd.Process.Kill())
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected killed process to exit unsuccessfully")
	}
	_, _, err = f.store.Post(f.ctx, f.workspace, "times-out", move(source, destination, 1))
	if !errors.Is(err, ledger.ErrBusy) {
		t.Fatalf("expected bounded lock wait, got %v", err)
	}
	must(t, lock.Rollback(f.ctx))
	if j, e := f.counts(t); j != 1 || e != 2 || f.balance(t, source, nil) != 5 {
		t.Fatal("killed or timed-out transaction changed persisted data")
	}
	f.post(t, "crash", move(source, destination, 1))
}

func TestMigrationReapplyPreservesPostedHistory(t *testing.T) {
	f := newFixture(t, 11)
	for range 2 {
		must(t, migrations.Apply(f.ctx, f.admin))
	}
	if j, e := f.counts(t); j != 1 || e != 2 || f.balance(t, source, nil) != 11 {
		t.Fatal("migration changed posted data")
	}
}

func TestDeadlockRetriesAfterDatabaseRollback(t *testing.T) {
	f := newFixture(t, 50)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	// An out-of-band maintenance transaction acquires account locks in reverse order.
	maintenance, err := f.admin.Begin(f.ctx)
	must(t, err)
	defer maintenance.Rollback(f.ctx)
	_, err = maintenance.Exec(f.ctx, "SET LOCAL deadlock_timeout='5s'")
	must(t, err)
	_, err = maintenance.Exec(f.ctx, "SELECT id FROM public.accounts WHERE workspace_id=$1 AND id=$2 FOR UPDATE", f.workspace, destination)
	must(t, err)

	result := make(chan error, 1)
	go func() {
		_, _, err := f.store.Post(f.ctx, f.workspace, "recovered-deadlock", move(source, destination, 3))
		result <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		must(t, maintenance.QueryRow(f.ctx, `SELECT EXISTS(
		  SELECT 1 FROM pg_stat_activity
		  WHERE application_name='ledger-integrity-test' AND wait_event_type='Lock'
		    AND query LIKE '%public.post_ledger(%'
		)`).Scan(&waiting))
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("posting worker never waited on maintenance lock")
		}
		_, err = maintenance.Exec(f.ctx, "SELECT pg_stat_clear_snapshot()")
		must(t, err)
		time.Sleep(10 * time.Millisecond)
	}
	_, err = maintenance.Exec(f.ctx, "SELECT id FROM public.accounts WHERE workspace_id=$1 AND id=$2 FOR UPDATE", f.workspace, source)
	must(t, err)
	must(t, maintenance.Commit(f.ctx))
	must(t, <-result)
	var retried bool
	for _, span := range recorder.Ended() {
		for _, event := range span.Events() {
			retried = retried || event.Name == "retry after database rollback"
		}
	}
	if !retried || f.balance(t, source, nil) != 47 || f.balance(t, destination, nil) != 3 {
		t.Fatal("deadlock retry did not commit exactly one posting")
	}
	if j, e := f.counts(t); j != 2 || e != 4 {
		t.Fatal("deadlock left partial or duplicate journal entries")
	}
}
