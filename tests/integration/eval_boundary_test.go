package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/google/uuid"
)

// P7-03 conflict reproduction: a no-internal-verifier arm cannot use the strict
// production dependency path unchanged. Passing this test proves the guard,
// NOT that a baseline was implemented or an evaluation succeeded.
func TestEvalNoVerifierCannotUseStrictDependencyPath(t *testing.T) {
	ctx := context.Background()
	s := testdb.New(t)
	run := uuid.NewString()
	_, owner, e := s.CreateRunClaimed(ctx, run, "synthetic no-verifier boundary", 4, "eval-boundary", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginPlanning(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	p := planner.Proposal{SchemaVersion: "plan.v1", Rationale: "reproduce mandatory verification boundary", Nodes: []planner.Node{
		{Key: "source", Role: "research", Goal: "produce candidate", RequiredInputs: []string{}, OutputKind: "finding"},
		{Key: "sink", Role: "aggregate", Goal: "consume candidate", RequiredInputs: []string{"source"}, OutputKind: "report"},
	}, Edges: []planner.Edge{{From: "source", To: "sink"}}}
	if _, e = s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "source-worker", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "fixture.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishArtifact(ctx, c, cas, "finding", "application/json", "fixture.v1", []byte(`{"schema_version":"facts.v1","facts":[{"key":"candidate","value":true}]}`))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyVerdicts(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	_, e = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "sink-worker", Role: "aggregate"}, time.Minute)
	if !errors.Is(e, store.ErrNoReady) {
		t.Fatal("unverified dependency unexpectedly runnable", e)
	}
	var source, sink string
	var verdicts int
	if e = s.Pool.QueryRow(ctx, "SELECT state FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&source); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT state FROM task_instances WHERE run_id=$1 AND node_key='sink'", run).Scan(&sink); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifact_verdicts WHERE artifact_id=$1", a.ID).Scan(&verdicts); e != nil {
		t.Fatal(e)
	}
	w, e := s.FinalizeWorkflow(ctx, run, owner)
	if e != nil {
		t.Fatal(e)
	}
	if source != "output_ready" || sink != "pending" || verdicts != 0 || w.State != "running" {
		t.Fatal("boundary changed", source, sink, verdicts, w.State)
	}
	t.Logf("source_state=%s sink_state=%s claim=NO_READY_TASK workflow=%s verdict_count=%d", source, sink, w.State, verdicts)
}
