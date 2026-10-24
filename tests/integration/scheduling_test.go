package integration

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"testing"
	"time"
)

func TestSchedulingReadyVersionAndDependencyFence(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, oc := planFixture(t, s)
	p := simplePlan()
	p.Nodes = append(p.Nodes, planner.Node{Key: "B", Role: "research", Goal: "long chain", RequiredInputs: []string{}, OutputKind: "finding"}, planner.Node{Key: "C", Role: "research", Goal: "child", RequiredInputs: []string{}, OutputKind: "finding"})
	p.Edges = []planner.Edge{{From: "B", To: "C"}}
	if _, e := s.ProposeAndCommit(ctx, run, oc, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	view, e := s.SchedulingView(ctx, run)
	if e != nil || len(view.Nodes) != 3 || len(view.Edges) != 1 {
		t.Fatal(view, e)
	}
	id := store.WorkerIdentity{Owner: "scheduled", Role: "research"}
	if _, e = s.ClaimReady(ctx, run, id, time.Minute, 2, view.Nodes[0].ID, "fifo"); !errors.Is(e, store.ErrFenced) {
		t.Fatal("old plan", e)
	}
	if _, e = s.ClaimReady(ctx, run, id, time.Minute, 1, view.Nodes[2].ID, "critical_path"); !errors.Is(e, store.ErrNoReady) {
		t.Fatal("pending child", e)
	}
	c, e := (scheduler.Scheduler{Store: s}).ClaimScheduled(ctx, run, id, time.Minute, "critical_path")
	if e != nil || c.NodeKey != "B" {
		t.Fatal(c, e)
	}
	var policy string
	if e = s.Pool.QueryRow(ctx, "SELECT payload->>'scheduling_policy' FROM workflow_events WHERE entity_id=$1 AND kind='task_claimed'", c.TaskID).Scan(&policy); e != nil || policy != "critical_path" {
		t.Fatal(policy, e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET state='ready' WHERE task_id=$1", view.Nodes[2].ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimReady(ctx, run, id, time.Minute, 1, view.Nodes[2].ID, "critical_path"); !errors.Is(e, store.ErrStaleInput) {
		t.Fatal("dependency bypass", e)
	}
	var attempts int
	if e = s.Pool.QueryRow(ctx, "SELECT attempts FROM task_instances WHERE task_id=$1", view.Nodes[2].ID).Scan(&attempts); e != nil || attempts != 0 {
		t.Fatal(attempts, e)
	}
}
func TestRoutingAuditedAndConcurrentBudgetAuthoritative(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	if e := s.ConfigureBudget(ctx, run, oc, budget.Limits{ModelCalls: 1, ToolCalls: 1, Tokens: 100, CostMicros: 100, ConcurrentCalls: 2, TaskConcurrency: 20, Delegations: 24}); e != nil {
		t.Fatal(e)
	}
	r := budgetRequest(0)
	router := budget.Router{AllowedProviders: []string{"mock"}, Catalog: []budget.RouteModel{{Price: *r.Pricing, Roles: []string{"research"}, Available: true}}}
	report, e := s.BudgetReport(ctx, run)
	if e != nil {
		t.Fatal(e)
	}
	in := budget.RouteInput{Role: "research", LogicalID: r.LogicalID, InputDigest: r.InputDigest, InputTokens: 60, OutputTokens: 40}
	quote, e := router.Route(in, report)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReserveModelCall(ctx, cs[0], quote); e != nil {
		t.Fatal(e)
	}
	in.LogicalID = budgetRequest(1).LogicalID
	stale, e := router.Route(in, report)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReserveModelCall(ctx, cs[1], stale); !errors.Is(e, store.ErrBudget) {
		t.Fatal("stale quote overspent", e)
	}
	var reason string
	if e = s.Pool.QueryRow(ctx, "SELECT payload->'request'->'routing'->>'reason' FROM workflow_events WHERE run_id=$1 AND kind='model_reserved'", run).Scan(&reason); e != nil || reason != "lowest_authorized_cost" {
		t.Fatal(reason, e)
	}
}
func TestRoutingResourceAdapterUsesActualRole(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	r := budgetRequest(0)
	router := budget.Router{AllowedProviders: []string{"mock"}, Catalog: []budget.RouteModel{{Price: *r.Pricing, Roles: []string{"action"}, Available: true}}}
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	called := false
	in := budget.RouteInput{Role: "action", LogicalID: r.LogicalID, InputDigest: r.InputDigest, InputTokens: 60, OutputTokens: 40}
	transport := func(context.Context, budget.ModelRequest) (budget.ModelUsage, bool, error) {
		called = true
		return budget.ModelUsage{Status: "succeeded", Known: true, Source: "local_stub", PricingVersion: r.Pricing.Version, InputTokens: 10, Tokens: 10, CostMicros: 10}, true, nil
	}
	if _, e = resources.RoutedModel(ctx, cs[0], router, in, transport); e == nil || called {
		t.Fatal("caller escalated role", e)
	}
	router.Catalog[0].Roles = []string{"research"}
	if _, e = resources.RoutedModel(ctx, cs[0], router, in, transport); e != nil || !called {
		t.Fatal(e)
	}
	b, e := s.BudgetReport(ctx, run)
	if e != nil || !b.Conserved || b.SettledCostMicros != 10 {
		t.Fatal(b, e)
	}
}

func TestSchedulingHistoryUsesCommittedSuccessfulExecution(t *testing.T) {
	s, run, oc, cs, cas := budgetFixture(t, 2)
	ctx := context.Background()
	// Actual elapsed local work between the existing execution/publication events.
	<-time.NewTimer(5 * time.Millisecond).C
	a, e := s.PublishArtifact(ctx, cs[0], cas, "finding", "application/json", "research.v1", []byte(`{"synthetic":true}`))
	if e != nil {
		t.Fatal(e)
	}
	// This synthetic verdict only isolates the estimator; independent verifier tests remain separate.
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'estimator-fixture.v1',true)", a.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyVerdicts(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	view, e := s.SchedulingView(ctx, run)
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range view.Nodes {
		if n.Estimate < 5*time.Millisecond || n.Estimate >= time.Second {
			t.Fatal("history ignored", n)
		}
	}
}
