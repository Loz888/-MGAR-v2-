package integration

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"testing"
)

func TestSchemaCatalogTerminalAndReader(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	for _, column := range []string{"owner", "epoch", "lease_until", "revision", "retry_count", "attempts", "result_artifact_id"} {
		var exists bool
		err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='task_instances' AND column_name=$1)", column).Scan(&exists)
		if err != nil || !exists {
			t.Fatalf("missing task column %s %v", column, err)
		}
	}
	for _, index := range []string{"task_ready_idx", "edges_to_idx", "outbox_unpublished_idx", "events_run_idx", "model_task_idx"} {
		var exists bool
		err := s.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", index).Scan(&exists)
		if err != nil || !exists {
			t.Fatalf("missing index %s %v", index, err)
		}
	}
	for _, kind := range []string{"f", "c", "u", "p"} {
		var n int
		if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_constraint WHERE connamespace=current_schema()::regnamespace AND contype=$1::\"char\"", kind).Scan(&n); err != nil || n == 0 {
			t.Fatalf("missing constraints %s %v", kind, err)
		}
	}
	var run string
	if err := s.Pool.QueryRow(ctx, "INSERT INTO workflow_runs(goal,state,finished_at) VALUES('terminal','succeeded',clock_timestamp()) RETURNING run_id").Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE workflow_runs SET state='running',finished_at=NULL WHERE run_id=$1", run); err == nil {
		t.Fatal("terminal reopened")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE mgar_reader"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM workflow_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO workflow_runs(goal) VALUES('unauthorized')"); err == nil {
		t.Fatal("reader can write")
	}
}
