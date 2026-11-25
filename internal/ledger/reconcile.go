package ledger

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type StatementLine struct {
	Journal  string `json:"journal"`
	Currency string `json:"currency"`
	Credits  string `json:"credits"`
}

type Statement struct {
	From    int64           `json:"from,string"`
	Through int64           `json:"through,string"`
	Lines   []StatementLine `json:"lines"`
}

type Difference struct {
	Journal     string         `json:"journal"`
	Kind        string         `json:"kind"`
	Expected    *StatementLine `json:"expected,omitempty"`
	Observed    *StatementLine `json:"observed,omitempty"`
	Occurrences int            `json:"occurrences,omitempty"`
}

type Reconciliation struct {
	ID          string       `json:"id"`
	RecordedAt  time.Time    `json:"recordedAt"`
	From        int64        `json:"from,string"`
	Through     int64        `json:"through,string"`
	Matched     int          `json:"matched"`
	Differences []Difference `json:"differences"`
}

func (s Store) Reconcile(ctx context.Context, workspace string, statement Statement) (Reconciliation, error) {
	if statement.From < 1 || statement.Through < statement.From ||
		statement.Through-statement.From >= 1000 || statement.Lines == nil || len(statement.Lines) > 1000 {
		return Reconciliation{}, ErrInvalid
	}
	for _, line := range statement.Lines {
		credits, err := strconv.ParseInt(line.Credits, 10, 64)
		if !ID(line.Journal) || !Currency(line.Currency) || err != nil || credits <= 0 || strconv.FormatInt(credits, 10) != line.Credits {
			return Reconciliation{}, ErrInvalid
		}
	}
	tx, err := s.scope(ctx, workspace, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Reconciliation{}, err
	}
	defer rollback(tx)
	var latest int64
	err = tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM public.journals WHERE workspace_id=$1", workspace).Scan(&latest)
	if err != nil {
		return Reconciliation{}, err
	}
	if statement.Through > latest {
		return Reconciliation{}, ErrInvalid
	}
	rows, err := tx.Query(ctx, `SELECT j.id::text,j.currency,sum(e.amount)::text FROM public.journals j
	 JOIN public.entries e ON (e.workspace_id,e.journal_id)=(j.workspace_id,j.id)
	 WHERE j.workspace_id=$1 AND j.sequence BETWEEN $2 AND $3 AND e.amount>0
	 GROUP BY j.id,j.currency,j.sequence ORDER BY j.sequence`, workspace, statement.From, statement.Through)
	if err != nil {
		return Reconciliation{}, err
	}
	expected, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StatementLine])
	if err != nil {
		return Reconciliation{}, err
	}
	r := compare(expected, statement.Lines)
	id, err := uuid.NewRandom()
	if err != nil {
		return Reconciliation{}, err
	}
	r.ID, r.From, r.Through = id.String(), statement.From, statement.Through
	report, err := json.Marshal(r)
	if err != nil {
		return Reconciliation{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO public.reconciliations
	 (workspace_id,id,from_sequence,through_sequence,report) VALUES ($1,$2,$3,$4,$5)
	 RETURNING recorded_at`, workspace, r.ID, r.From, r.Through, report).Scan(&r.RecordedAt)
	if err != nil {
		return Reconciliation{}, err
	}
	return r, tx.Commit(ctx)
}

func compare(expected, observed []StatementLine) Reconciliation {
	r := Reconciliation{Differences: []Difference{}}
	actual := make(map[string][]StatementLine, len(observed))
	index := make(map[string]bool, len(expected))
	for _, line := range observed {
		actual[line.Journal] = append(actual[line.Journal], line)
	}
	for _, line := range expected {
		index[line.Journal] = true
		lines := actual[line.Journal]
		switch {
		case len(lines) == 0:
			r.Differences = append(r.Differences, Difference{Journal: line.Journal, Kind: "missing", Expected: &line})
		case len(lines) == 1 && lines[0] == line:
			r.Matched++
		default:
			for _, observed := range lines {
				if observed != line {
					r.Differences = append(r.Differences, Difference{Journal: line.Journal, Kind: "mismatch", Expected: &line, Observed: &observed})
				}
			}
		}
	}
	seen := make(map[string]bool, len(actual))
	for _, line := range observed {
		if seen[line.Journal] {
			continue
		}
		seen[line.Journal] = true
		if len(actual[line.Journal]) > 1 {
			r.Differences = append(r.Differences, Difference{Journal: line.Journal, Kind: "duplicate", Occurrences: len(actual[line.Journal])})
		}
		if !index[line.Journal] {
			r.Differences = append(r.Differences, Difference{Journal: line.Journal, Kind: "unexpected", Observed: &line})
		}
	}
	return r
}

func (s Store) Reconciliations(ctx context.Context, workspace string) ([]Reconciliation, error) {
	tx, err := s.scope(ctx, workspace, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `SELECT report,recorded_at FROM public.reconciliations
	 WHERE workspace_id=$1 ORDER BY recorded_at DESC,id LIMIT 10`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []Reconciliation{}
	for rows.Next() {
		var r Reconciliation
		var raw []byte
		var recordedAt time.Time
		if err := rows.Scan(&raw, &recordedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		r.RecordedAt = recordedAt
		results = append(results, r)
	}
	return results, rows.Err()
}
