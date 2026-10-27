package demo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/google/uuid"
)

func aggregateBindingFixture(t *testing.T) (*fixture, []store.TaskClaim, []byte) {
	t.Helper()
	ctx := context.Background()
	f, e := newFixture(ctx, testOptions(t))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := f.close(); e != nil {
			t.Error(e)
		}
	})
	run := uuid.NewString()
	_, owner, e := f.s.CreateRunClaimed(ctx, run, "owned receipt scope fixture", 8, "controller", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.BeginPlanning(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	if e = f.s.ConfigureBudget(ctx, run, owner, budget.Limits{ModelCalls: 4, ToolCalls: 8, Tokens: 10000, CostMicros: 2000, ConcurrentCalls: 4, TaskConcurrency: 4, Delegations: 8}); e != nil {
		t.Fatal(e)
	}
	p := proposal("analysis")
	p.BaseVersion = 0
	p.Nodes = []planner.Node{{Key: "a", Role: "aggregate", Goal: "controlled fixture a", RequiredInputs: []string{}, OutputKind: "report"}, {Key: "b", Role: "aggregate", Goal: "controlled fixture b", RequiredInputs: []string{}, OutputKind: "report"}}
	p.Edges = []planner.Edge{}
	if _, e = f.s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	claims := []store.TaskClaim{}
	for i := 0; i < 2; i++ {
		c, e := f.s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-" + string(rune('a'+i)), Role: "aggregate"}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.BuildSnapshot(ctx, c, "mock.v1", "demo.v1", f.cas); e != nil {
			t.Fatal(e)
		}
		c, e = f.s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		claims = append(claims, c)
	}
	// Trusted fixture state is the input to the binding unit, never model output.
	body := inputcontext.Canonical(handoff.MergeResult{SchemaVersion: "merge_result.v1", Facts: []handoff.Fact{}, Conflicts: []handoff.Conflict{}, Missing: []handoff.Missing{}, CanFinalize: true})
	return f, claims, body
}

func TestAggregateReceiptRequiresDurableSameClaim(t *testing.T) {
	for _, mode := range []string{"valid", "forged", "extra-field", "full-body", "no-tool", "no-execution", "different-task", "wrong-owner", "wrong-epoch", "wrong-plan", "pending-tool", "unknown-tool", "wrong-CAS-receipt", "duplicate-tool", "pending-model", "unknown-model", "pending-resource", "unknown-resource", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, claims, body := aggregateBindingFixture(t)
			c := claims[0]
			state := &taskTools{expected: body, executed: 1}
			output := receiptFor(body)
			finish := func(status string, observation []byte) {
				t.Helper()
				r := store.ToolRequest{ID: uuid.NewString(), Capability: "aggregate.merge", Version: "demo.v1", Resource: "fixture", Effect: "readonly", Hash: artifact.Digest([]byte("owned empty-argument fixture request"))}
				if _, e := f.s.BeginTool(ctx, c, r); e != nil {
					t.Fatal(e)
				}
				if status != "reserved" {
					if e := f.s.FinishTool(ctx, c, r, f.cas, observation, status); e != nil {
						t.Fatal(e)
					}
				}
			}
			switch mode {
			case "no-tool":
			case "pending-tool":
				finish("reserved", nil)
			case "unknown-tool":
				finish("unknown", nil)
			case "wrong-CAS-receipt":
				finish("succeeded", inputcontext.Canonical(mergeReceipt{Schema: "merge-receipt.v1", Hash: strings.Repeat("0", 64)}))
			default:
				finish("succeeded", output)
			}
			switch mode {
			case "forged":
				output = inputcontext.Canonical(mergeReceipt{Schema: "merge-receipt.v1", Hash: strings.Repeat("f", 64)})
			case "extra-field":
				output = inputcontext.Canonical(map[string]any{"schema_version": "merge-receipt.v1", "merge_sha256": artifact.Digest(body), "verified": true})
			case "full-body":
				output = body
			case "no-execution":
				state.executed = 0
			case "different-task":
				c = claims[1]
			case "wrong-owner":
				c.Owner = "other-owner"
			case "wrong-epoch":
				c.Epoch++
			case "wrong-plan":
				c.PlanVersion++
			case "duplicate-tool":
				finish("succeeded", output)
			case "pending-model", "unknown-model":
				price := budget.Pricing{Provider: "mock", Model: "test.v1", Version: "test.v1", InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}
				r, e := budget.PricedRequest(uuid.NewString(), price, 100, 100, strings.Repeat("a", 64))
				if e != nil {
					t.Fatal(e)
				}
				call, e := f.s.ReserveModelCall(ctx, c, r)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "unknown-model" {
					if e = f.s.FinishModelCall(ctx, c, call.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
						t.Fatal(e)
					}
				}
			case "pending-resource", "unknown-resource":
				ticket, e := f.s.EnqueueResource(ctx, c, "db", 0, 20*time.Second)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "unknown-resource" {
					held, e := f.s.AcquireResource(ctx, c, ticket)
					if e != nil || !held {
						t.Fatal("resource not held", e)
					}
					if e = f.s.FinishResource(ctx, ticket, false); e != nil {
						t.Fatal(e)
					}
				}
			case "terminal":
				w, e := f.s.Workflow(ctx, c.RunID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.s.CancelWorkflow(ctx, c.RunID, w.Revision); e != nil {
					t.Fatal(e)
				}
			}
			resolved, e := f.resolveAggregate(ctx, c, state, output)
			if mode == "valid" {
				if e != nil || string(resolved) != string(body) {
					t.Fatal("valid same-claim receipt did not preserve full bytes", e)
				}
			} else if e == nil || len(resolved) != 0 {
				t.Fatal("receipt bypassed trusted binding", mode)
			}
		})
	}
}
