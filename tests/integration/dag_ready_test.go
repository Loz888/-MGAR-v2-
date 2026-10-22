package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"sync"
	"testing"
)

func TestDependencyInputSnapshotCannotBeInjected(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, fanPlan()); e != nil {
		t.Fatal(e)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	fixtureOutput(t, s, run, "B", true, 1)
	_, e := s.Pool.Exec(ctx, `INSERT INTO task_inputs(task_id,run_id,artifact_id,content_sha256,producer_node,source_plan_version)
 SELECT child.task_id,$1,a.artifact_id,repeat('0',64),'A',1 FROM task_instances child,task_instances parent,artifacts a
 WHERE child.run_id=$1 AND child.node_key='C' AND parent.run_id=$1 AND parent.node_key='A' AND a.artifact_id=parent.result_artifact_id`, run)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, run, c); !errors.Is(e, store.ErrStaleInput) {
		t.Fatalf("injected snapshot not rejected: %v", e)
	}
	var state string
	_ = s.Pool.QueryRow(ctx, "SELECT state FROM task_instances WHERE run_id=$1 AND node_key='C'", run).Scan(&state)
	if state != "pending" {
		t.Fatal("partial ready transition after rejected snapshot")
	}
}

func fanPlan() planner.Proposal {
	p := simplePlan()
	p.Nodes = append(p.Nodes, planner.Node{Key: "B", Role: "data", Goal: "synthetic data", RequiredInputs: []string{}, OutputKind: "sql_evidence"}, planner.Node{Key: "C", Role: "aggregate", Goal: "combine", RequiredInputs: []string{}, OutputKind: "report"})
	p.Edges = []planner.Edge{{From: "A", To: "C"}, {From: "B", To: "C"}}
	return p
}

// fixtureOutput inserts synthetic DB metadata; it does not claim to run a Verifier or Artifact CAS.
func fixtureOutput(t *testing.T, s *store.Store, run, key string, passed bool, sourceVersion int) {
	t.Helper()
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	var task, kind string
	e = tx.QueryRow(ctx, "SELECT n.task_instance_id,n.requirements->>'required_output_kind' FROM plan_nodes n JOIN workflow_runs w ON w.run_id=n.run_id AND n.plan_version=w.active_plan_version WHERE n.run_id=$1 AND n.node_key=$2", run, key).Scan(&task, &kind)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256([]byte("synthetic fixture:" + task))
	var artifact string
	e = tx.QueryRow(ctx, "INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length) VALUES($1,$2,$3,$4,'artifact.v1',$5,$6,0) RETURNING artifact_id", run, kind, hex.EncodeToString(hash[:]), "artifact://fixture-"+task, task, sourceVersion).Scan(&artifact)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic-fixture.v1',$2)", artifact, passed); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "UPDATE task_instances SET state='succeeded',result_artifact_id=$2,revision=revision+1 WHERE task_id=$1", task, artifact); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestReadyFanInDuplicateAndCrossProcess(t *testing.T) {
	s := testdb.New(t)
	run, claim := planFixture(t, s)
	ctx := context.Background()
	if _, e := s.ProposeAndCommit(ctx, run, claim, fanPlan()); e != nil {
		t.Fatal(e)
	}
	sched := scheduler.Scheduler{Store: s}
	ready, e := sched.GetReady(ctx, run, claim)
	if e != nil || len(ready) != 2 {
		t.Fatalf("roots: %v %v", ready, e)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	ready, e = sched.GetReady(ctx, run, claim)
	if e != nil || len(ready) != 1 || ready[0].NodeKey != "B" {
		t.Fatalf("premature fanin: %v %v", ready, e)
	}
	fixtureOutput(t, s, run, "B", true, 1)
	_ = processBatch(t, s, "ready", run, 2)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := sched.GetReady(ctx, run, claim); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events e JOIN task_instances t ON t.task_id=e.entity_id WHERE t.run_id=$1 AND t.node_key='C' AND e.kind='task_ready'", run).Scan(&n)
	if e != nil || n != 1 {
		t.Fatalf("duplicate ready events %d %v", n, e)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_inputs i JOIN task_instances t USING(task_id) WHERE t.run_id=$1 AND t.node_key='C'", run).Scan(&n)
	if n != 2 {
		t.Fatalf("input snapshot size %d", n)
	}
}
func TestDependencyFailureAndStaleArtifactBlocked(t *testing.T) {
	for _, mode := range []string{"failed", "cancelled", "unverified", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s := testdb.New(t)
			ctx := context.Background()
			run, c := planFixture(t, s)
			p := fanPlan()
			if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
				t.Fatal(e)
			}
			if mode == "stale" {
				p.BaseVersion = 1
				if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "failed" || mode == "cancelled" {
				if _, e := s.Pool.Exec(ctx, "UPDATE task_instances SET state=$2 WHERE run_id=$1 AND node_key='A'", run, mode); e != nil {
					t.Fatal(e)
				}
			} else {
				fixtureOutput(t, s, run, "A", mode != "unverified", 1)
			}
			if _, e := s.RefreshReady(ctx, run, c); e != nil {
				t.Fatal(e)
			}
			var state string
			e := s.Pool.QueryRow(ctx, "SELECT t.state FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id JOIN workflow_runs w ON w.run_id=n.run_id AND w.active_plan_version=n.plan_version WHERE n.run_id=$1 AND n.node_key='C'", run).Scan(&state)
			if e != nil || state != "blocked" {
				t.Fatalf("unsafe dependency %s %v", state, e)
			}
		})
	}
}
