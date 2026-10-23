package integration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func incrementalFixture(t *testing.T) (*store.Store, *artifact.CAS, string, store.OrchestratorClaim, planner.Proposal, map[string]inputcontext.ExecutionConfig, map[string]artifact.Metadata) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	run, owner := planFixture(t, s)
	p := fanPlan()
	p.Nodes[2].Role = "action"
	p.Nodes[2].OutputKind = "patch"
	p.Nodes[2].RequiredInputs = []string{"A", "B"}
	if _, e := s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cas.Close() })
	configs := map[string]inputcontext.ExecutionConfig{}
	artifacts := map[string]artifact.Metadata{}
	for _, n := range p.Nodes {
		configs[n.Key] = inputcontext.ExecutionConfig{ModelVersion: "mock.v1", PolicyVersion: n.Role + ".v1", OutputToolVersion: n.Role + ".v1"}
		if _, e = s.RefreshReady(ctx, run, owner); e != nil {
			t.Fatal(e)
		}
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-" + n.Key, Role: n.Role}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BuildSnapshot(ctx, c, "mock.v1", n.Role+".v1", cas); e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		a, e := s.PublishArtifact(ctx, c, cas, n.OutputKind, "text/plain", n.Role+".v1", []byte("original-"+n.Key))
		if e != nil {
			t.Fatal(e)
		}
		artifacts[n.Key] = a
		if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.fixture',true)", a.ID); e != nil {
			t.Fatal(e)
		}
		if e = s.ApplyVerdicts(ctx, run, owner); e != nil {
			t.Fatal(e)
		}
	}
	return s, cas, run, owner, p, configs, artifacts
}

