// Package testdb creates and removes only its own random synthetic test schemas.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("MGAR_TEST_DATABASE_URL")
	if dsn == "" {
		dir, _ := os.Getwd()
		for {
			b, err := os.ReadFile(filepath.Join(dir, ".local/db.env"))
			if err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(line, "MGAR_TEST_DATABASE_URL=") {
						dsn = strings.TrimPrefix(line, "MGAR_TEST_DATABASE_URL=")
					}
				}
				break
			}
			next := filepath.Dir(dir)
			if next == dir {
				break
			}
			dir = next
		}
	}
	if dsn == "" {
		t.Fatal("synthetic PostgreSQL required; run bootstrap, then source scripts/dev-env.sh")
	}
	return dsn
}
func New(t testing.TB) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 8)
	if _, err = rand.Read(b); err != nil {
		t.Fatal(err)
	}
	schema := "mgar_test_" + hex.EncodeToString(b)
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = s.Pool.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	cfg := s.Pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = s.Pool.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); s.Pool.Close() })
	if err = store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &store.Store{Pool: pool}
}
