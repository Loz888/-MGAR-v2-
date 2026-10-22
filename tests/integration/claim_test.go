package integration

import (
	"context"
	"errors"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"strings"
	"testing"
	"time"
)

func claimedFixture(t *testing.T) (*store.Store, string, store.TaskClaim) {
	s := testdb.New(t)
	run, c := planFixture(t, s)
	ctx := context.Background()
	if _, e := s.ProposeAndCommit(ctx, run, c, simplePlan()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, c); e != nil {
		t.Fatal(e)
	}
	task, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-1", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	return s, run, task
}
func artifactForClaim(t *testing.T, s *store.Store, c store.TaskClaim) string {
	t.Helper()
	var artifact string
	e := s.Pool.QueryRow(context.Background(), "INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length) VALUES($1,'finding',repeat('1',64),$2,'artifact.v1',$3,$4,0) RETURNING artifact_id", c.RunID, "artifact://fixture-"+c.TaskID, c.TaskID, c.PlanVersion).Scan(&artifact)
	if e != nil {
		t.Fatal(e)
	}
	return artifact
}
func TestClaimCrossProcessGlobalBudget(t *testing.T) {
	for _, tasks := range []int{1, 8} {
		t.Run(fmt.Sprint(tasks), func(t *testing.T) {
			s := testdb.New(t)
			ctx := context.Background()
			run, c := planFixture(t, s)
			p := simplePlan()
			p.Nodes = nil
			for i := 0; i < tasks; i++ {
				p.Nodes = append(p.Nodes, planner.Node{Key: fmt.Sprintf("N%d", i), Role: "research", Goal: "synthetic", RequiredInputs: []string{}, OutputKind: "finding"})
			}
			if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_delegations=1,max_concurrency=8 WHERE run_id=$1", run); e != nil {
				t.Fatal(e)
			}
			if _, e := s.RefreshReady(ctx, run, c); e != nil {
				t.Fatal(e)
			}
			outputs := processBatch(t, s, "claim", run, 8)
			wins := 0
			for _, out := range outputs {
				if strings.Contains(out, "RESULT winner") {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("expected one authorization, got %d: %v", wins, outputs)
			}
			var used, running int
			if e := s.Pool.QueryRow(ctx, "SELECT used_delegations,running_count FROM run_budgets WHERE run_id=$1", run).Scan(&used, &running); e != nil || used != 1 || running != 1 {
				t.Fatalf("budget mismatch %d %d %v", used, running, e)
			}
		})
	}
}
func TestFenceRevisionLeaseRoleAndOutputCommit(t *testing.T) {
	s, run, c := claimedFixture(t)
	ctx := context.Background()
	stale := c
	var e error
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RenewTask(ctx, stale, time.Minute); !errors.Is(e, store.ErrCASConflict) {
		t.Fatalf("old revision accepted: %v", e)
	}
	c, e = s.RenewTask(ctx, c, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RenewTask(ctx, c, time.Minute); !errors.Is(e, store.ErrLeaseExpired) {
		t.Fatalf("expired renewal: %v", e)
	}
	newer, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-2", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if newer.Epoch != c.Epoch+1 {
		t.Fatal("epoch not advanced")
	}
	newer, e = s.StartTask(ctx, newer)
	if e != nil {
		t.Fatal(e)
	}
	artifact := artifactForClaim(t, s, newer)
	if e = s.CommitOutput(ctx, c, artifact); !errors.Is(e, store.ErrFenced) {
		t.Fatalf("old owner accepted: %v", e)
	}
	wrong := newer
	wrong.Role = "action"
	if e = s.CommitOutput(ctx, wrong, artifact); !errors.Is(e, store.ErrPolicy) {
		t.Fatalf("wrong role accepted: %v", e)
	}
	if e = s.CommitOutput(ctx, newer, artifact); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitOutput(ctx, newer, artifact); e == nil {
		t.Fatal("duplicate completion accepted")
	}
	var state string
	var used, running int
	_ = s.Pool.QueryRow(ctx, "SELECT state FROM workflow_runs WHERE run_id=$1", run).Scan(&state)
	if state != "running" {
		t.Fatal("worker failure cancelled workflow")
	}
	_ = s.Pool.QueryRow(ctx, "SELECT used_delegations,running_count FROM run_budgets WHERE run_id=$1", run).Scan(&used, &running)
	if used != 1 || running != 0 {
		t.Fatalf("reservation not conserved after takeover: %d %d", used, running)
	}
}
func TestClaimUnknownSideEffectNotReplayed(t *testing.T) {
	s, run, c := claimedFixture(t)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_model_calls=1,reserved_model_calls=1,max_cost_usd=1,reserved_cost=.1 WHERE run_id=$1", run); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO model_calls(logical_call_id,run_id,task_id,provider,model,status,estimate_bound,reservation) VALUES(gen_random_uuid(),$1,$2,'mock','synthetic-unknown','unknown',.1,'{}')", run, c.TaskID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "takeover", Role: "research"}, time.Minute); !errors.Is(e, store.ErrUnsafeReplay) {
		t.Fatalf("unknown side effect was replayed: %v", e)
	}
	var reserved string
	_ = s.Pool.QueryRow(ctx, "SELECT reserved_cost::text FROM run_budgets WHERE run_id=$1", run).Scan(&reserved)
	if reserved != "0.100000" {
		t.Fatalf("unknown charge lost: %s", reserved)
	}
}
func TestOrchestratorFenceAndRenew(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, old := planFixture(t, s)
	if _, e := s.TakeOrchestrator(ctx, run, "new", time.Minute); !errors.Is(e, store.ErrFenced) {
		t.Fatalf("live lease stolen %v", e)
	}
	if _, e := s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", run); e != nil {
		t.Fatal(e)
	}
	c, e := s.TakeOrchestrator(ctx, run, "new", time.Minute)
	if e != nil || c.Epoch != old.Epoch+1 {
		t.Fatalf("takeover %v %v", c, e)
	}
	if _, e = s.RenewOrchestrator(ctx, run, old, time.Minute); !errors.Is(e, store.ErrFenced) {
		t.Fatalf("old orchestrator renewed %v", e)
	}
	c, e = s.RenewOrchestrator(ctx, run, c, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProposeAndCommit(ctx, run, c, simplePlan()); e != nil {
		t.Fatal(e)
	}
}
