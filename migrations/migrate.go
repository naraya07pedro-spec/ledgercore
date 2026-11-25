package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS

func Apply(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(189397612);
		CREATE TABLE IF NOT EXISTS public.schema_migrations
			(version text PRIMARY KEY, checksum text NOT NULL)`)
	if err != nil {
		return err
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		sql, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(sql)
		checksum := hex.EncodeToString(digest[:])
		var existing string
		err = tx.QueryRow(ctx, "SELECT checksum FROM public.schema_migrations WHERE version=$1", name).Scan(&existing)
		switch err {
		case nil:
			if checksum != existing {
				return fmt.Errorf("migration %s checksum differs", name)
			}
		case pgx.ErrNoRows:
			if _, err = tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err = tx.Exec(ctx, "INSERT INTO public.schema_migrations VALUES ($1,$2)", name, checksum); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return tx.Commit(ctx)
}
