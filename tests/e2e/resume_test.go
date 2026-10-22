package e2e

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func assertResumeWorkflowEvents(t *testing.T, s *store.Store, w store.Workflow, kinds ...string) {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(), `SELECT e.entity_revision,e.kind,
	 o.event_id IS NOT NULL AND o.event_kind=e.kind
	 FROM workflow_events e LEFT JOIN outbox_events o USING(event_id)
	 WHERE e.run_id=$1 AND e.entity_type='workflow' ORDER BY e.entity_revision`, w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var revision int64
		var kind string
		var delivered bool
		if err = rows.Scan(&revision, &kind, &delivered); err != nil {
			t.Fatal(err)
		}
		if i >= len(kinds) || revision != int64(i) || kind != kinds[i] || !delivered {
			t.Fatalf("incomplete workflow audit/outbox: index=%d revision=%d kind=%s outbox=%v expected=%v", i, revision, kind, delivered, kinds)
		}
		i++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(kinds) || w.Revision != int64(i-1) {
		t.Fatalf("workflow revisions lack events: events=%d revision=%d expected=%v", i, w.Revision, kinds)
	}
}

func TestResumeSafeBlockedAndInterrupted(t *testing.T) {
	for _, suspended := range []string{"blocked", "interrupted"} {
		for _, planned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/planned=%v", suspended, planned), func(t *testing.T) {
				ctx := context.Background()
				var s *store.Store
				var w store.Workflow
				var claim store.OrchestratorClaim
				var err error
				kinds := []string{"workflow_queued", "workflow_planning"}
				target := "planning"
				if planned {
					s, w, claim = lifecycleFixture(t, true)
					// This existing helper seeds synthetic metadata, not actual Agent/Verifier execution.
					seedSucceeded(t, s, w.RunID, "A")
					kinds = append(kinds, "plan_committed")
					target = "running"
				} else {
					s = testdb.New(t)
					w, err = s.CreateRun(ctx, "", "safe no-plan resume fixture", 24)
					if err != nil {
						t.Fatal(err)
					}
					claim, err = s.TakeOrchestrator(ctx, w.RunID, "safe-resume-orchestrator", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = s.BeginPlanning(ctx, w.RunID, claim); err != nil {
						t.Fatal(err)
					}
				}
				paused, err := s.InterruptWorkflow(ctx, w.RunID, claim, suspended, "synthetic_safe_resume_fixture")
				if err != nil || paused.State != suspended || paused.FinishedAt != nil {
					t.Fatalf("pause: %+v %v", paused, err)
				}
				resumed, err := s.ResumeWorkflow(ctx, w.RunID, claim)
				if err != nil || resumed.State != target || resumed.FinishedAt != nil || resumed.ActivePlanVersion != w.ActivePlanVersion || resumed.Revision != paused.Revision+1 {
					t.Fatalf("safe explicit resume: %+v %v", resumed, err)
				}
				persisted, err := s.Workflow(ctx, w.RunID)
				if err != nil || !reflect.DeepEqual(persisted, resumed) {
					t.Fatalf("resume was not persisted: %+v %v", persisted, err)
				}
				kinds = append(kinds, "workflow_"+suspended, "workflow_"+target)
				assertResumeWorkflowEvents(t, s, persisted, kinds...)
				var calls, tasks, artifacts int
				if err = s.Pool.QueryRow(ctx, `SELECT
				 (SELECT count(*) FROM model_calls WHERE run_id=$1),
				 (SELECT count(*) FROM task_instances WHERE run_id=$1),
				 (SELECT count(*) FROM artifacts WHERE run_id=$1)`, w.RunID).Scan(&calls, &tasks, &artifacts); err != nil {
					t.Fatal(err)
				}
				if calls != 0 || (planned && (tasks != 3 || artifacts != 1)) || (!planned && (tasks != 0 || artifacts != 0)) {
					t.Fatalf("resume executed work or changed fixture history: calls=%d tasks=%d artifacts=%d", calls, tasks, artifacts)
				}
			})
		}
	}
}

