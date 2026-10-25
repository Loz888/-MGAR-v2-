package evalbaseline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/google/uuid"
)

type fixture struct {
	s                          *store.Store
	b                          *Broker
	cas                        *artifact.CAS
	dir, run                   string
	owner                      store.OrchestratorClaim
	source, consumer, research store.TaskClaim
	candidate                  Candidate
	plan                       planner.Proposal
	metadata                   artifact.Metadata
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{s: testdb.New(t), run: uuid.NewString(), dir: t.TempDir()}
	var e error
	f.cas, e = artifact.New(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := f.cas.Close(); e != nil {
			t.Error(e)
		}
	})
	_, f.owner, e = f.s.CreateRunClaimed(ctx, f.run, "candidate boundary fixture", 16, "controller", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.BeginPlanning(ctx, f.run, f.owner); e != nil {
		t.Fatal(e)
	}
	f.plan = planner.Proposal{SchemaVersion: "plan.v1", Rationale: "isolated admission carriers", Edges: []planner.Edge{}, Nodes: []planner.Node{
		{Key: "data", Role: "data", Goal: "read", RequiredInputs: []string{}, OutputKind: "sql_evidence"},
		{Key: "research", Role: "research", Goal: "read", RequiredInputs: []string{}, OutputKind: "finding"},
		{Key: "action", Role: "action", Goal: "act", RequiredInputs: []string{}, OutputKind: "patch"},
		{Key: "aggregate", Role: "aggregate", Goal: "report", RequiredInputs: []string{}, OutputKind: "report"},
	}}
	if _, e = f.s.ProposeAndCommit(ctx, f.run, f.owner, f.plan); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RefreshReady(ctx, f.run, f.owner); e != nil {
		t.Fatal(e)
	}
	claim := func(role string) store.TaskClaim {
		c, e := f.s.ClaimTask(ctx, f.run, store.WorkerIdentity{Owner: role + "-worker", Role: role}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.BuildSnapshot(ctx, c, "mock.v1", "fixture.v1", f.cas); e != nil {
			t.Fatal(e)
		}
		c, e = f.s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	f.source = claim("data")
	f.metadata, e = f.s.PublishArtifact(ctx, f.source, f.cas, "sql_evidence", "application/json", "fixture.v1", []byte(`{"row_count":1,"rows":[[15,2,1]]}`))
	if e != nil {
		t.Fatal(e)
	}
	f.consumer = claim("action")
	f.research = claim("research")
	f.b, e = New(ctx, f.s, f.cas, f.run, 1)
	if e != nil {
		t.Fatal(e)
	}
	f.candidate, e = f.b.Publish(ctx, f.source, f.metadata)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestCandidateConcurrentReadIsPinnedAndUnverified(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	items := []Candidate{f.candidate}
	handle, e := f.b.Grant(ctx, f.consumer, items)
	if e != nil {
		t.Fatal(e)
	}
	items[0].Content = "caller-mutated" // grant must not retain caller-owned slices.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, e := f.b.Read(ctx, f.consumer, handle)
			if e == nil {
				var context Context
				e = json.Unmarshal(raw, &context)
				if e == nil && (len(context.Inputs) != 1 || context.Inputs[0] != f.candidate || context.Inputs[0].SemanticStatus != "unverified") {
					e = errors.New("candidate changed")
				}
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	raw, e := f.b.Read(ctx, f.consumer, handle)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), `"verified":true`) {
		t.Fatal("candidate elevated")
	}
	var verdicts int
	if e = f.s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifact_verdicts").Scan(&verdicts); e != nil || verdicts != 0 {
		t.Fatal("candidate wrote a verdict")
	}
}
func TestCandidateRejectsCrossRunRoleHashVersionAndRebinding(t *testing.T) {
	for _, mode := range []string{"run", "role", "producer_role", "task", "version", "hash", "content", "semantic", "kind", "tool", "size", "node", "duplicate", "empty", "unknown_grant", "epoch", "revision", "owner", "consumer_role", "foreign_real_claim", "rebind"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			candidate := f.candidate
			consumer := f.consumer
			handle, e := f.b.Grant(ctx, consumer, []Candidate{candidate})
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "run":
				candidate.RunID = uuid.NewString()
			case "role":
				consumer = f.research
			case "producer_role":
				candidate.Role = "research"
			case "task":
				candidate.TaskID = uuid.NewString()
			case "version":
				candidate.PlanVersion++
			case "hash":
				candidate.Hash = strings.Repeat("a", 64)
			case "content":
				candidate.Content = `{"rows":[[999]]}`
			case "semantic":
				candidate.SemanticStatus = "verified"
			case "kind":
				candidate.Kind = "finding"
			case "tool":
				candidate.ToolVersion = "different.v1"
			case "size":
				candidate.Size++
			case "node":
				candidate.Node = "research"
			case "epoch":
				consumer.Epoch++
			case "revision":
				consumer.Revision++
			case "owner":
				consumer.Owner = "attacker"
			case "consumer_role":
				consumer.Role = "aggregate"
			case "foreign_real_claim":
				consumer = setup(t).consumer
			}
			if mode == "unknown_grant" {
				_, e = f.b.Read(ctx, consumer, "unknown")
			} else if mode == "duplicate" {
				_, e = f.b.Grant(ctx, consumer, []Candidate{candidate, candidate})
			} else if mode == "empty" {
				_, e = f.b.Grant(ctx, consumer, nil)
			} else if mode == "rebind" {
				body := []byte(`{"schema_version":"facts.v1","facts":[]}`)
				a, err := f.s.PublishArtifact(ctx, f.research, f.cas, "finding", "application/json", "fixture.v1", body)
				if err != nil {
					t.Fatal(err)
				}
				other, err := f.b.Publish(ctx, f.research, a)
				if err != nil {
					t.Fatal(err)
				}
				_, e = f.b.Grant(ctx, consumer, []Candidate{candidate, other})
			} else {
				_, e = f.b.Grant(ctx, consumer, []Candidate{candidate})
			}
			if !errors.Is(e, ErrScope) {
				t.Fatalf("scope accepted %s: %v", mode, e)
			}
			if mode == "epoch" || mode == "revision" || mode == "owner" || mode == "consumer_role" || mode == "foreign_real_claim" {
				if _, e = f.b.Read(ctx, consumer, handle); !errors.Is(e, ErrScope) {
					t.Fatal("forged consumer read")
				}
			}
		})
	}
}
func TestCandidateRejectsCASDamagePlanChangeAndExpiredAuthority(t *testing.T) {
	for _, mode := range []string{"cas", "plan", "lease", "cancel", "producer_identity"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			handle, e := f.b.Grant(ctx, f.consumer, []Candidate{f.candidate})
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "cas":
				if e = os.WriteFile(filepath.Join(f.dir, f.candidate.Hash), []byte("damaged owned test object"), 0600); e != nil {
					t.Fatal(e)
				}
			case "plan":
				p := f.plan
				p.BaseVersion = 1
				if _, e = f.s.ProposeAndCommit(ctx, f.run, f.owner, p); e != nil {
					t.Fatal(e)
				}
			case "lease":
				if _, e = f.s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", f.consumer.TaskID); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				w, e := f.s.Workflow(ctx, f.run)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.s.CancelWorkflow(ctx, f.run, w.Revision); e != nil {
					t.Fatal(e)
				}
			case "producer_identity":
				c := f.source
				c.Epoch++
				if _, e = f.b.Publish(ctx, c, f.metadata); !errors.Is(e, ErrScope) {
					t.Fatal("producer forgery")
				}
				return
			}
			if _, e = f.b.Read(ctx, f.consumer, handle); !errors.Is(e, ErrScope) {
				t.Fatalf("stale context accepted %s %v", mode, e)
			}
		})
	}
}
