package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

// These helpers use a separate Go process and PostgreSQL backend. The parent
// observes the actual lock wait before releasing the owned synthetic blocker.
func TestAuditProcessHelper(t *testing.T) {
	mode := os.Getenv("MGAR_AUDIT_PROCESS")
	if mode == "" {
		return
	}
	s := attachedStore(t)
	ctx := context.Background()
	var pid int
	if err := s.Pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY", pid)
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "ready_expired":
		_, err := s.RefreshReady(ctx, os.Getenv("MGAR_TEST_RUN"), store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0})
		if !errors.Is(err, store.ErrFenced) {
			t.Fatalf("ready list released after orchestrator lease expired: %v", err)
		}
		fmt.Println("RESULT fenced")
	case "ready":
		ready, err := s.RefreshReady(ctx, os.Getenv("MGAR_TEST_RUN"), store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("RESULT ready", len(ready))
	case "snapshot":
		var claim store.TaskClaim
		if err := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &claim); err != nil {
			t.Fatal(err)
		}
		_, err := s.ReadSnapshot(ctx, claim, os.Getenv("MGAR_TEST_SNAPSHOT"))
		if !errors.Is(err, store.ErrLeaseExpired) {
			t.Fatalf("snapshot released after task lease expired: %v", err)
		}
		fmt.Println("RESULT lease_expired")
	default:
		t.Fatal("unknown audit operation")
	}
}

func startAuditProcess(t *testing.T, ctx context.Context, s *store.Store, run, mode string) (int, <-chan lifecycleResult) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuditProcessHelper$")
	cmd.Env = append(os.Environ(), "MGAR_AUDIT_PROCESS="+mode, "MGAR_TEST_RUN="+run, "MGAR_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Always kill/reap an owned child if a negative assertion fails early.
	done := make(chan struct{})
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	var pid int
	if err == nil {
		_, err = fmt.Sscanf(line, "READY %d", &pid)
	}
	result := make(chan lifecycleResult, 1)
	go func() {
		defer close(done)
		out, readErr := io.ReadAll(reader)
		waitErr := cmd.Wait()
		if readErr != nil || waitErr != nil {
			result <- lifecycleResult{err: fmt.Errorf("child: %s: %w", out, errors.Join(readErr, waitErr))}
			return
		}
		if !strings.Contains(string(out), "RESULT") {
			result <- lifecycleResult{err: fmt.Errorf("missing result: %s", out)}
			return
		}
		result <- lifecycleResult{}
	}()
	if err != nil || pid <= 0 {
		t.Fatalf("child startup: %q %v", line, err)
	}
	if _, err = io.WriteString(stdin, "START\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	return pid, result
}

func TestAuditReadySerializesWithPendingRevocationAcrossProcess(t *testing.T) {
	s := testdb.New(t)
	run, owner := planFixture(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.ProposeAndCommit(ctx, run, owner, fanPlan()); err != nil {
		t.Fatal(err)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	fixtureOutput(t, s, run, "B", true, 1)
	verdict, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verdict.Rollback(context.Background())
	if _, err = verdict.Exec(ctx, `INSERT INTO artifact_verdicts(artifact_id,checker_version,passed)
 SELECT result_artifact_id,'audit-revocation.v1',false FROM task_instances WHERE run_id=$1 AND node_key='A'`, run); err != nil {
		t.Fatal(err)
	}
	pid, result := startAuditProcess(t, ctx, s, run, "ready")
	waitLifecycleLock(t, ctx, s, pid, result)
	if err = verdict.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if completed := completedLifecycle(t, ctx, result); completed.err != nil {
		t.Fatal(completed.err)
	}
	var state string
	var readyEvents, inputs int
	if err = s.Pool.QueryRow(ctx, `SELECT t.state,(SELECT count(*) FROM workflow_events e WHERE e.entity_id=t.task_id AND e.kind='task_ready'),
 (SELECT count(*) FROM task_inputs i WHERE i.task_id=t.task_id) FROM task_instances t WHERE run_id=$1 AND node_key='C'`, run).Scan(&state, &readyEvents, &inputs); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || readyEvents != 0 || inputs != 0 {
		t.Fatalf("revoked dependency released: state=%s ready_events=%d inputs=%d", state, readyEvents, inputs)
	}
}

func TestAuditContextReadFencesLeaseExpiredDuringDatabaseWait(t *testing.T) {
	s, cas, claim, _ := contextFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := s.BuildSnapshot(ctx, claim, "mock.v1", "action.v1", cas)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MGAR_TEST_CLAIM", string(encoded))
	t.Setenv("MGAR_TEST_SNAPSHOT", snapshot.ID)
	if _, err = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()+interval '2 seconds' WHERE task_id=$1", claim.TaskID); err != nil {
		t.Fatal(err)
	}
	before := snapshotAuthorization(t, s, claim.RunID, claim.TaskID)
	blocker, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, "LOCK TABLE context_snapshots IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	pid, result := startAuditProcess(t, ctx, s, claim.RunID, "snapshot")
	waitLifecycleLock(t, ctx, s, pid, result)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if err = s.Pool.QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", claim.TaskID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if completed := completedLifecycle(t, ctx, result); completed.err != nil {
		t.Fatal(completed.err)
	}
	if after := snapshotAuthorization(t, s, claim.RunID, claim.TaskID); after != before {
		t.Fatal("read mutated task/budget/audit")
	}
}

func TestAuditReadyReadFencesOrchestratorExpiredDuringWait(t *testing.T) {
	s := testdb.New(t)
	run, owner := planFixture(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.ProposeAndCommit(ctx, run, owner, simplePlan()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshReady(ctx, run, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()+interval '2 seconds' WHERE run_id=$1", run); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run); err != nil {
		t.Fatal(err)
	}
	pid, result := startAuditProcess(t, ctx, s, run, "ready_expired")
	waitLifecycleLock(t, ctx, s, pid, result)
	waitOrchestratorExpired(t, ctx, s, run)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if completed := completedLifecycle(t, ctx, result); completed.err != nil {
		t.Fatal(completed.err)
	}
}