type resumeBudgetSnapshot struct {
	Used, Running, ReservedCalls, ReservedTokens, SettledCalls, SettledTokens int64
	ReservedCost, SettledCost                                                 string
	Attempts, Retries                                                         int
}

func resumeBudget(t *testing.T, s *store.Store, run string) resumeBudgetSnapshot {
	t.Helper()
	var snapshot resumeBudgetSnapshot
	err := s.Pool.QueryRow(context.Background(), `SELECT used_delegations,running_count,
	 reserved_model_calls,reserved_tokens,settled_model_calls,settled_tokens,
	 reserved_cost::text,settled_cost::text,
	 (SELECT COALESCE(sum(attempts),0) FROM task_instances WHERE run_id=$1),
	 (SELECT COALESCE(sum(retry_count),0) FROM task_instances WHERE run_id=$1)
	 FROM run_budgets WHERE run_id=$1`, run).Scan(&snapshot.Used, &snapshot.Running,
		&snapshot.ReservedCalls, &snapshot.ReservedTokens, &snapshot.SettledCalls, &snapshot.SettledTokens,
		&snapshot.ReservedCost, &snapshot.SettledCost, &snapshot.Attempts, &snapshot.Retries)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResumeUnknownOrReservedModelCallFailsClosed(t *testing.T) {
	for _, status := range []string{"unknown", "reserved"} {
		t.Run(status, func(t *testing.T) {
			s, w, orchestrator := lifecycleFixture(t, false)
			ctx := context.Background()
			if _, err := s.RefreshReady(ctx, w.RunID, orchestrator); err != nil {
				t.Fatal(err)
			}
			task, err := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "uncertain-fixture-worker", Role: "research"}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			// Only synthetic ledger rows: no Provider or external model is called.
			if _, err = s.Pool.Exec(ctx, `UPDATE run_budgets SET max_model_calls=1,reserved_model_calls=1,
			 max_tokens_est=200,reserved_tokens=123,max_cost_usd=1,reserved_cost=.1 WHERE run_id=$1`, w.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Pool.Exec(ctx, `INSERT INTO model_calls
			 (logical_call_id,run_id,task_id,provider,model,status,estimate_bound,reservation)
			 VALUES(gen_random_uuid(),$1,$2,'mock','synthetic-uncertain',$3,.1,'{"tokens":123}')`, w.RunID, task.TaskID, status); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", task.TaskID); err != nil {
				t.Fatal(err)
			}
			before := resumeBudget(t, s, w.RunID)
			recovered, err := (scheduler.Scheduler{Store: s}).RecoverRun(ctx, w.RunID, orchestrator.Owner)
			if err != nil || recovered.State != "interrupted" || recovered.FinishedAt != nil {
				t.Fatalf("uncertain recovery did not interrupt: %+v %v", recovered, err)
			}
			fresh, err := s.TakeOrchestrator(ctx, w.RunID, orchestrator.Owner, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ResumeWorkflow(ctx, w.RunID, fresh); !errors.Is(err, store.ErrUnsafeReplay) {
				t.Fatalf("uncertain call was resumed: %v", err)
			}
			if after := resumeBudget(t, s, w.RunID); after != before || after.ReservedCalls != 1 || after.ReservedTokens != 123 || after.ReservedCost != "0.100000" || after.Used != 1 || after.Running != 1 || after.Attempts != 1 || after.Retries != 0 {
				t.Fatalf("uncertain recovery changed reservations/attempts: before=%+v after=%+v", before, after)
			}
			persisted, err := s.Workflow(ctx, w.RunID)
			if err != nil || persisted.State != recovered.State || persisted.Revision != recovered.Revision {
				t.Fatalf("failed resume mutated workflow: %+v %v", persisted, err)
			}
			var calls int
			var actual string
			if err = s.Pool.QueryRow(ctx, "SELECT count(*),min(status) FROM model_calls WHERE run_id=$1", w.RunID).Scan(&calls, &actual); err != nil || calls != 1 || actual != status {
				t.Fatalf("recovery replayed/settled fixture model call: count=%d status=%s %v", calls, actual, err)
			}
			assertResumeWorkflowEvents(t, s, persisted, "workflow_queued", "workflow_planning", "plan_committed", "workflow_interrupted")
		})
	}
}

// resumeCommitFixtureOutput uses actual claim/start/output transactions. The
// Artifact row is synthetic metadata; this does not implement or validate CAS.
func resumeCommitFixtureOutput(t *testing.T, s *store.Store, w store.Workflow, orchestrator store.OrchestratorClaim) (store.TaskClaim, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.RefreshReady(ctx, w.RunID, orchestrator); err != nil {
		t.Fatal(err)
	}
	task, err := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "output-fixture-worker", Role: "research"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.StartTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	var artifact string
	err = s.Pool.QueryRow(ctx, `INSERT INTO artifacts
	 (run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
	 VALUES($1,'finding',repeat('3',64),$2,'artifact.v1',$3,$4,0) RETURNING artifact_id`,
		w.RunID, "artifact://resume-fixture-"+task.TaskID, task.TaskID, task.PlanVersion).Scan(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitOutput(ctx, task, artifact); err != nil {
		t.Fatal(err)
	}
	return task, artifact
}

func resumeFixtureVerdict(t *testing.T, s *store.Store, artifact string) {
	t.Helper()
	// Explicitly synthetic trusted-verdict fixture, not an actual independent Verifier run.
	if _, err := s.Pool.Exec(context.Background(), "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic-resume-fixture.v1',true)", artifact); err != nil {
		t.Fatal(err)
	}
}

func TestResumeExhaustedBudgetStaysBlockedWithoutAttempts(t *testing.T) {
	s, w, orchestrator := lifecycleFixture(t, true)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_delegations=1 WHERE run_id=$1", w.RunID); err != nil {
		t.Fatal(err)
	}
	_, artifact := resumeCommitFixtureOutput(t, s, w, orchestrator)
	resumeFixtureVerdict(t, s, artifact)
	if err := s.ApplyVerdicts(ctx, w.RunID, orchestrator); err != nil {
		t.Fatal(err)
	}
	before := resumeBudget(t, s, w.RunID)
	blocked, err := s.BlockIfBudgetExhausted(ctx, w.RunID, orchestrator)
	if err != nil || blocked.State != "blocked" || blocked.FinishedAt != nil {
		t.Fatalf("exhausted run not blocked: %+v %v", blocked, err)
	}
	if _, err = s.ResumeWorkflow(ctx, w.RunID, orchestrator); !errors.Is(err, store.ErrBudget) {
		t.Fatalf("exhausted delegation budget resumed: %v", err)
	}
	if _, err = s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "over-budget-worker", Role: "data"}, time.Minute); !errors.Is(err, store.ErrBadState) {
		t.Fatalf("blocked run admitted another worker: %v", err)
	}
	recovered, err := (scheduler.Scheduler{Store: s}).RecoverRun(ctx, w.RunID, orchestrator.Owner)
	if err != nil || recovered.State != "blocked" || recovered.Revision != blocked.Revision {
		t.Fatalf("automatic recovery reopened blocked run: %+v %v", recovered, err)
	}
	if after := resumeBudget(t, s, w.RunID); after != before || after.Used != 1 || after.Running != 0 || after.Attempts != 1 || after.Retries != 0 {
		t.Fatalf("blocked operations grew attempts/released delegation: before=%+v after=%+v", before, after)
	}
	assertResumeWorkflowEvents(t, s, recovered, "workflow_queued", "workflow_planning", "plan_committed", "workflow_blocked")
}

