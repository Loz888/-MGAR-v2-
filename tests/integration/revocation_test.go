package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func readyDependentFixture(t *testing.T) (*store.Store, string, store.OrchestratorClaim) {
	t.Helper()
	s := testdb.New(t)
	run, orchestrator := planFixture(t, s)
	ctx := context.Background()
	if _, err := s.ProposeAndCommit(ctx, run, orchestrator, fanPlan()); err != nil {
		t.Fatal(err)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	fixtureOutput(t, s, run, "B", true, 1)
	if _, err := s.RefreshReady(ctx, run, orchestrator); err != nil {
		t.Fatal(err)
	}
	return s, run, orchestrator
}

func revokeDependency(t *testing.T, s *store.Store, run string) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(), `INSERT INTO artifact_verdicts(artifact_id,checker_version,passed)
	 SELECT t.result_artifact_id,'synthetic-revocation.v1',false
	 FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id
	 JOIN workflow_runs w ON w.run_id=n.run_id AND w.active_plan_version=n.plan_version
	 WHERE n.run_id=$1 AND n.node_key='A'`, run)
	if err != nil {
		t.Fatal(err)
	}
}

type authorizationSnapshot struct {
	State, Owner          string
	Epoch, Revision       int64
	Used, Running, Events int
}

func snapshotAuthorization(t *testing.T, s *store.Store, run, task string) authorizationSnapshot {
	t.Helper()
	var snap authorizationSnapshot
	err := s.Pool.QueryRow(context.Background(), `SELECT t.state,COALESCE(t.owner,''),t.epoch,t.revision,
	 b.used_delegations,b.running_count,(SELECT count(*) FROM workflow_events e WHERE e.run_id=t.run_id)
	 FROM task_instances t JOIN run_budgets b ON b.run_id=t.run_id WHERE t.task_id=$1 AND t.run_id=$2`, task, run).
		Scan(&snap.State, &snap.Owner, &snap.Epoch, &snap.Revision, &snap.Used, &snap.Running, &snap.Events)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestClaimRejectsRevokedReadyArtifact(t *testing.T) {
	s, run, _ := readyDependentFixture(t)
	ctx := context.Background()
	var task string
	if err := s.Pool.QueryRow(ctx, "SELECT task_id FROM task_instances WHERE run_id=$1 AND node_key='C'", run).Scan(&task); err != nil {
		t.Fatal(err)
	}
	before := snapshotAuthorization(t, s, run, task)
	revokeDependency(t, s, run)
	if _, err := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregate-1", Role: "aggregate"}, time.Minute); !errors.Is(err, store.ErrStaleInput) {
		t.Fatalf("revoked ready input authorized: %v", err)
	}
	if after := snapshotAuthorization(t, s, run, task); after != before {
		t.Fatalf("denial changed task/budget/audit: before=%+v after=%+v", before, after)
	}
}

