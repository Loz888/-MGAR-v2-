package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lifecyclePlan(fan bool) planner.Proposal {
	p := planner.Proposal{
		SchemaVersion: "plan.v1", Rationale: "deterministic synthetic control-plane fixture",
		Nodes: []planner.Node{{Key: "A", Role: "research", Goal: "fixture A", RequiredInputs: []string{}, OutputKind: "finding"}},
		Edges: []planner.Edge{},
	}
	if fan {
		p.Nodes = append(p.Nodes,
			planner.Node{Key: "B", Role: "data", Goal: "fixture B", RequiredInputs: []string{}, OutputKind: "sql_evidence"},
			planner.Node{Key: "C", Role: "aggregate", Goal: "fixture fan-in", RequiredInputs: []string{}, OutputKind: "report"})
		p.Edges = []planner.Edge{{From: "A", To: "C"}, {From: "B", To: "C"}}
	}
	return p
}

func lifecycleFixture(t *testing.T, fan bool) (*store.Store, store.Workflow, store.OrchestratorClaim) {
	t.Helper()
	ctx := context.Background()
	s := testdb.New(t)
	w, err := s.CreateRun(ctx, "", "synthetic lifecycle fixture", 24)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.TakeOrchestrator(ctx, w.RunID, "fixture-orchestrator", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginPlanning(ctx, w.RunID, c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProposeAndCommit(ctx, w.RunID, c, lifecyclePlan(fan)); err != nil {
		t.Fatal(err)
	}
	w, err = s.Workflow(ctx, w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return s, w, c
}

// seedSucceeded is a database fixture for the P1 control-plane protocol. It does
// not execute an Agent, claim an actual Verifier result, or implement Artifact CAS.
func seedSucceeded(t *testing.T, s *store.Store, run, node string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var task, kind string
	var version int
	err = tx.QueryRow(ctx, `SELECT n.task_instance_id,n.requirements->>'required_output_kind',n.plan_version
	 FROM plan_nodes n JOIN workflow_runs w ON w.run_id=n.run_id AND w.active_plan_version=n.plan_version
	 WHERE n.run_id=$1 AND n.node_key=$2`, run, node).Scan(&task, &kind, &version)
	if err != nil {
		t.Fatal(err)
	}
	var artifact string
	err = tx.QueryRow(ctx, `INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
	 VALUES($1,$2,repeat('1',64),$3,'artifact.v1',$4,$5,0) RETURNING artifact_id`,
		run, kind, "artifact://fixture-"+task, task, version).Scan(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'e2e-fixture.v1',true)", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE task_instances SET state='succeeded',result_artifact_id=$2,revision=revision+1 WHERE task_id=$1", task, artifact); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func attachLifecycleStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("MGAR_E2E_DSN")
	if err := config.ValidateTestDSN(dsn); err != nil {
		t.Fatal("only an isolated synthetic database is allowed")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid synthetic connection configuration")
	}
	schema := os.Getenv("MGAR_E2E_SCHEMA")
	if !regexp.MustCompile(`^mgar_test_[0-9a-f]{16}$`).MatchString(schema) {
		t.Fatal("owned test schema required")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &store.Store{Pool: pool}
}

// TestLifecycleProcessHelper is invoked only by the parent test's own binary.
// All process barriers are pipes; no sleep is used to manufacture concurrency.
func TestLifecycleProcessHelper(t *testing.T) {
	kind := os.Getenv("MGAR_E2E_PROCESS")
	if kind == "" {
		return
	}
	s := attachLifecycleStore(t)
	run := os.Getenv("MGAR_E2E_RUN")
	ctx := context.Background()
	switch kind {
	case "crash-recovery":
		scan := scheduler.Scheduler{Store: s, AfterOrchestratorClaim: func(string) {
			fmt.Println("CLAIMED")
			select {}
		}}
		if _, err := scan.RecoverRun(ctx, run, "crashed-orchestrator"); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash barrier unexpectedly returned")
	case "recover-once":
		w, err := (scheduler.Scheduler{Store: s}).RecoverRun(ctx, run, "restarted-orchestrator")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("RESULT", w.State)
	case "cancel", "finish":
		var claim store.OrchestratorClaim
		if err := json.Unmarshal([]byte(os.Getenv("MGAR_E2E_CLAIM")), &claim); err != nil {
			t.Fatal("invalid claim fixture")
		}
		revision, err := strconv.ParseInt(os.Getenv("MGAR_E2E_REVISION"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("READY")
		if _, err = bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		var w store.Workflow
		if kind == "cancel" {
			w, err = s.CancelWorkflow(ctx, run, revision)
		} else {
			w, err = s.FinalizeWorkflow(ctx, run, claim)
		}
		if err != nil && !errors.Is(err, store.ErrBadState) && !errors.Is(err, store.ErrCASConflict) {
			t.Fatal(err)
		}
		if err == nil {
			fmt.Println("RESULT", w.State)
		} else {
			fmt.Println("RESULT rejected")
		}
	default:
		t.Fatal("unknown process fixture")
	}
}

func lifecycleCommand(ctx context.Context, t *testing.T, s *store.Store, run, kind string, extra ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifecycleProcessHelper$")
	cmd.Env = append(os.Environ(), "MGAR_E2E_PROCESS="+kind, "MGAR_E2E_RUN="+run,
		"MGAR_E2E_DSN="+testdb.DSN(t), "MGAR_E2E_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}

func TestLifecycleSIGKILLRecoveryRebuildsReady(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Fatal("SIGKILL recovery acceptance requires Linux/WSL2; not validated on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, w, _ := lifecycleFixture(t, true)
	seedSucceeded(t, s, w.RunID, "A")
	seedSucceeded(t, s, w.RunID, "B")
	if _, err := s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); err != nil {
		t.Fatal(err)
	}
	cmd := lifecycleCommand(ctx, t, s, w.RunID, "crash-recovery")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "CLAIMED\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("recovery barrier not reached: %q %v %s", line, err, stderr.String())
	}
	var old store.OrchestratorClaim
	err = s.Pool.QueryRow(ctx, "SELECT owner_id,epoch,revision FROM orchestrator_leases WHERE run_id=$1", w.RunID).Scan(&old.Owner, &old.Epoch, &old.Revision)
	if err != nil || old.Owner != "crashed-orchestrator" {
		t.Fatalf("claim was not persisted before crash: %v %v", old, err)
	}
	pid := cmd.Process.Pid
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("SIGKILL child unexpectedly exited successfully")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected actual SIGKILL, got %v", cmd.ProcessState)
	}
	// The killed process cannot renew. This is an explicit DB-clock fault hook,
	// avoiding sleeps and changing only this owned synthetic lease.
	if _, err = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); err != nil {
		t.Fatal(err)
	}
	restarted := lifecycleCommand(ctx, t, s, w.RunID, "recover-once")
	output, err := restarted.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "RESULT running") {
		t.Fatalf("fresh process failed to recover: %v %s", err, output)
	}
	var epoch int64
	if err = s.Pool.QueryRow(ctx, "SELECT epoch FROM orchestrator_leases WHERE run_id=$1", w.RunID).Scan(&epoch); err != nil || epoch <= old.Epoch {
		t.Fatalf("recovery did not advance epoch: old=%d new=%d %v", old.Epoch, epoch, err)
	}
	var ready, inputs, tasks, calls int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_instances WHERE run_id=$1 AND node_key='C' AND state='ready'", w.RunID).Scan(&ready); err != nil || ready != 1 {
		t.Fatalf("fanin not reconstructed exactly once: %d %v", ready, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_inputs i JOIN task_instances t USING(task_id) WHERE t.run_id=$1 AND t.node_key='C'", w.RunID).Scan(&inputs); err != nil || inputs != 2 {
		t.Fatalf("input snapshot not reconstructed: %d %v", inputs, err)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_instances WHERE run_id=$1", w.RunID).Scan(&tasks)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM model_calls WHERE run_id=$1", w.RunID).Scan(&calls)
	if tasks != 3 || calls != 0 {
		t.Fatalf("recovery duplicated tasks or called a model: tasks=%d calls=%d", tasks, calls)
	}
	p := lifecyclePlan(true)
	p.BaseVersion = 1
	if _, err = s.ProposeAndCommit(ctx, w.RunID, old, p); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("killed owner's stale authorization was accepted: %v", err)
	}
	t.Logf("SIGKILL pid=%d; persisted orchestrator epoch %d -> %d; ready C=%d; fixed inputs=%d; model calls=%d", pid, old.Epoch, epoch, ready, inputs, calls)
}

func TestLifecycleExpiredAttemptLimitFailsWithoutRetry(t *testing.T) {
	s, w, claim := lifecycleFixture(t, false)
	ctx := context.Background()
	if _, err := s.RefreshReady(ctx, w.RunID, claim); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "exhausted-fixture-worker", Role: "research"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE task_instances SET max_attempts=attempts,lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverExpired(ctx, w.RunID, claim); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts, retries, running int
	if err = s.Pool.QueryRow(ctx, "SELECT state,attempts,retry_count FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&state, &attempts, &retries); err != nil || state != "failed" || attempts != 1 || retries != 0 {
		t.Fatalf("attempt limit did not stop execution: state=%s attempts=%d retries=%d %v", state, attempts, retries, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT running_count FROM run_budgets WHERE run_id=$1", w.RunID).Scan(&running); err != nil || running != 0 {
		t.Fatalf("exhausted execution reservation not released: %d %v", running, err)
	}
	final, err := s.FinalizeWorkflow(ctx, w.RunID, claim)
	if err != nil || final.State != "failed" || final.FinishedAt == nil {
		t.Fatalf("exhausted workflow left pending: %+v %v", final, err)
	}
}

func TestLifecycleCancelFinishCrossProcessSingleTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, w, claim := lifecycleFixture(t, false)
	seedSucceeded(t, s, w.RunID, "A")
	w, err := s.FinalizeWorkflow(ctx, w.RunID, claim)
	if err != nil || w.State != "verifying" {
		t.Fatalf("verifying checkpoint: %s %v", w.State, err)
	}
	claimJSON, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	var readers []*bufio.Reader
	var writers []io.WriteCloser
	for _, kind := range []string{"cancel", "finish"} {
		cmd := lifecycleCommand(ctx, t, s, w.RunID, kind, "MGAR_E2E_CLAIM="+string(claimJSON), "MGAR_E2E_REVISION="+strconv.FormatInt(w.Revision, 10))
		cmd.Stderr = os.Stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
		readers = append(readers, bufio.NewReader(stdout))
		writers = append(writers, stdin)
	}
	for _, reader := range readers {
		if line, err := reader.ReadString('\n'); err != nil || line != "READY\n" {
			t.Fatalf("competition barrier not reached: %q %v", line, err)
		}
	}
	for _, writer := range writers {
		if _, err = io.WriteString(writer, "START\n"); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
	}
	for i, cmd := range cmds {
		output, err := io.ReadAll(readers[i])
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Wait(); err != nil {
			t.Fatalf("terminal contender failed: %v %s", err, output)
		}
	}
	final, err := s.Workflow(ctx, w.RunID)
	if err != nil || (final.State != "succeeded" && final.State != "cancelled") || final.FinishedAt == nil {
		t.Fatalf("invalid terminal state: %+v %v", final, err)
	}
	var terminals, revisions, calls int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND entity_type='workflow' AND kind IN ('workflow_succeeded','workflow_failed','workflow_cancelled')", w.RunID).Scan(&terminals); err != nil || terminals != 1 {
		t.Fatalf("terminal was not committed exactly once: %d %v", terminals, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(DISTINCT entity_revision) FROM workflow_events WHERE run_id=$1 AND entity_type='workflow'", w.RunID).Scan(&revisions); err != nil || int64(revisions) != final.Revision+1 {
		t.Fatalf("workflow state revisions lack audit events: revisions=%d final=%d %v", revisions, final.Revision, err)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM model_calls WHERE run_id=$1", w.RunID).Scan(&calls)
	if calls != 0 {
		t.Fatal("control-plane lifecycle caused a model call")
	}
	t.Logf("terminal=%s; terminal events=%d; audit revisions=%d; model calls=%d", final.State, terminals, revisions, calls)
}

func TestLifecycleTerminalIDResubmitDoesNotOverwrite(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	w, err := s.CreateRun(ctx, "", "original synthetic goal", 24)
	if err != nil {
		t.Fatal(err)
	}
	final, err := s.CancelWorkflow(ctx, w.RunID, w.Revision)
	if err != nil || final.State != "cancelled" || final.FinishedAt == nil {
		t.Fatalf("cancel queued run: %+v %v", final, err)
	}
	if _, err = s.CreateRun(ctx, w.RunID, "overwrite attempt", 24); err == nil {
		t.Fatal("duplicate terminal run ID was accepted")
	}
	unchanged, err := s.Workflow(ctx, w.RunID)
	if err != nil || unchanged.Goal != w.Goal || unchanged.State != final.State || unchanged.Revision != final.Revision || unchanged.FinishedAt == nil || !unchanged.FinishedAt.Equal(*final.FinishedAt) {
		t.Fatalf("terminal history overwritten: %+v %v", unchanged, err)
	}
	if _, err = s.ResumeWorkflow(ctx, w.RunID, store.OrchestratorClaim{}); err == nil {
		t.Fatal("terminal run was resumed")
	}
}

func TestLifecycleRollbackNoPartialResultOrCancellation(t *testing.T) {
	s, w, claim := lifecycleFixture(t, false)
	ctx := context.Background()
	if _, err := s.RefreshReady(ctx, w.RunID, claim); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "fixture-worker", Role: "research"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.StartTask(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	var artifact string
	err = s.Pool.QueryRow(ctx, `INSERT INTO artifacts(run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length)
	 VALUES($1,'finding',repeat('2',64),$2,'artifact.v1',$3,$4,0) RETURNING artifact_id`,
		w.RunID, "artifact://fixture-"+c.TaskID, c.TaskID, c.PlanVersion).Scan(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Workflow(ctx, w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore, outboxBefore int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", w.RunID).Scan(&eventsBefore)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events").Scan(&outboxBefore)
	if _, err = s.Pool.Exec(ctx, `CREATE FUNCTION reject_outbox_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic rollback fault'; END $$;
	 CREATE TRIGGER injected_outbox_failure BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_outbox_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitOutput(ctx, c, artifact); err == nil {
		t.Fatal("output commit ignored injected transaction failure")
	}
	if _, err = s.CancelWorkflow(ctx, w.RunID, before.Revision); err == nil {
		t.Fatal("cancellation ignored injected transaction failure")
	}
	var taskState string
	var resultNull bool
	var revision int64
	if err = s.Pool.QueryRow(ctx, "SELECT state,result_artifact_id IS NULL,revision FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&taskState, &resultNull, &revision); err != nil || taskState != "executing" || !resultNull || revision != c.Revision {
		t.Fatalf("partial task result/cancellation: state=%s null=%v revision=%d %v", taskState, resultNull, revision, err)
	}
	after, err := s.Workflow(ctx, w.RunID)
	if err != nil || after.State != before.State || after.Revision != before.Revision || after.FinishedAt != nil {
		t.Fatalf("partial workflow cancellation: %+v %v", after, err)
	}
	var running, used, eventsAfter, outboxAfter int
	if err = s.Pool.QueryRow(ctx, "SELECT running_count,used_delegations FROM run_budgets WHERE run_id=$1", w.RunID).Scan(&running, &used); err != nil || running != 1 || used != 1 {
		t.Fatalf("budget partially released: %d %d %v", running, used, err)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", w.RunID).Scan(&eventsAfter)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events").Scan(&outboxAfter)
	if eventsAfter != eventsBefore || outboxAfter != outboxBefore {
		t.Fatalf("partial event/outbox: events %d->%d outbox %d->%d", eventsBefore, eventsAfter, outboxBefore, outboxAfter)
	}
}
