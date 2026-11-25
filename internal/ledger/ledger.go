package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var (
	ErrInvalid  = errors.New("invalid ledger request")
	ErrFunds    = errors.New("insufficient funds")
	ErrConflict = errors.New("idempotency conflict or journal already reversed")
	ErrNotFound = errors.New("record not found")
	ErrBusy     = errors.New("database contention; retry with the same idempotency key")
	ErrRange    = errors.New("amount exceeds supported balance range")
)

type Store struct{ Pool *pgxpool.Pool }

type Principal struct {
	Workspace string
	Role      string
}

type Entry struct {
	Account string `json:"account"`
	Amount  int64  `json:"amount,string"`
}

type Posting struct {
	Currency string  `json:"currency"`
	Memo     string  `json:"memo"`
	Entries  []Entry `json:"entries"`
}

type Journal struct {
	ID         string    `json:"id"`
	Sequence   int64     `json:"sequence,string"`
	Currency   string    `json:"currency"`
	Memo       string    `json:"memo"`
	RecordedAt time.Time `json:"recordedAt"`
	ReversalOf string    `json:"reversalOf,omitempty"`
	Entries    []Entry   `json:"entries"`
}

func ID(v string) bool {
	parsed, err := uuid.Parse(v)
	return err == nil && parsed != uuid.Nil && parsed.String() == v
}

func Currency(v string) bool { return v == "USD" || v == "EUR" || v == "IDR" }

func (s Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	hash := sha256.Sum256([]byte(token))
	var p Principal
	err := s.Pool.QueryRow(ctx, "SELECT workspace_id::text, role FROM public.authenticate_ledger($1)", hash[:]).
		Scan(&p.Workspace, &p.Role)
	return p, classify(err)
}

func (s Store) scope(ctx context.Context, workspace string, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.Pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "SELECT set_config('ledger.workspace',$1,true)", workspace)
	if err != nil {
		rollback(tx)
		return nil, err
	}
	return tx, nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx) // pgx discards the connection if rollback fails.
}

func (s Store) Post(ctx context.Context, workspace, key string, request Posting) (Journal, bool, error) {
	request.Entries = slices.Clone(request.Entries)
	if !validate(&request) {
		return Journal{}, false, ErrInvalid
	}
	return s.write(ctx, workspace, key, request, "")
}

func (s Store) Reverse(ctx context.Context, workspace, key, original string) (Journal, bool, error) {
	if !ID(original) {
		return Journal{}, false, ErrInvalid
	}
	return s.write(ctx, workspace, key, Posting{}, original)
}

func (s Store) write(ctx context.Context, workspace, key string, request Posting, original string) (Journal, bool, error) {
	ctx, span := otel.Tracer("ledgercore/ledger").Start(ctx, "journal.write")
	defer span.End()
	if !ID(workspace) || len(key) == 0 || len(key) > 128 ||
		strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) != -1 {
		return Journal{}, false, ErrInvalid
	}
	payload, err := json.Marshal(struct {
		Posting  Posting
		Original string
	}{request, original})
	if err != nil {
		return Journal{}, false, err
	}
	digest := sha256.Sum256(payload)
	for attempt := 0; attempt < 3; attempt++ {
		result, replay, err := s.writeOnce(ctx, workspace, key, request, original, digest)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || (pgerr.Code != "40P01" && pgerr.Code != "40001") {
			return result, replay, classify(err)
		}
		span.AddEvent("retry after database rollback")
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 15 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return Journal{}, false, ctx.Err()
			}
		}
	}
	return Journal{}, false, ErrBusy
}