func TestResumeVerdictAndFinalizePersistSeparateCheckpoints(t *testing.T) {
	s, w, orchestrator := lifecycleFixture(t, false)
	ctx := context.Background()
	task, artifact := resumeCommitFixtureOutput(t, s, w, orchestrator)
	if err := s.ApplyVerdicts(ctx, w.RunID, orchestrator); err != nil {
		t.Fatal(err)
	}
	var state string
	var revision int64
	if err := s.Pool.QueryRow(ctx, "SELECT state,revision FROM task_instances WHERE task_id=$1", task.TaskID).Scan(&state, &revision); err != nil || state != "output_ready" || revision != task.Revision+1 {
		t.Fatalf("output without trusted verdict was promoted: %s revision=%d %v", state, revision, err)
	}
	resumeFixtureVerdict(t, s, artifact)
	if err := s.ApplyVerdicts(ctx, w.RunID, orchestrator); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT state,revision FROM task_instances WHERE task_id=$1", task.TaskID).Scan(&state, &revision); err != nil || state != "succeeded" || revision != task.Revision+3 {
		t.Fatalf("trusted verdict did not finish task through verifying: %s revision=%d %v", state, revision, err)
	}
	var verifyingEvents, successEvents int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='task_verifying' AND entity_revision=$2),
	 count(*) FILTER(WHERE kind='task_succeeded' AND entity_revision=$3)
	 FROM workflow_events WHERE run_id=$1 AND entity_id=$4 AND entity_type='task'`,
		w.RunID, task.Revision+2, task.Revision+3, task.TaskID).Scan(&verifyingEvents, &successEvents); err != nil || verifyingEvents != 1 || successEvents != 1 {
		t.Fatalf("task success lacks verifying/succeeded audit: verifying=%d succeeded=%d %v", verifyingEvents, successEvents, err)
	}
	running, err := s.Workflow(ctx, w.RunID)
	if err != nil || running.State != "running" || running.Revision != w.Revision || running.FinishedAt != nil {
		t.Fatalf("task verdict skipped workflow checkpoint: %+v %v", running, err)
	}
	checkpoint, err := s.FinalizeWorkflow(ctx, w.RunID, orchestrator)
	if err != nil || checkpoint.State != "verifying" || checkpoint.FinishedAt != nil || checkpoint.Revision != w.Revision+1 {
		t.Fatalf("first persistent workflow checkpoint: %+v %v", checkpoint, err)
	}
	persisted, err := s.Workflow(ctx, w.RunID)
	if err != nil || !reflect.DeepEqual(persisted, checkpoint) {
		t.Fatalf("verifying checkpoint not independently committed: %+v %v", persisted, err)
	}
	final, err := s.FinalizeWorkflow(ctx, w.RunID, orchestrator)
	if err != nil || final.State != "succeeded" || final.FinishedAt == nil || final.Revision != checkpoint.Revision+1 {
		t.Fatalf("second persistent workflow checkpoint: %+v %v", final, err)
	}
	if _, err = s.FinalizeWorkflow(ctx, w.RunID, orchestrator); !errors.Is(err, store.ErrBadState) {
		t.Fatalf("terminal workflow finalized twice: %v", err)
	}
	if _, err = s.CancelWorkflow(ctx, w.RunID, final.Revision); !errors.Is(err, store.ErrBadState) {
		t.Fatalf("completed workflow was cancelled: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE workflow_runs SET finished_at=finished_at+interval '1 second' WHERE run_id=$1", w.RunID); err == nil {
		t.Fatal("database allowed terminal finished_at rewrite")
	}
	unchanged, err := s.Workflow(ctx, w.RunID)
	if err != nil || !reflect.DeepEqual(unchanged, final) {
		t.Fatalf("terminal timestamp/history changed: %+v %v", unchanged, err)
	}
	assertResumeWorkflowEvents(t, s, unchanged, "workflow_queued", "workflow_planning", "plan_committed", "workflow_verifying", "workflow_succeeded")
	budget := resumeBudget(t, s, w.RunID)
	if budget.Used != 1 || budget.Running != 0 || budget.Attempts != 1 || budget.Retries != 0 || budget.ReservedCalls != 0 {
		t.Fatalf("successful checkpoints changed delegation accounting: %+v", budget)
	}
	t.Logf("real claim/start/CommitOutput; synthetic verdict fixture; committed workflow revisions %d (running) -> %d (verifying) -> %d (succeeded), finished_at frozen", running.Revision, checkpoint.Revision, final.Revision)
}