func TestInvalidateReuseRealPGPreservesHistoryAndNewInputs(t *testing.T) {
	s, cas, run, owner, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	before := map[string]string{}
	for key, a := range old {
		var body string
		if e := s.Pool.QueryRow(ctx, "SELECT to_jsonb(t)::text FROM task_instances t WHERE task_id=$1", a.ProducerTaskID).Scan(&body); e != nil {
			t.Fatal(e)
		}
		before[key] = body
	}
	p.BaseVersion = 1
	p.Nodes[0].Goal = "new upstream evidence"
	version, decision, e := s.ProposeIncremental(ctx, run, owner, p, configs, cas)
	if e != nil || version != 2 || len(decision.Reuse) != 1 || decision.Reuse["B"] != old["B"].ID || len(decision.Recompute) != 2 {
		t.Fatal(version, decision, e)
	}
	var reusedTask, reusedID string
	if e = s.Pool.QueryRow(ctx, "SELECT task_instance_id,reuse_artifact_id FROM plan_nodes WHERE run_id=$1 AND plan_version=2 AND node_key='B'", run).Scan(&reusedTask, &reusedID); e != nil || reusedTask != old["B"].ProducerTaskID || reusedID != old["B"].ID {
		t.Fatal(reusedTask, reusedID, e)
	}
	for key, a := range old {
		var body string
		if e = s.Pool.QueryRow(ctx, "SELECT to_jsonb(t)::text FROM task_instances t WHERE task_id=$1", a.ProducerTaskID).Scan(&body); e != nil || body != before[key] {
			t.Fatal("history changed", key, e)
		}
	}
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "new-A", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "wrong-model.v1", "research.v1", cas); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("plan config bypass", e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	newer, e := s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", []byte("new A evidence"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.fixture',true)", newer.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyVerdicts(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "new-C", Role: "action"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	foundOldB := false
	for _, ref := range snap.Artifacts {
		if ref.ProducerNode == "B" {
			foundOldB = ref.ID == old["B"].ID && ref.SourcePlanVersion == 1
		}
	}
	if !foundOldB {
		t.Fatal("reuse source version rewritten")
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishArtifact(ctx, c, cas, "patch", "text/plain", "wrong-tool.v1", []byte("rejected recomputation")); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("wrong tool accepted", e)
	}
	previous, e := cas.Read(old["C"].Hash, old["C"].ByteLength)
	if e != nil || string(previous) != "original-C" {
		t.Fatal("failed recompute changed old bytes", e)
	}
	current, e := s.PublishArtifact(ctx, c, cas, "patch", "text/plain", "action.v1", []byte("new-C"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.fixture',true)", current.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyVerdicts(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	w, e := s.FinalizeWorkflow(ctx, run, owner)
	if e != nil || w.State != "verifying" {
		t.Fatal(w, e)
	}
	w, e = s.FinalizeWorkflow(ctx, run, owner)
	if e != nil || w.State != "succeeded" {
		t.Fatal(w, e)
	}
	var used, running int
	if e = s.Pool.QueryRow(ctx, "SELECT used_delegations,running_count FROM run_budgets WHERE run_id=$1", run).Scan(&used, &running); e != nil || used != 5 || running != 0 {
		t.Fatal("reuse charged/reset delegation budget", used, running, e)
	}
}

func TestInvalidateReuseConfigAndRevocationDoNotAdoptOldResults(t *testing.T) {
	s, cas, run, owner, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	p.BaseVersion = 1
	cfg := configs["B"]
	cfg.OutputToolVersion = "data.v2"
	configs["B"] = cfg
	if _, e := s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.revoked',false)", old["A"].ID); e != nil {
		t.Fatal(e)
	}
	_, d, e := s.ProposeIncremental(ctx, run, owner, p, configs, cas)
	if e != nil || len(d.Reuse) != 0 || len(d.Recompute) != 3 {
		t.Fatal(d, e)
	}
	if d.Recompute["B"] != "configuration_changed" || d.Recompute["A"] != "no_valid_provenance" {
		t.Fatal("wrong invalidation reasons", d)
	}
}

func TestReuseAllValidThenLatestRevocationFailsCurrentPlan(t *testing.T) {
	s, cas, run, owner, p, configs, old := incrementalFixture(t)
	ctx := context.Background()
	p.BaseVersion = 1
	_, d, e := s.ProposeIncremental(ctx, run, owner, p, configs, cas)
	if e != nil || len(d.Reuse) != 3 {
		t.Fatal(d, e)
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.late-revocation',false)", old["B"].ID); e != nil {
		t.Fatal(e)
	}
	w, e := s.FinalizeWorkflow(ctx, run, owner)
	if e != nil || w.State != "failed" {
		t.Fatal("revoked reuse counted succeeded", w, e)
	}
	var state string
	if e = s.Pool.QueryRow(ctx, "SELECT state FROM task_instances WHERE task_id=$1", old["B"].ProducerTaskID).Scan(&state); e != nil || state != "succeeded" {
		t.Fatal("historical task changed", state, e)
	}
}

func TestInvalidateReuseRollsBackPlanAndMappingOnOutboxFailure(t *testing.T) {
	s, cas, run, owner, p, configs, _ := incrementalFixture(t)
	ctx := context.Background()
	p.BaseVersion = 1
	p.Nodes[0].Goal = "changed"
	if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION fail_incremental_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER incremental_fault BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_incremental_outbox()`); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.ProposeIncremental(ctx, run, owner, p, configs, cas); e == nil {
		t.Fatal("missing injected failure")
	}
	for _, table := range []string{"plans", "plan_nodes", "plan_edges", "plan_node_configs"} {
		var n int
		if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE run_id=$1 AND plan_version=2", run).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial incremental commit", table, n, e)
		}
	}
	var version int
	if e := s.Pool.QueryRow(ctx, "SELECT active_plan_version FROM workflow_runs WHERE run_id=$1", run).Scan(&version); e != nil || version != 1 {
		t.Fatal(version, e)
	}
}

func TestInvalidateReuseCrossProcessPlanCAS(t *testing.T) {
	s, cas, run, _, p, configs, old := incrementalFixture(t)
	p.BaseVersion = 1
	p.Nodes[0].Goal = "new evidence"
	plan, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	config, e := json.Marshal(configs)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_TEST_PROPOSAL", string(plan))
	t.Setenv("MGAR_TEST_CONFIGS", string(config))
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
	for _, out := range processBatch(t, s, "incremental", run, 2) {
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
}

func TestReuseExecutionConfigCannotBeBypassedByClaimNodeKey(t *testing.T) {
	for _, mode := range []string{"model", "tool"} {
		t.Run(mode, func(t *testing.T) {
			s, cas, run, owner, p, configs, _ := incrementalFixture(t)
			ctx := context.Background()
			p.BaseVersion = 1
			p.Nodes[0].Goal = "changed"
			if _, _, e := s.ProposeIncremental(ctx, run, owner, p, configs, cas); e != nil {
				t.Fatal(e)
			}
			if _, e := s.RefreshReady(ctx, run, owner); e != nil {
				t.Fatal(e)
			}
			c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "new-A", Role: "research"}, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			forged := c
			forged.NodeKey = "missing-node"
			if mode == "model" {
				if _, e = s.BuildSnapshot(ctx, forged, "wrong-model.v1", "research.v1", cas); !errors.Is(e, store.ErrPolicy) {
					t.Fatal("forged node bypassed model config", e)
				}
				return
			}
			if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
				t.Fatal(e)
			}
			c, e = s.StartTask(ctx, c)
			if e != nil {
				t.Fatal(e)
			}
			forged = c
			forged.NodeKey = "missing-node"
			if _, e = s.PublishArtifact(ctx, forged, cas, "finding", "text/plain", "wrong-tool.v1", []byte("unauthorized tool version")); !errors.Is(e, store.ErrPolicy) {
				t.Fatal("forged node bypassed output config", e)
			}
		})
	}
}
