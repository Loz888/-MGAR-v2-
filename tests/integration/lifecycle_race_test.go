package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

type lifecycleResult struct {
	workflow store.Workflow
	err      error
}

func lifecycleAuthorizer(t *testing.T, ctx context.Context, s *store.Store) (*store.Store, int) {
	t.Helper()
	cfg := s.Pool.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var pid int
	if err = pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return &store.Store{Pool: pool}, pid
}

func waitLifecycleLock(t *testing.T, ctx context.Context, s *store.Store, pid int, result <-chan lifecycleResult) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := s.Pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case completed := <-result:
			t.Fatalf("operation finished without serializing with the pending write: %+v", completed)
		case <-ctx.Done():
			t.Fatal("operation never reached the expected PostgreSQL lock wait")
		case <-ticker.C:
		}
	}
}

func completedLifecycle(t *testing.T, ctx context.Context, result <-chan lifecycleResult) lifecycleResult {
	t.Helper()
	select {
	case completed := <-result:
		return completed
	case <-ctx.Done():
		t.Fatal("operation did not finish after releasing its PostgreSQL blocker")
		return lifecycleResult{}
	}
}

func outputReadyFixture(t *testing.T) (*store.Store, string, store.TaskClaim, string) {
	t.Helper()
	s, run, task := claimedFixture(t)
	ctx := context.Background()
	task, err := s.StartTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	artifact := artifactForClaim(t, s, task)
	if err = s.CommitOutput(ctx, task, artifact); err != nil {
		t.Fatal(err)
	}
	return s, run, task, artifact
}

func TestApplyVerdictsWaitsForConcurrentRevocation(t *testing.T) {
	s, run, task, artifact := outputReadyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic-initial.v1',true)", artifact); err != nil {
		t.Fatal(err)
	}
	verdict, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verdict.Rollback(context.Background())
	if _, err = verdict.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic-race.v1',false)", artifact); err != nil {
		t.Fatal(err)
	}
	authorizer, pid := lifecycleAuthorizer(t, ctx, s)
	result := make(chan lifecycleResult, 1)
	go func() {
		result <- lifecycleResult{err: authorizer.ApplyVerdicts(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0})}
	}()
	waitLifecycleLock(t, ctx, s, pid, result)
	if err = verdict.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if completed := completedLifecycle(t, ctx, result); completed.err != nil {
		t.Fatal(completed.err)
	}
	if snapshot := snapshotAuthorization(t, s, run, task.TaskID); snapshot.State != "failed" {
		t.Fatalf("task accepted a verdict superseded during its lock wait: %+v", snapshot)
	}
	var successes int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE entity_id=$1 AND kind='task_succeeded'", task.TaskID).Scan(&successes); err != nil || successes != 0 {
		t.Fatalf("superseded verdict produced a success event: %d %v", successes, err)
	}
}

func TestFinalizeWaitsForConcurrentRevocation(t *testing.T) {
	s := testdb.New(t)
	run, orchestrator := planFixture(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.ProposeAndCommit(ctx, run, orchestrator, simplePlan()); err != nil {
		t.Fatal(err)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	if w, err := s.FinalizeWorkflow(ctx, run, orchestrator); err != nil || w.State != "verifying" {
		t.Fatalf("initial verifying transition: %+v %v", w, err)
	}
	verdict, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verdict.Rollback(context.Background())
	if _, err = verdict.Exec(ctx, `INSERT INTO artifact_verdicts(artifact_id,checker_version,passed)
	 SELECT result_artifact_id,'synthetic-race.v1',false FROM task_instances WHERE run_id=$1 AND node_key='A'`, run); err != nil {
		t.Fatal(err)
	}
	authorizer, pid := lifecycleAuthorizer(t, ctx, s)
	result := make(chan lifecycleResult, 1)
	go func() {
		w, err := authorizer.FinalizeWorkflow(ctx, run, orchestrator)
		result <- lifecycleResult{workflow: w, err: err}
	}()
	waitLifecycleLock(t, ctx, s, pid, result)
	if err = verdict.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	completed := completedLifecycle(t, ctx, result)
	if completed.err != nil || completed.workflow.State != "failed" {
		t.Fatalf("workflow accepted a verdict superseded during its lock wait: %+v %v", completed.workflow, completed.err)
	}
	var successes int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='workflow_succeeded'", run).Scan(&successes); err != nil || successes != 0 {
		t.Fatalf("superseded verdict produced workflow success: %d %v", successes, err)
	}
}

func waitOrchestratorExpired(t *testing.T, ctx context.Context, s *store.Store, run string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if err := s.Pool.QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM orchestrator_leases WHERE run_id=$1", run).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("synthetic orchestrator lease never expired")
		case <-ticker.C:
		}
	}
}

