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

// RestoreCheck writes a probe and must only be used on the disposable restore.
func RestoreCheck(ctx context.Context, store Store, token string) error {
	const probe = "restore-check"
	var unrestrictedRows int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM public.accounts").Scan(&unrestrictedRows); err != nil || unrestrictedRows != 0 {
		return errors.New("restored workspace policies did not fail closed")
	}
	principal, err := store.Authenticate(ctx, token)
	if err != nil || principal.Workspace != DemoWorkspace || principal.Role != "operator" {
		return errors.New("restored operator token failed")
	}
	opening, replay, err := store.Post(ctx, DemoWorkspace, "bootstrap-opening-v1", Posting{Currency: "USD", Memo: "Synthetic opening balances",
		Entries: []Entry{{DemoTreasury, -150000}, {DemoOperating, 100000}, {DemoReserve, 50000}}})
	if err != nil || !replay {
		return errors.New("opening receipt did not replay in restored database")
	}
	accounts, err := store.Accounts(ctx, DemoWorkspace, &opening.Sequence)
	if err != nil || len(accounts) != 3 {
		return errors.New("restored historical account view failed")
	}
	expected := map[string]string{DemoOperating: "100000", DemoReserve: "50000", DemoTreasury: "-150000"}
	for _, account := range accounts {
		if account.Balance != expected[account.ID] {
			return errors.New("restored historical balances differ")
		}
	}
	posting := Posting{Currency: "USD", Memo: "Restore write probe", Entries: []Entry{{DemoTreasury, -1}, {DemoReserve, 1}}}
	j, replay, err := store.Post(ctx, DemoWorkspace, probe, posting)
	if err != nil || replay || j.Sequence <= opening.Sequence {
		return errors.New("fresh restored posting failed")
	}
	again, replay, err := store.Post(ctx, DemoWorkspace, probe, posting)
	if err != nil || !replay || again.ID != j.ID {
		return errors.New("restored probe replay failed")
	}
	return nil
}
