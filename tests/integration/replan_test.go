package integration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

func replanPolicy() planner.ReplanPolicy {
	return planner.ReplanPolicy{MaxReplans: 3, MaxDepth: 6, MaxCostMicros: 24, CostPerTaskMicros: 1}
}
func replanWire(t *testing.T, p planner.Proposal) []byte {
	t.Helper()
	b, e := json.Marshal(map[string]any{"schema_version": "replan.v1", "failure_kind": "evidence_rejected", "failed_node": "A", "proposal": p})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func rejectReplanEvidence(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if _, e := s.Pool.Exec(context.Background(), "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'replan.fixture',false)", id); e != nil {
		t.Fatal(e)
	}
}

func TestReplanPartialReuseAndAuditedDiff(t *testing.T) {
	s, cas, run, c, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	rejectReplanEvidence(t, s, old["A"].ID)
	p.BaseVersion = 1
	p.Nodes[0].Goal = "repair rejected branch"
	r, e := s.Replan(ctx, run, c, replanWire(t, p), configs, replanPolicy(), cas)
	if e != nil || !r.Accepted || r.Version != 2 || r.Decision.Reuse["B"] != old["B"].ID || len(r.Decision.Recompute) != 2 {
		t.Fatal(r, e)
	}
	var state string
	if e = s.Pool.QueryRow(ctx, "SELECT state FROM task_instances WHERE task_id=$1", old["C"].ProducerTaskID).Scan(&state); e != nil || state != "succeeded" {
		t.Fatal("history rewritten", state, e)
	}
	var audited bool
	if e = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_events WHERE run_id=$1 AND kind='replan_attempt' AND payload->>'reason'='accepted' AND payload->'decision'->'reuse'->>'B'=$2)", run, old["B"].ID).Scan(&audited); e != nil || !audited {
		t.Fatal("missing diff", e)
	}
}
func TestReplanInvalidGraphsBoundedStopAndPolicyFrozen(t *testing.T) {
	s, cas, run, c, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	rejectReplanEvidence(t, s, old["A"].ID)
	p.BaseVersion = 1
	p.Edges = append(p.Edges, planner.Edge{From: "C", To: "A"})
	wire := replanWire(t, p)
	r, e := s.Replan(ctx, run, c, wire, configs, replanPolicy(), cas)
	if !errors.Is(e, store.ErrReplanRejected) || r.Stopped {
		t.Fatal(r, e)
	}
	altered := replanPolicy()
	altered.MaxReplans++
	if _, e = s.Replan(ctx, run, c, wire, configs, altered, cas); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("policy loosened", e)
	}
	r, e = s.Replan(ctx, run, c, wire, configs, replanPolicy(), cas)
	if !errors.Is(e, store.ErrReplanRejected) || !r.Stopped {
		t.Fatal(r, e)
	}
	w, e := s.Workflow(ctx, run)
	if e != nil || w.ActivePlanVersion != 1 || w.State != "failed" {
		t.Fatal(w, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM plans WHERE run_id=$1", run).Scan(&n); e != nil || n != 1 {
		t.Fatal("invalid graph persisted", n, e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='replan_attempt'", run).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
func TestReplanLimitsAndUnsupportedFailure(t *testing.T) {
	for _, mode := range []string{"cost", "depth", "delegations", "role", "unsubstantiated", "wire", "max_replans"} {
		t.Run(mode, func(t *testing.T) {
			s, cas, run, c, p, configs, old := incrementalFixture(t)
			ctx := context.Background()
			p.BaseVersion = 1
			p.Nodes[0].Goal = "correct failed branch"
			policy := replanPolicy()
			if mode != "unsubstantiated" {
				rejectReplanEvidence(t, s, old["A"].ID)
			}
			switch mode {
			case "cost":
				policy.MaxCostMicros = 1
			case "depth":
				policy.MaxDepth = 1
			case "delegations":
				if _, e := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_delegations=used_delegations WHERE run_id=$1", run); e != nil {
					t.Fatal(e)
				}
			case "role":
				p.Nodes[0].Role = "root"
			case "max_replans":
				policy.MaxReplans = 1
				policy.MaxCostMicros = 1
			}
			wire := replanWire(t, p)
			if mode == "wire" {
				wire = []byte(`{"schema_version":"replan.v1","state":"succeeded"}`)
			}
			r, e := s.Replan(ctx, run, c, wire, configs, policy, cas)
			if !errors.Is(e, store.ErrReplanRejected) || r.Accepted {
				t.Fatal(mode, r, e)
			}
			w, e := s.Workflow(ctx, run)
			if e != nil || w.ActivePlanVersion != 1 {
				t.Fatal(w, e)
			}
			if mode == "max_replans" && w.State != "failed" {
				t.Fatal("unbounded replans", w)
			}
		})
	}
}
func TestReplanConcurrentCASAndOldWorkerFenced(t *testing.T) {
	s, cas, run, c, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	rejectReplanEvidence(t, s, old["A"].ID)
	p.BaseVersion = 1
	p.Nodes[0].Goal = "corrected branch"
	t.Setenv("MGAR_TEST_PROPOSAL", string(replanWire(t, p)))
	encoded, e := json.Marshal(configs)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_TEST_CONFIGS", string(encoded))
	refs := []inputcontext.Ref{}
	for _, a := range old {
		refs = append(refs, inputcontext.Ref{Hash: a.Hash, ByteLength: a.ByteLength})
	}
	root := filepath.Join(t.TempDir(), "replica")
	replica, e := newSelectionReplica(root, cas, refs)
	if e != nil {
		t.Fatal(e)
	}
	defer replica.Close()
	t.Setenv("MGAR_TEST_CAS_ROOT", root)
	wins, conflicts := 0, 0
	for _, out := range processBatch(t, s, "replan", run, 2) {
		if strings.Contains(out, "RESULT winner") {
			wins++
		}
		if strings.Contains(out, "RESULT conflict") {
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	if _, e = s.RefreshReady(ctx, run, c); e != nil {
		t.Fatal(e)
	}
	worker, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "old-worker", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	// A trusted negative verdict on reused B permits replanning while A is active.
	rejectReplanEvidence(t, s, old["B"].ID)
	p.BaseVersion = 2
	p.Nodes[0].Goal = "third version"
	var proposal map[string]any
	if e = json.Unmarshal(replanWire(t, p), &proposal); e != nil {
		t.Fatal(e)
	}
	proposal["failed_node"] = "B"
	wire, e := json.Marshal(proposal)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Replan(ctx, run, c, wire, configs, replanPolicy(), cas); e != nil {
		t.Fatal(e)
	}
	if _, e = s.StartTask(ctx, worker); !errors.Is(e, store.ErrFenced) {
		t.Fatal("old worker not fenced", e)
	}
	var running int
	if e = s.Pool.QueryRow(ctx, "SELECT running_count FROM run_budgets WHERE run_id=$1", run).Scan(&running); e != nil || running != 0 {
		t.Fatal(running, e)
	}
}

func TestReplanOutboxFailureRollsBackAttemptAndPlan(t *testing.T) {
	s, cas, run, c, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	rejectReplanEvidence(t, s, old["A"].ID)
	p.BaseVersion = 1
	p.Nodes[0].Goal = "correction"
	if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION fail_replan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='replan_attempt' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_replan BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_replan()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Replan(ctx, run, c, replanWire(t, p), configs, replanPolicy(), cas); e == nil {
		t.Fatal("missing fault")
	}
	w, e := s.Workflow(ctx, run)
	if e != nil || w.ActivePlanVersion != 1 {
		t.Fatal(w, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='replan_attempt'", run).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
