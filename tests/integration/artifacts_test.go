package integration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func TestArtifactCASCrossProcessDedupDistinctProducers(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, owner := planFixture(t, s)
	p := simplePlan()
	for _, key := range []string{"B", "C", "D"} {
		p.Nodes = append(p.Nodes, planner.Node{Key: key, Role: "research", Goal: "parallel", RequiredInputs: []string{}, OutputKind: "finding"})
	}
	if _, e := s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "cas")
	t.Setenv("MGAR_TEST_CAS_ROOT", root)
	for _, out := range processBatch(t, s, "artifact", run, 4) {
		if !strings.Contains(out, "RESULT published") {
			t.Fatal(out)
		}
	}
	cas, e := artifact.New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	hashes, e := cas.Inventory()
	if e != nil || len(hashes) != 1 {
		t.Fatal(hashes, e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(DISTINCT producer_task) FROM artifacts WHERE run_id=$1", run).Scan(&count); e != nil || count != 4 {
		t.Fatal(count, e)
	}
}

func TestArtifactPublicationDedupProvenanceAndFencing(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, owner := planFixture(t, s)
	p := simplePlan()
	p.Nodes = append(p.Nodes, planner.Node{Key: "B", Role: "research", Goal: "independent", RequiredInputs: []string{}, OutputKind: "finding"})
	if _, e := s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	output := []byte(`{"statement":"same content, distinct producers","verified":false}`)
	ids := []string{}
	for i := 0; i < 2; i++ {
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker", Role: "research"}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
			t.Fatal(e)
		}
		wrong := c
		wrong.Role = "action"
		if _, e = s.PublishArtifact(ctx, wrong, cas, "finding", "application/json", "research.v1", output); !errors.Is(e, store.ErrPolicy) {
			t.Fatal("wrong role", e)
		}
		a, e := s.PublishArtifact(ctx, c, cas, "finding", "application/json", "research.v1", output)
		if e != nil {
			t.Fatal(e)
		}
		if a.ProducerTaskID != c.TaskID || a.StorageURI != "artifact://"+a.ID || a.Verified {
			t.Fatal("bad provenance", a)
		}
		ids = append(ids, a.ID)
		if _, e = s.PublishArtifact(ctx, c, cas, "finding", "application/json", "research.v1", output); e == nil {
			t.Fatal("stale revision accepted")
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("producer identities merged")
	}
	inventory, e := cas.Inventory()
	if e != nil || len(inventory) != 1 {
		t.Fatal(inventory, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE run_id=$1", run).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_instances WHERE run_id=$1 AND state='output_ready'", run).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}

func TestArtifactRollbackLeavesInventoryOrphanAndLeaseRejects(t *testing.T) {
	s, run, c := claimedFixture(t)
	ctx := context.Background()
	c, e := s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION fail_cas_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER injected_cas BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_cas_outbox()`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "fixture.v1", []byte("orphan")); e == nil {
		t.Fatal("expected rollback")
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE run_id=$1", run).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	inventory, e := cas.Inventory()
	if e != nil || len(inventory) != 1 {
		t.Fatal(inventory, e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "fixture.v1", []byte("expired")); !errors.Is(e, store.ErrLeaseExpired) {
		t.Fatal(e)
	}
}
