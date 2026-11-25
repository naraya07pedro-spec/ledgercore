package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
)

type lane struct {
	token  string
	bodies [2][]byte
}

func payloads(token, runID, from, to string) (lane, error) {
	l := lane{token: token}
	for i, pair := range [][2]string{{from, to}, {to, from}} {
		body, err := json.Marshal(ledger.Posting{Currency: "USD", Memo: "Synthetic load " + runID, Entries: []ledger.Entry{
			{Account: pair[0], Amount: -1}, {Account: pair[1], Amount: 1},
		}})
		if err != nil {
			return lane{}, err
		}
		l.bodies[i] = body
	}
	return l, nil
}

func prepare(ctx context.Context, db *pgx.Conn, kind string, workers int, runID, token string) ([]lane, error) {
	if kind == "shared" {
		l, err := payloads(token, runID, ledger.DemoOperating, ledger.DemoReserve)
		if err != nil {
			return nil, err
		}
		lanes := make([]lane, workers)
		for i := range lanes {
			lanes[i] = l
		}
		return lanes, nil
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	store := ledger.Store{Pool: pool}
	lanes := make([]lane, 0, workers)
	var workspace, funding string
	for i := range workers {
		if i == 0 || kind == "cross" {
			workspace, funding = uuid.NewString(), uuid.NewString()
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return nil, err
			}
			token = hex.EncodeToString(secret)
			hash := sha256.Sum256([]byte(token))
			if _, err := db.Exec(ctx, "INSERT INTO public.workspaces VALUES ($1,'Synthetic benchmark')", workspace); err != nil {
				return nil, err
			}
			if _, err := db.Exec(ctx, "INSERT INTO public.memberships VALUES ($1,$2,'operator')", hash[:], workspace); err != nil {
				return nil, err
			}
			if _, err := db.Exec(ctx, `INSERT INTO public.accounts (workspace_id,id,name,currency,allow_overdraft)
			 VALUES ($1,$2,'Funding control','USD',true)`, workspace, funding); err != nil {
				return nil, err
			}
		}
		from, to := uuid.NewString(), uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO public.accounts (workspace_id,id,name,currency)
		 VALUES ($1,$2,'Lane A','USD'),($1,$3,'Lane B','USD')`, workspace, from, to); err != nil {
			return nil, err
		}
		if _, _, err := store.Post(ctx, workspace, "bench-fund-"+from, ledger.Posting{Currency: "USD", Memo: "Synthetic benchmark funding", Entries: []ledger.Entry{
			{Account: funding, Amount: -200000}, {Account: from, Amount: 100000}, {Account: to, Amount: 100000},
		}}); err != nil {
			return nil, err
		}
		l, err := payloads(token, runID, from, to)
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, l)
	}
	return lanes, nil
}
