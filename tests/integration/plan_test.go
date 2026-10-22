package integration

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"strings"
	"sync"
	"testing"
)

func TestPlanCrossProcessCAS(t *testing.T) {
	s := testdb.New(t)
	run, _ := planFixture(t, s)
	outputs := processBatch(t, s, "plan", run, 2)
	wins, conflicts := 0, 0
	for _, out := range outputs {
		if strings.Contains(out, "RESULT winner") {
			wins++
		}
		if strings.Contains(out, "RESULT conflict") {
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("cross-process CAS %v", outputs)
	}
}

func TestPlanInvalidDependencyLeavesDBUnchanged(t *testing.T) {
	s := testdb.New(t)
	run, claim := planFixture(t, s)
	p := simplePlan()
	p.Edges = []planner.Edge{{From: "A", To: "foreign-workflow-node"}}
	if _, e := s.ProposeAndCommit(context.Background(), run, claim, p); e == nil {
		t.Fatal("invalid dependency accepted")
	}
	var n int
	_ = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM plans").Scan(&n)
	if n != 0 {
		t.Fatal("invalid proposal wrote a partial plan")
	}
}

func planFixture(t *testing.T, s *store.Store) (string, store.OrchestratorClaim) {
	t.Helper()
	ctx := context.Background()
	var run string
	if e := s.Pool.QueryRow(ctx, "INSERT INTO workflow_runs(goal,state) VALUES('synthetic','planning') RETURNING run_id").Scan(&run); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO run_budgets(run_id) VALUES($1)", run); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO orchestrator_leases VALUES($1,'orchestrator',1,clock_timestamp()+interval '1 hour',0)", run); e != nil {
		t.Fatal(e)
	}
	return run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}
}
func simplePlan() planner.Proposal {
	return planner.Proposal{SchemaVersion: "plan.v1", Rationale: "synthetic", Nodes: []planner.Node{{Key: "A", Role: "research", Goal: "inspect", RequiredInputs: []string{}, OutputKind: "finding"}}, Edges: []planner.Edge{}}
}
func TestPlanConcurrentCASAndHistory(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, claim := planFixture(t, s)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := s.ProposeAndCommit(ctx, run, claim, simplePlan())
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrPlanConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS: %d success %d conflicts", success, conflict)
	}
	var n int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM plans WHERE run_id=$1", run).Scan(&n)
	if n != 1 {
		t.Fatal("partial competing plan")
	}
	if _, e := s.Pool.Exec(ctx, "UPDATE plans SET proposer='rewrite' WHERE run_id=$1", run); e == nil {
		t.Fatal("history mutable")
	}
	p := simplePlan()
	p.BaseVersion = 1
	if _, e := s.ProposeAndCommit(ctx, run, claim, p); e != nil {
		t.Fatal(e)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM plans WHERE run_id=$1", run).Scan(&n)
	if n != 2 {
		t.Fatal("old plan not retained")
	}
	claim.Owner = "old-owner"
	p.BaseVersion = 2
	if _, e := s.ProposeAndCommit(ctx, run, claim, p); !errors.Is(e, store.ErrFenced) {
		t.Fatalf("old owner result %v", e)
	}
}
func TestPlanRollbackOnOutboxFailure(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, claim := planFixture(t, s)
	if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION fail_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER injected BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_outbox()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProposeAndCommit(ctx, run, claim, simplePlan()); e == nil {
		t.Fatal("expected failure")
	}
	for _, table := range []string{"plans", "plan_nodes", "plan_edges", "task_instances", "workflow_events", "outbox_events"} {
		var n int
		if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial %s %d %v", table, n, e)
		}
	}
	var active int
	_ = s.Pool.QueryRow(ctx, "SELECT active_plan_version FROM workflow_runs WHERE run_id=$1", run).Scan(&active)
	if active != 0 {
		t.Fatal("version advanced after rollback")
	}
}