func TestRecoveryRollsBackWhenLeaseExpiresDuringLockWait(t *testing.T) {
	s, run, task := claimedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.Pool.Exec(ctx, "UPDATE task_instances SET max_attempts=attempts,lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()+interval '1 second' WHERE run_id=$1", run); err != nil {
		t.Fatal(err)
	}
	before := snapshotAuthorization(t, s, run, task.TaskID)
	blocker, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run); err != nil {
		t.Fatal(err)
	}
	authorizer, pid := lifecycleAuthorizer(t, ctx, s)
	result := make(chan lifecycleResult, 1)
	go func() {
		result <- lifecycleResult{err: authorizer.RecoverExpired(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0})}
	}()
	waitLifecycleLock(t, ctx, s, pid, result)
	waitOrchestratorExpired(t, ctx, s, run)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if completed := completedLifecycle(t, ctx, result); !errors.Is(completed.err, store.ErrFenced) {
		t.Fatalf("expired orchestrator committed recovery mutations: %v", completed.err)
	}
	if after := snapshotAuthorization(t, s, run, task.TaskID); after != before {
		t.Fatalf("expired recovery changed task/budget/audit: before=%+v after=%+v", before, after)
	}
}

func TestLifecycleReadOnlyPathsFenceLeaseExpiredDuringWait(t *testing.T) {
	for _, operation := range []string{"apply_without_verdict", "finalize_pending", "budget_available"} {
		t.Run(operation, func(t *testing.T) {
			s, run, task, _ := outputReadyFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()+interval '1 second' WHERE run_id=$1", run); err != nil {
				t.Fatal(err)
			}
			before := snapshotAuthorization(t, s, run, task.TaskID)
			blocker, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			switch operation {
			case "apply_without_verdict":
				_, err = blocker.Exec(ctx, "SELECT task_id FROM task_instances WHERE task_id=$1 FOR UPDATE", task.TaskID)
			case "finalize_pending":
				_, err = blocker.Exec(ctx, "LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE")
			case "budget_available":
				_, err = blocker.Exec(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run)
			}
			if err != nil {
				t.Fatal(err)
			}
			authorizer, pid := lifecycleAuthorizer(t, ctx, s)
			result := make(chan lifecycleResult, 1)
			go func() {
				orchestrator := store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}
				var completed lifecycleResult
				switch operation {
				case "apply_without_verdict":
					completed.err = authorizer.ApplyVerdicts(ctx, run, orchestrator)
				case "finalize_pending":
					completed.workflow, completed.err = authorizer.FinalizeWorkflow(ctx, run, orchestrator)
				case "budget_available":
					completed.workflow, completed.err = authorizer.BlockIfBudgetExhausted(ctx, run, orchestrator)
				}
				result <- completed
			}()
			waitLifecycleLock(t, ctx, s, pid, result)
			waitOrchestratorExpired(t, ctx, s, run)
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if completed := completedLifecycle(t, ctx, result); !errors.Is(completed.err, store.ErrFenced) {
				t.Fatalf("%s returned success after its orchestrator lease expired: %v", operation, completed.err)
			}
			if after := snapshotAuthorization(t, s, run, task.TaskID); after != before {
				t.Fatalf("expired operation changed task/budget/audit: before=%+v after=%+v", before, after)
			}
		})
	}
}
