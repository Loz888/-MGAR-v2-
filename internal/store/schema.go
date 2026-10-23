package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	if err := config.ValidateTestDSN(dsn); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("synthetic database unavailable")
	}
	return &Store{Pool: pool}, nil
}
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := ApplyMigration(ctx, pool, "001_control", migrations.ControlSQL); err != nil {
		return err
	}
	if err := ApplyMigration(ctx, pool, "002_artifact_cas", migrations.ArtifactSQL); err != nil {
		return err
	}
	if err := ApplyMigration(ctx, pool, "003_context_snapshots", migrations.ContextSQL); err != nil {
		return err
	}
	return ApplyMigration(ctx, pool, "004_plan_node_configs", migrations.PlanConfigSQL)
}
func ApplyMigration(ctx context.Context, pool *pgxpool.Pool, version, sql string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext(current_schema()))"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY,checksum text NOT NULL)"); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(sql))
	checksum := hex.EncodeToString(digest[:])
	var previous string
	err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", version).Scan(&previous)
	if err == nil {
		if previous != checksum {
			return errors.New("migration checksum mismatch")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1,$2)", version, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
