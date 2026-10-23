package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFanInMergeProvenanceConflictAndPublication(t *testing.T) {
	for _, mode := range []string{"agreement", "conflict", "race"} {
		t.Run(mode, func(t *testing.T) {
			conflict := mode == "conflict"
			s := testdb.New(t)
			ctx := context.Background()
			run, c := planFixture(t, s)
			p := fanPlan()
			if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
				t.Fatal(e)
			}
			casPath := filepath.Join(t.TempDir(), "cas")
			cas, e := artifact.New(casPath)
			if e != nil {
				t.Fatal(e)
			}
			defer cas.Close()
			ids := map[string]bool{}
			for i, n := range p.Nodes[:2] {
				if _, e = s.RefreshReady(ctx, run, c); e != nil {
					t.Fatal(e)
				}
				claim, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: n.Key, Role: n.Role}, time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.BuildSnapshot(ctx, claim, "mock.v1", n.Role+".v1", cas); e != nil {
					t.Fatal(e)
				}
				claim, e = s.StartTask(ctx, claim)
				if e != nil {
					t.Fatal(e)
				}
				value := "12"
				if conflict && i == 1 {
					value = "13"
				}
				a, e := s.PublishArtifact(ctx, claim, cas, n.OutputKind, "application/json", n.Role+".v1", []byte(`{"schema_version":"facts.v1","facts":[{"key":"paid_orders","value":`+value+`}]}`))
				if e != nil {
					t.Fatal(e)
				}
				ids[a.ID] = true
				if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'merge.fixture',true)", a.ID); e != nil {
					t.Fatal(e)
				}
				if e = s.ApplyVerdicts(ctx, run, c); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.RefreshReady(ctx, run, c); e != nil {
				t.Fatal(e)
			}
			aggregate, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregator", Role: "aggregate"}, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			snap, e := s.BuildSnapshot(ctx, aggregate, "mock.v1", "aggregate.v1", cas)
			if e != nil {
				t.Fatal(e)
			}
			aggregate, e = s.StartTask(ctx, aggregate)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "race" {
				encoded, e := json.Marshal(aggregate)
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("MGAR_TEST_CLAIM", string(encoded))
				t.Setenv("MGAR_TEST_CAS_ROOT", casPath)
				t.Setenv("MGAR_TEST_SNAPSHOT", snap.ID)
				wins, denied := 0, 0
				for _, out := range processBatch(t, s, "merge", run, 2) {
					if strings.Contains(out, "RESULT winner") {
						wins++
					}
					if strings.Contains(out, "RESULT denied") {
						denied++
					}
				}
				if wins != 1 || denied != 1 {
					t.Fatal(wins, denied)
				}
				var published, running int
				if e = s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM artifacts WHERE producer_task=$2),running_count FROM run_budgets WHERE run_id=$1", run, aggregate.TaskID).Scan(&published, &running); e != nil || published != 1 || running != 0 {
					t.Fatal(published, running, e)
				}
				return
			}
			if conflict {
				if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION fail_merge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='merge_rejected' THEN RAISE EXCEPTION 'synthetic merge fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_merge BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_merge()`); e != nil {
					t.Fatal(e)
				}
				if _, _, e = s.PublishMerge(ctx, aggregate, cas, snap.ID); e == nil || errors.Is(e, store.ErrMergeConflict) {
					t.Fatal("missing transactional fault", e)
				}
				var state string
				var running int
				if e = s.Pool.QueryRow(ctx, "SELECT t.state,b.running_count FROM task_instances t JOIN run_budgets b USING(run_id) WHERE t.task_id=$1", aggregate.TaskID).Scan(&state, &running); e != nil || state != "executing" || running != 1 {
					t.Fatal("partial rejection", state, running, e)
				}
				if _, e = s.Pool.Exec(ctx, "DROP TRIGGER fail_merge ON outbox_events"); e != nil {
					t.Fatal(e)
				}
			}
			a, r, e := s.PublishMerge(ctx, aggregate, cas, snap.ID)
			if conflict {
				if !errors.Is(e, store.ErrMergeConflict) || r.CanFinalize || a.ID != "" {
					t.Fatal(a, r, e)
				}
				var n int
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE producer_task=$1", aggregate.TaskID).Scan(&n); e != nil || n != 0 {
					t.Fatal("conflict published", n, e)
				}
				w, e := s.FinalizeWorkflow(ctx, run, c)
				if e != nil || w.State != "failed" {
					t.Fatal("conflict left without honest exit", w, e)
				}
				return
			}
			if e != nil || a.ID == "" || a.Verified || len(r.Facts) != 1 || len(r.Facts[0].Sources) != 2 {
				t.Fatal(a, r, e)
			}
			for _, ref := range r.Facts[0].Sources {
				if !ids[ref.ID] {
					t.Fatal("fabricated provenance", ref)
				}
			}
			w, e := s.FinalizeWorkflow(ctx, run, c)
			if e != nil || w.State == "succeeded" {
				t.Fatal("merge self-verified", w, e)
			}
		})
	}
}
func TestFanInRequiredFailureHonestFinalState(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, fanPlan()); e != nil {
		t.Fatal(e)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	if _, e := s.Pool.Exec(ctx, "UPDATE task_instances SET state='failed',revision=revision+1 WHERE run_id=$1 AND node_key='B'", run); e != nil {
		t.Fatal(e)
	}
	w, e := (scheduler.Scheduler{Store: s}).ResolveFanIn(ctx, run, c)
	if e != nil || w.State != "failed" {
		t.Fatal(w, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events e JOIN task_instances t ON t.task_id=e.entity_id WHERE t.run_id=$1 AND t.node_key='C' AND e.kind IN ('task_ready','output_ready')", run).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed dependency published", n, e)
	}
}