func (s Store) writeOnce(ctx context.Context, workspace, key string, request Posting, original string, digest [32]byte) (Journal, bool, error) {
	tx, err := s.scope(ctx, workspace, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Journal{}, false, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", workspace+"/"+key); err != nil {
		return Journal{}, false, err
	}
	var existingID string
	var existingHash []byte
	err = tx.QueryRow(ctx, "SELECT id::text,payload_hash FROM public.journals WHERE workspace_id=$1 AND idempotency_key=$2", workspace, key).
		Scan(&existingID, &existingHash)
	switch {
	case err == nil:
		if !bytes.Equal(existingHash, digest[:]) {
			return Journal{}, false, ErrConflict
		}
		j, err := getJournal(ctx, tx, workspace, existingID)
		return j, true, err
	case !errors.Is(err, pgx.ErrNoRows):
		return Journal{}, false, err
	}
	if original != "" {
		j, err := getJournal(ctx, tx, workspace, original)
		if err != nil {
			return Journal{}, false, err
		}
		if j.ReversalOf != "" {
			return Journal{}, false, ErrInvalid
		}
		request = Posting{Currency: j.Currency, Memo: "Reversal", Entries: j.Entries}
		for i := range request.Entries {
			request.Entries[i].Amount *= -1
		}
	}
	newID, err := uuid.NewRandom()
	if err != nil {
		return Journal{}, false, err
	}
	j := Journal{ID: newID.String(), Currency: request.Currency, Memo: request.Memo, Entries: request.Entries, ReversalOf: original}
	accounts := make([]string, 0, len(j.Entries))
	amounts := make([]int64, 0, len(j.Entries))
	for _, line := range j.Entries {
		accounts = append(accounts, line.Account)
		amounts = append(amounts, line.Amount)
	}
	err = writeEntries(ctx, tx, workspace, key, digest, &j, accounts, amounts)
	if err != nil {
		return Journal{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Journal{}, false, err
	}
	return j, false, nil
}

func writeEntries(ctx context.Context, tx pgx.Tx, workspace, key string, digest [32]byte, j *Journal, accounts []string, amounts []int64) error {
	ctx, span := otel.Tracer("ledgercore/ledger").Start(ctx, "journal.postgres_post")
	span.SetAttributes(attribute.Int("ledger.entry_count", len(accounts)))
	defer span.End()
	return tx.QueryRow(ctx, `SELECT sequence,recorded_at FROM public.post_ledger(
		$1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$9)`,
		workspace, j.ID, j.Currency, key, digest[:], j.Memo, j.ReversalOf, accounts, amounts).Scan(&j.Sequence, &j.RecordedAt)
}

func validate(p *Posting) bool {
	if !Currency(p.Currency) || !utf8.ValidString(p.Memo) || utf8.RuneCountInString(p.Memo) > 200 ||
		strings.ContainsFunc(p.Memo, unicode.IsControl) || len(p.Entries) < 2 || len(p.Entries) > 100 {
		return false
	}
	slices.SortFunc(p.Entries, func(a, b Entry) int { return strings.Compare(a.Account, b.Account) })
	var sum int64 // At most 100 lines of magnitude 9e15.
	for i, line := range p.Entries {
		if !ID(line.Account) || line.Amount == 0 || line.Amount < -9e15 || line.Amount > 9e15 ||
			(i > 0 && p.Entries[i-1].Account == line.Account) {
			return false
		}
		sum += line.Amount
	}
	return sum == 0
}

func classify(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "LC004":
			return ErrNotFound
		case "LC003":
			return ErrFunds
		case "23505":
			return ErrConflict
		case "LC002":
			return ErrInvalid
		case "22003":
			return ErrRange
		case "55P03", "57014":
			return ErrBusy
		}
	}
	return err
}

func getJournal(ctx context.Context, tx pgx.Tx, workspace, id string) (Journal, error) {
	var j Journal
	err := tx.QueryRow(ctx, `SELECT id::text,sequence,currency,memo,recorded_at,coalesce(reversal_of::text,'')
		FROM public.journals WHERE workspace_id=$1 AND id=$2`, workspace, id).
		Scan(&j.ID, &j.Sequence, &j.Currency, &j.Memo, &j.RecordedAt, &j.ReversalOf)
	if err != nil {
		return j, err
	}
	rows, err := tx.Query(ctx, `SELECT account_id::text,amount FROM public.entries
		WHERE workspace_id=$1 AND journal_id=$2 ORDER BY account_id`, workspace, id)
	if err != nil {
		return j, err
	}
	defer rows.Close()
	for rows.Next() {
		var line Entry
		if err := rows.Scan(&line.Account, &line.Amount); err != nil {
			return j, err
		}
		j.Entries = append(j.Entries, line)
	}
	return j, rows.Err()
}

type Account struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Currency       string `json:"currency"`
	Balance        string `json:"balance"`
	AllowOverdraft bool   `json:"allowOverdraft"`
}

func (s Store) Accounts(ctx context.Context, workspace string, through *int64) ([]Account, error) {
	tx, err := s.scope(ctx, workspace, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	query := `SELECT id::text,name,currency,balance::text,allow_overdraft
		FROM public.accounts WHERE workspace_id=$1 ORDER BY name,id LIMIT 200`
	args := []any{workspace}
	if through != nil {
		var latest int64
		err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM public.journals WHERE workspace_id=$1", workspace).Scan(&latest)
		if err != nil {
			return nil, err
		}
		if *through < 0 || *through > latest {
			return nil, ErrInvalid
		}
		query = `SELECT a.id::text,a.name,a.currency,coalesce(h.balance,'0'),a.allow_overdraft
		FROM public.accounts a LEFT JOIN (
		 SELECT e.account_id,sum(e.amount)::text balance FROM public.entries e
		 JOIN public.journals j ON (j.workspace_id,j.id)=(e.workspace_id,e.journal_id)
		 WHERE j.workspace_id=$1 AND j.sequence <= $2 GROUP BY e.account_id
		) h ON h.account_id=a.id WHERE a.workspace_id=$1 ORDER BY a.name,a.id LIMIT 200`
		args = append(args, *through)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Currency, &a.Balance, &a.AllowOverdraft); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s Store) Journals(ctx context.Context, workspace, search, currency string, before int64) ([]Journal, error) {
	tx, err := s.scope(ctx, workspace, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM public.journals
	WHERE workspace_id=$1 AND ($2='' OR currency=$2) AND ($3=0 OR sequence<$3)
	AND ($4='' OR strpos(lower(memo),lower($4))>0 OR strpos(id::text,lower($4))>0)
	ORDER BY sequence DESC LIMIT 25`, workspace, currency, before, search)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	result := make([]Journal, 0, len(ids))
	for _, id := range ids {
		j, err := getJournal(ctx, tx, workspace, id)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, nil
}
