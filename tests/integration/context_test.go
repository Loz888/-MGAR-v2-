package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func contextFixture(t *testing.T) (*store.Store, *artifact.CAS, store.TaskClaim, artifact.Metadata) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	run, owner := planFixture(t, s)
	p := simplePlan()
	p.Nodes = append(p.Nodes, planner.Node{Key: "B", Role: "action", Goal: "use A", RequiredInputs: []string{"A"}, OutputKind: "patch"})
	p.Edges = []planner.Edge{{From: "A", To: "B"}}
	if _, e := s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cas.Close() })
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "research", Role: "research"}, time.Minute)
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
	a, e := s.PublishArtifact(ctx, c, cas, "finding", "application/json", "research.v1", []byte(`{"clue":"duplicate paid orders","verified":false}`))
	if e != nil {
		t.Fatal(e)
	}
	// Explicit trusted Verifier fixture: not an independent execution claim.
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.verifier.v1',true)", a.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyVerdicts(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "action", Role: "action"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	return s, cas, c, a
}

func TestContextSnapshotReplayACLAndLineage(t *testing.T) {
	s, cas, c, a := contextFixture(t)
	ctx := context.Background()
	snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	if snap.ID != replay.ID || snap.Hash() != replay.Hash() || !bytes.Equal(snap.Bytes(), replay.Bytes()) {
		t.Fatal("snapshot drift")
	}
	if len(snap.Artifacts) != 1 || snap.Artifacts[0].ID != a.ID || snap.Artifacts[0].ProducerTaskID != a.ProducerTaskID || snap.Artifacts[0].ToolVersion != "research.v1" {
		t.Fatal(snap)
	}
	command := exec.CommandContext(ctx, "python", "-c", "import sys;from mgar_agents.core.schemas import ContextSnapshot;print(ContextSnapshot(sys.stdin.read()).content_sha256)")
	command.Stdin = bytes.NewReader(snap.Bytes())
	wire, e := command.CombinedOutput()
	if e != nil || string(bytes.TrimSpace(wire)) != snap.Hash() {
		t.Fatalf("Go/Python snapshot mismatch: %s %v", wire, e)
	}
	body, e := s.ReadInput(ctx, c, cas, snap.ID, a.ID)
	if e != nil || !bytes.Contains(body, []byte("duplicate paid orders")) {
		t.Fatal(e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "different.v1", "action.v1", cas); !errors.Is(e, store.ErrStaleInput) {
		t.Fatal("config replacement accepted", e)
	}
	if _, e = s.ReadInput(ctx, c, cas, snap.ID, "00000000-0000-4000-8000-000000000001"); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("unlisted artifact", e)
	}
	wrong := c
	wrong.Role = "research"
	if _, e = s.ReadInput(ctx, wrong, cas, snap.ID, a.ID); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("role forged", e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE context_snapshots SET content_sha256=repeat('0',64) WHERE task_id=$1", c.TaskID); e == nil {
		t.Fatal("snapshot mutable")
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.PublishArtifact(ctx, c, cas, "patch", "text/plain", "action.v1", []byte("synthetic candidate"))
	if e != nil {
		t.Fatal(e)
	}
	var snapshotID string
	if e = s.Pool.QueryRow(ctx, "SELECT snapshot_id FROM artifact_contexts WHERE artifact_id=$1", out.ID).Scan(&snapshotID); e != nil || snapshotID != snap.ID {
		t.Fatal(snapshotID, e)
	}
}

func TestContextSnapshotCrossProcessReplay(t *testing.T) {
	s, cas, c, _ := contextFixture(t)
	claim, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_TEST_CLAIM", string(claim))
	root := filepath.Join(t.TempDir(), "replay-cas")
	// Copy the actual authorized object into the second trusted replica root.
	snapshot, e := s.BuildSnapshot(context.Background(), c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	replica, e := artifact.New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer replica.Close()
	for _, r := range snapshot.Artifacts {
		body, e := cas.Read(r.Hash, r.ByteLength)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = replica.Put(body); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("MGAR_TEST_CAS_ROOT", root)
	for _, out := range processBatch(t, s, "context", c.RunID, 2) {
		if !bytes.Contains([]byte(out), []byte(snapshot.Hash())) {
			t.Fatal(out)
		}
	}
}

func TestContextACLForeignSnapshotAndRevokedOrCorruptInput(t *testing.T) {
	s, cas, c, a := contextFixture(t)
	ctx := context.Background()
	snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	otherRun, otherOwner := planFixture(t, s)
	if _, e = s.ProposeAndCommit(ctx, otherRun, otherOwner, simplePlan()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, otherRun, otherOwner); e != nil {
		t.Fatal(e)
	}
	foreign, e := s.ClaimTask(ctx, otherRun, store.WorkerIdentity{Owner: "foreign", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadInput(ctx, foreign, cas, snap.ID, a.ID); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("cross Run accepted", e)
	}
	// A real, existing same-Run private artifact remains outside the fixed inputs.
	var private string
	if e = s.Pool.QueryRow(ctx, `INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
 VALUES($1,'trace',$2,'artifact://private-fixture','artifact.v1',$3,1,$4) RETURNING artifact_id`, c.RunID, a.Hash, a.ProducerTaskID, a.ByteLength).Scan(&private); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadInput(ctx, c, cas, snap.ID, private); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("same-run private artifact accepted", e)
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic.revocation',false)", a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas); !errors.Is(e, store.ErrStaleInput) {
		t.Fatal("revoked accepted", e)
	}
}