func TestTaskGuardRejectsForgedPlanGeneration(t *testing.T) {
	for _, operation := range []string{"start", "renew", "save"} {
		t.Run(operation, func(t *testing.T) {
			s, run, claim := claimedFixture(t)
			ctx := context.Background()
			var err error
			if operation == "save" {
				claim, err = s.StartTask(ctx, claim)
				if err != nil {
					t.Fatal(err)
				}
			}
			artifact := artifactForClaim(t, s, claim)
			p := simplePlan()
			p.BaseVersion = 1
			if _, err = s.ProposeAndCommit(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}, p); err != nil {
				t.Fatal(err)
			}
			claim.PlanVersion = 2 // Forging a label cannot move an old task into the active plan.
			before := snapshotAuthorization(t, s, run, claim.TaskID)
			switch operation {
			case "start":
				_, err = s.StartTask(ctx, claim)
			case "renew":
				_, err = s.RenewTask(ctx, claim, time.Minute)
			case "save":
				err = s.CommitOutput(ctx, claim, artifact)
			}
			if !errors.Is(err, store.ErrFenced) {
				t.Fatalf("old task with forged generation accepted by %s: %v", operation, err)
			}
			if after := snapshotAuthorization(t, s, run, claim.TaskID); after != before {
				t.Fatalf("denial changed task/budget/audit: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestTaskGuardRejectsRevokedFixedInput(t *testing.T) {
	for _, operation := range []string{"start", "renew", "save", "takeover"} {
		t.Run(operation, func(t *testing.T) {
			s, run, _ := readyDependentFixture(t)
			ctx := context.Background()
			claim, err := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregate-1", Role: "aggregate"}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			var artifact string
			if operation == "save" {
				claim, err = s.StartTask(ctx, claim)
				if err != nil {
					t.Fatal(err)
				}
				err = s.Pool.QueryRow(ctx, `INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
				 VALUES($1,'report',repeat('1',64),$2,'artifact.v1',$3,$4,0) RETURNING artifact_id`, run, "artifact://fixture-"+claim.TaskID, claim.TaskID, claim.PlanVersion).Scan(&artifact)
				if err != nil {
					t.Fatal(err)
				}
			}
			if operation == "takeover" {
				if _, err = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", claim.TaskID); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotAuthorization(t, s, run, claim.TaskID)
			revokeDependency(t, s, run)
			switch operation {
			case "start":
				_, err = s.StartTask(ctx, claim)
			case "renew":
				_, err = s.RenewTask(ctx, claim, time.Minute)
			case "save":
				err = s.CommitOutput(ctx, claim, artifact)
			case "takeover":
				_, err = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregate-2", Role: "aggregate"}, time.Minute)
			}
			if !errors.Is(err, store.ErrStaleInput) {
				t.Fatalf("revoked fixed input authorized by %s: %v", operation, err)
			}
			if after := snapshotAuthorization(t, s, run, claim.TaskID); after != before {
				t.Fatalf("denial changed task/budget/audit: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestClaimRejectsInvalidFixedSnapshot(t *testing.T) {
	for _, mode := range []string{"missing", "hash", "producer", "source_plan", "extra"} {
		t.Run(mode, func(t *testing.T) {
			s := testdb.New(t)
			ctx := context.Background()
			run, orchestrator := planFixture(t, s)
			p := fanPlan()
			if _, err := s.ProposeAndCommit(ctx, run, orchestrator, p); err != nil {
				t.Fatal(err)
			}
			version := 1
			if mode == "source_plan" {
				p.BaseVersion = 1
				if _, err := s.ProposeAndCommit(ctx, run, orchestrator, p); err != nil {
					t.Fatal(err)
				}
				version = 2
			}
			fixtureOutput(t, s, run, "A", true, version)
			fixtureOutput(t, s, run, "B", true, version)
			// Seed a stale/corrupt ready row directly, bypassing the readiness API.
			// The claim boundary must independently reject this persisted snapshot.
			_, err := s.Pool.Exec(ctx, `INSERT INTO task_inputs(task_id,run_id,artifact_id,content_sha256,producer_node,source_plan_version)
			 SELECT child.task_instance_id,$1,a.artifact_id,
			 CASE WHEN $3='hash' AND parent.node_key='A' THEN repeat('0',64) ELSE a.content_sha256 END,
			 CASE WHEN $3='producer' AND parent.node_key='A' THEN 'foreign_node' ELSE parent.node_key END,
			 CASE WHEN $3='source_plan' THEN 1 ELSE a.source_plan_version END
			 FROM plan_nodes child JOIN plan_nodes parent ON parent.run_id=child.run_id AND parent.plan_version=child.plan_version
			 JOIN task_instances t ON t.task_id=parent.task_instance_id JOIN artifacts a ON a.artifact_id=t.result_artifact_id
			 WHERE child.run_id=$1 AND child.plan_version=$2 AND child.node_key='C' AND parent.node_key IN ('A','B')
			 AND ($3<>'missing' OR parent.node_key='A')`, run, version, mode)
			if err != nil {
				t.Fatal(err)
			}
			var task string
			err = s.Pool.QueryRow(ctx, `UPDATE task_instances t SET state='ready' FROM plan_nodes n
			 WHERE n.task_instance_id=t.task_id AND n.run_id=$1 AND n.plan_version=$2 AND n.node_key='C' RETURNING t.task_id`, run, version).Scan(&task)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "extra" {
				var artifact string
				err = s.Pool.QueryRow(ctx, `INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
				 SELECT $1,'finding',repeat('3',64),'artifact://extra-synthetic-input','artifact.v1',n.task_instance_id,$2,0
				 FROM plan_nodes n WHERE n.run_id=$1 AND n.plan_version=$2 AND n.node_key='A' RETURNING artifact_id`, run, version).Scan(&artifact)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Pool.Exec(ctx, `INSERT INTO task_inputs(task_id,run_id,artifact_id,content_sha256,producer_node,source_plan_version)
				 VALUES($1,$2,$3,repeat('3',64),'A',$4)`, task, run, artifact, version); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotAuthorization(t, s, run, task)
			if _, err = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregate-1", Role: "aggregate"}, time.Minute); !errors.Is(err, store.ErrStaleInput) {
				t.Fatalf("invalid %s snapshot authorized: %v", mode, err)
			}
			if after := snapshotAuthorization(t, s, run, task); after != before {
				t.Fatalf("denial changed task/budget/audit: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestClaimRechecksVerdictCommittedDuringArtifactLockWait(t *testing.T) {
	s, run, _ := readyDependentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verdictTx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verdictTx.Rollback(context.Background())
	_, err = verdictTx.Exec(ctx, `INSERT INTO artifact_verdicts(artifact_id,checker_version,passed)
	 SELECT t.result_artifact_id,'synthetic-concurrent-revocation.v1',false FROM task_instances t
	 WHERE t.run_id=$1 AND t.node_key='A'`, run)
	if err != nil {
		t.Fatal(err)
	}
	// A dedicated one-connection pool makes the blocked authorizer PID observable.
	cfg := s.Pool.Config().Copy()
	cfg.MaxConns = 1
	authorizers, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer authorizers.Close()
	var pid int
	if err = authorizers.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	claimStore := &store.Store{Pool: authorizers}
	result := make(chan error, 1)
	go func() {
		_, err := claimStore.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "aggregate-concurrent", Role: "aggregate"}, time.Minute)
		result <- err
	}()
	// Wait for a real PostgreSQL lock dependency, without a timing assumption.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err = s.Pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err = <-result:
			t.Fatalf("claim did not serialize with uncommitted verdict: %v", err)
		case <-ctx.Done():
			t.Fatal("claim never waited on the pending verdict")
		case <-ticker.C:
		}
	}
	if err = verdictTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, store.ErrStaleInput) {
			t.Fatalf("claim missed verdict committed during artifact lock wait: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("claim did not finish after verdict commit")
	}
}

func TestRecoveryReleasesOldPlanReservation(t *testing.T) {
	s, run, old := claimedFixture(t)
	ctx := context.Background()
	orchestrator := store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}
	if _, err := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_concurrency=1 WHERE run_id=$1", run); err != nil {
		t.Fatal(err)
	}
	p := simplePlan()
	p.BaseVersion = 1
	if _, err := s.ProposeAndCommit(ctx, run, orchestrator, p); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpired(ctx, run, orchestrator); err != nil {
		t.Fatal(err)
	}
	settled := snapshotAuthorization(t, s, run, old.TaskID)
	if settled.State != "cancelled" || settled.Used != 1 || settled.Running != 0 {
		t.Fatalf("old generation reservation leaked or delegation refunded: %+v", settled)
	}
	if _, err := s.RefreshReady(ctx, run, orchestrator); err != nil {
		t.Fatal(err)
	}
	current, err := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-current", Role: "research"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if current.PlanVersion != 2 || current.TaskID == old.TaskID {
		t.Fatalf("recovery reused old task: %+v", current)
	}
	if snapshot := snapshotAuthorization(t, s, run, current.TaskID); snapshot.Used != 2 || snapshot.Running != 1 {
		t.Fatalf("new generation did not reserve its own delegation: %+v", snapshot)
	}
}
