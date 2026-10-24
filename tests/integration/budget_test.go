package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func budgetFixture(t *testing.T, n int) (*store.Store, string, store.OrchestratorClaim, []store.TaskClaim, *artifact.CAS) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	run, oc := planFixture(t, s)
	if e := s.ConfigureBudget(ctx, run, oc, budget.Limits{ModelCalls: 7, ToolCalls: 7, Tokens: 700, CostMicros: 700, ConcurrentCalls: 20, TaskConcurrency: 20, Delegations: 24}); e != nil {
		t.Fatal(e)
	}
	p := planner.Proposal{SchemaVersion: "plan.v1", Rationale: "bounded synthetic budget race", Nodes: []planner.Node{}, Edges: []planner.Edge{}}
	for i := 0; i < n; i++ {
		p.Nodes = append(p.Nodes, planner.Node{Key: fmt.Sprintf("A%02d", i), Role: "research", Goal: "same prompt", RequiredInputs: []string{}, OutputKind: "finding"})
	}
	if _, e := s.ProposeAndCommit(ctx, run, oc, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cas.Close() })
	claims := []store.TaskClaim{}
	for i := 0; i < n; i++ {
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: fmt.Sprintf("worker-%d", i), Role: "research"}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		claims = append(claims, c)
	}
	return s, run, oc, claims, cas
}
func budgetRequest(i int) budget.ModelRequest {
	p := budget.Pricing{Version: "synthetic.v1", Provider: "mock", Model: "small.v1", InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	r, _ := budget.PricedRequest(fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1), p, 60, 40, strings.Repeat("a", 64))
	return r
}
func TestBudgetLedgerPricedSettlement(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	r := budgetRequest(0)
	call, e := s.ReserveModelCall(ctx, cs[0], r)
	if e != nil {
		t.Fatal(e)
	}
	// A caller changing its price object after reservation cannot change the DB quote.
	r.Pricing.InputMicrosPerMillion = 9_000_000
	u := budget.ModelUsage{Status: "failed", Known: true, InputTokens: 10, OutputTokens: 5, Tokens: 15, CostMicros: 15, PricingVersion: "synthetic.v1", Source: "local_stub"}
	if e = s.FinishModelCall(ctx, cs[0], call.ID, u); e != nil {
		t.Fatal(e)
	}
	r = budgetRequest(1)
	next, e := s.ReserveModelCall(ctx, cs[1], r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishModelCall(ctx, cs[1], next.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
		t.Fatal(e)
	}
	report, e := s.BudgetReport(ctx, run)
	if e != nil || !report.Conserved || report.ReservedModels != 1 || report.SettledModels != 1 || report.ReservedTokens != 100 || report.SettledTokens != 15 || report.ReservedCostMicros != 100 || report.SettledCostMicros != 15 || report.UnknownModels != 1 {
		t.Fatalf("report=%+v %v", report, e)
	}
}

func TestBudgetLedgerTwentyProcessQuota(t *testing.T) {
	for _, mode := range []string{"modelquota", "toolquota", "mixed_concurrency"} {
		t.Run(mode, func(t *testing.T) {
			s, run, oc, claims, _ := budgetFixture(t, 20)
			ctx := context.Background()
			want := 7
			if mode == "mixed_concurrency" {
				want = 3
				if e := s.ConfigureBudget(ctx, run, oc, budget.Limits{ModelCalls: 7, ToolCalls: 7, Tokens: 700, CostMicros: 700, ConcurrentCalls: 3, TaskConcurrency: 20, Delegations: 24}); e != nil {
					t.Fatal(e)
				}
			}
			raw, _ := json.Marshal(claims)
			t.Setenv("MGAR_TEST_BUDGET_CLAIMS", string(raw))
			t.Setenv("MGAR_TEST_BUDGET_MODE", mode)
			outputs := processBatch(t, s, "budget", run, 20)
			wins, denied := 0, 0
			for _, out := range outputs {
				if strings.Contains(out, "RESULT winner") {
					wins++
				}
				if strings.Contains(out, "RESULT denied") {
					denied++
				}
			}
			if wins != want || denied != 20-want {
				t.Fatalf("20 processes: winners=%d denied=%d", wins, denied)
			}
			report, e := s.BudgetReport(ctx, run)
			if e != nil || !report.Conserved || report.ReservedModels+report.ReservedTools != int64(want) {
				t.Fatal(report, e)
			}
			t.Logf("20 OS processes: mode=%s admitted=%d denied=%d conserved=%t", mode, wins, denied, report.Conserved)
		})
	}
}
func TestBudgetLedgerGlobalToolsAndAtomicRollback(t *testing.T) {
	for _, mode := range []string{"settled", "unknown", "rollback", "concurrency"} {
		t.Run(mode, func(t *testing.T) {
			s, run, oc, cs, cas := budgetFixture(t, 2)
			ctx := context.Background()
			l := budget.Limits{ModelCalls: 7, ToolCalls: 1, Tokens: 700, CostMicros: 700, ConcurrentCalls: 1, TaskConcurrency: 20, Delegations: 24}
			if e := s.ConfigureBudget(ctx, run, oc, l); e != nil {
				t.Fatal(e)
			}
			r := store.ToolRequest{ID: budgetRequest(0).LogicalID, Capability: "read", Version: "tool.v1", Resource: "fixture", Hash: strings.Repeat("a", 64), Effect: "readonly"}
			if _, e := s.BeginTool(ctx, cs[0], r); e != nil {
				t.Fatal(e)
			}
			if _, e := s.ReserveModelCall(ctx, cs[1], budgetRequest(1)); e != store.ErrBudget {
				t.Fatal("tool did not share concurrency", e)
			}
			if mode == "rollback" {
				if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION reject_settle() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='tool_succeeded' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_settle BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_settle()`); e != nil {
					t.Fatal(e)
				}
				if e := s.FinishTool(ctx, cs[0], r, cas, []byte(`{"ok":true}`), "succeeded"); e == nil {
					t.Fatal("settlement partial commit")
				}
			} else {
				status := "failed"
				if mode == "unknown" {
					status = "unknown"
				}
				if e := s.FinishTool(ctx, cs[0], r, cas, nil, status); e != nil {
					t.Fatal(e)
				}
			}
			report, e := s.BudgetReport(ctx, run)
			if e != nil || !report.Conserved {
				t.Fatal(report, e)
			}
			if mode == "unknown" || mode == "rollback" {
				if report.ReservedTools != 1 || report.SettledTools != 0 {
					t.Fatal(report)
				}
			} else {
				if report.ReservedTools != 0 || report.SettledTools != 1 {
					t.Fatal(report)
				}
				if _, e = s.ReserveModelCall(ctx, cs[1], budgetRequest(1)); e != nil {
					t.Fatal("released concurrency unavailable", e)
				}
			}
			r.ID = budgetRequest(2).LogicalID
			if _, e = s.BeginTool(ctx, cs[1], r); e != store.ErrBudget {
				t.Fatal("global tool quota bypassed", e)
			}
		})
	}
}
func TestBudgetLedgerRejectsPriceRewriteAndCounterDrift(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	r := budgetRequest(0)
	call, e := s.ReserveModelCall(ctx, cs[0], r)
	if e != nil {
		t.Fatal(e)
	}
	u := budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 10, InputTokens: 10, CostMicros: 10, PricingVersion: "synthetic.v2", Source: "local_stub"}
	if e = s.FinishModelCall(ctx, cs[0], call.ID, u); e != store.ErrBudget {
		t.Fatal("price version changed", e)
	}
	if e = s.ConfigureBudget(ctx, run, oc, budget.Limits{ModelCalls: 0, ToolCalls: 7, Tokens: 0, CostMicros: 0, ConcurrentCalls: 1, TaskConcurrency: 20, Delegations: 24}); e != store.ErrBudget {
		t.Fatal("shrank below reservation", e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE run_budgets SET reserved_tokens=reserved_tokens-1 WHERE run_id=$1", run); e != nil {
		t.Fatal(e)
	}
	report, e := s.BudgetReport(ctx, run)
	if e != nil || report.Conserved {
		t.Fatal("drift hidden or repaired", report, e)
	}
}

func TestBudgetLedgerMigrationBackfillsHistoricalTools(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	schema := s.Pool.Config().ConnConfig.RuntimeParams["search_path"] + "_upgrade"
	ident := pgx.Identifier{schema}.Sanitize()
	if _, e := s.Pool.Exec(ctx, "CREATE SCHEMA "+ident); e != nil {
		t.Fatal(e)
	}
	cfg := s.Pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, e := s.Pool.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	for _, m := range []struct{ id, sql string }{{"001_control", migrations.ControlSQL}, {"002_artifact_cas", migrations.ArtifactSQL}, {"003_context_snapshots", migrations.ContextSQL}, {"004_plan_node_configs", migrations.PlanConfigSQL}} {
		if e = store.ApplyMigration(ctx, pool, m.id, m.sql); e != nil {
			t.Fatal(e)
		}
	}
	var run, task string
	if e = pool.QueryRow(ctx, "INSERT INTO workflow_runs(goal) VALUES('historical synthetic') RETURNING run_id").Scan(&run); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO run_budgets(run_id) VALUES($1)", run); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "INSERT INTO task_instances(run_id,node_key,role) VALUES($1,'A','research') RETURNING task_id", run).Scan(&task); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO tool_invocations(task_id,capability,scope,request_hash,effect_class,status) SELECT $1,'historical','{}',$2,'readonly',CASE WHEN i<=5 THEN 'unknown' ELSE 'failed' END FROM generate_series(1,70) i", task, strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	if e = store.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	report, e := (&store.Store{Pool: pool}).BudgetReport(ctx, run)
	if e != nil || !report.Conserved || report.ReservedTools != 5 || report.SettledTools != 65 || report.Limits.ToolCalls != 70 || report.Limits.ConcurrentCalls != 5 {
		t.Fatal(report, e)
	}
	if e = store.Migrate(ctx, pool); e != nil {
		t.Fatal("upgrade repeat", e)
	}
}
func TestBudgetLedgerIndependentPricingVersions(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		r := budgetRequest(i)
		p := *r.Pricing
		p.Version = fmt.Sprintf("synthetic.v%d", i+1)
		p.InputMicrosPerMillion *= int64(i + 1)
		p.OutputMicrosPerMillion *= int64(i + 1)
		r, e := budget.PricedRequest(r.LogicalID, p, 60, 40, r.InputDigest)
		if e != nil {
			t.Fatal(e)
		}
		call, e := s.ReserveModelCall(ctx, cs[i], r)
		if e != nil {
			t.Fatal(e)
		}
		u := budget.ModelUsage{Status: "succeeded", Known: true, InputTokens: 10, Tokens: 10, CostMicros: int64(10 * (i + 1)), PricingVersion: p.Version, Source: "local_stub"}
		if e = s.FinishModelCall(ctx, cs[i], call.ID, u); e != nil {
			t.Fatal(e)
		}
	}
	report, e := s.BudgetReport(ctx, run)
	if e != nil || !report.Conserved || report.SettledModels != 2 || report.SettledCostMicros != 30 {
		t.Fatal(report, e)
	}
}
