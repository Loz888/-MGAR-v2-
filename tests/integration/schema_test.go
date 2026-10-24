package integration

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func TestSchemaMigrationAndConstraints(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workflow_runs", "orchestrator_leases", "plans", "plan_nodes", "plan_edges", "task_instances", "task_inputs", "artifacts", "artifact_verdicts", "outbox_events", "inbox_receipts", "model_calls", "tool_invocations", "run_budgets", "approval_requests", "workflow_events"} {
		var exists bool
		if err := s.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Fatalf("missing table %s %v", table, err)
		}
	}
	var run string
	if err := s.Pool.QueryRow(ctx, "INSERT INTO workflow_runs(goal) VALUES('synthetic') RETURNING run_id").Scan(&run); err != nil {
		t.Fatal(err)
	}
	for name, entry := range map[string][2]string{
		"invalid role":    {"INSERT INTO task_instances(run_id,node_key,role) VALUES($1,'x','admin')", "23514"},
		"negative budget": {"INSERT INTO run_budgets(run_id,max_cost_usd) VALUES($1,-1)", "23514"},
		"invalid FK":      {"INSERT INTO plan_edges(run_id,plan_version,from_key,to_key) VALUES($1,999,'missing','other')", "23503"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Pool.Exec(ctx, entry[0], run)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != entry[1] {
				t.Fatalf("expected constraint %s, got %v", entry[1], err)
			}
		})
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO workflow_events(run_id,entity_type,entity_id,entity_revision,kind) VALUES($1,'workflow',$1,0,'created')", run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO workflow_events(run_id,entity_type,entity_id,entity_revision,kind) VALUES($1,'workflow',$1,0,'created')", run); err == nil {
		t.Fatal("duplicate event accepted")
	}
	var n int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != 5 {
		t.Fatalf("migration repeated: %d %v", n, err)
	}
}

func TestMigrationRollbackAndChecksum(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	if err := store.ApplyMigration(ctx, s.Pool, "bad", "CREATE TABLE should_rollback(id int);SELECT 1/0;"); err == nil {
		t.Fatal("migration should fail")
	}
	var absent bool
	_ = s.Pool.QueryRow(ctx, "SELECT to_regclass('should_rollback') IS NULL").Scan(&absent)
	if !absent {
		t.Fatal("partial migration persisted")
	}
	if err := store.ApplyMigration(ctx, s.Pool, "001_control", "SELECT 1"); err == nil {
		t.Fatal("modified migration checksum accepted")
	}
}
