package ledger

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

const (
	DemoWorkspace = "10000000-0000-4000-8000-000000000001"
	DemoTreasury  = "10000000-0000-4000-8000-000000000002"
	DemoOperating = "10000000-0000-4000-8000-000000000003"
	DemoReserve   = "10000000-0000-4000-8000-000000000004"
)

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func TokenValid(t string) bool { return tokenPattern.MatchString(t) }

func Seed(ctx context.Context, admin *pgx.Conn, store Store, operator, viewer string) error {
	if !TokenValid(operator) || !TokenValid(viewer) || operator == viewer {
		return errors.New("configure distinct 32-byte hexadecimal demo tokens")
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "INSERT INTO public.workspaces VALUES ($1,'Demo operations') ON CONFLICT DO NOTHING", DemoWorkspace); err != nil {
		return err
	}
	for _, account := range []struct {
		id, name  string
		overdraft bool
	}{
		{DemoTreasury, "Funding control", true}, {DemoOperating, "Operating", false}, {DemoReserve, "Reserve", false},
	} {
		_, err = tx.Exec(ctx, `INSERT INTO public.accounts (workspace_id,id,name,currency,allow_overdraft)
		 VALUES ($1,$2,$3,'USD',$4) ON CONFLICT DO NOTHING`, DemoWorkspace, account.id, account.name, account.overdraft)
		if err != nil {
			return err
		}
	}
	for _, credential := range []struct{ token, role string }{{operator, "operator"}, {viewer, "viewer"}} {
		digest := sha256.Sum256([]byte(credential.token))
		tag, err := tx.Exec(ctx, `INSERT INTO public.memberships VALUES ($1,$2,$3)
		 ON CONFLICT (token_hash) DO UPDATE SET role=EXCLUDED.role
		 WHERE memberships.workspace_id=EXCLUDED.workspace_id`, digest[:], DemoWorkspace, credential.role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("demo token is already assigned to another workspace")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_, _, err = store.Post(ctx, DemoWorkspace, "bootstrap-opening-v1", Posting{Currency: "USD", Memo: "Synthetic opening balances",
		Entries: []Entry{{DemoTreasury, -150000}, {DemoOperating, 100000}, {DemoReserve, 50000}}})
	return err
}
