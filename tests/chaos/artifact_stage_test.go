package chaos

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

func faultSnapshot(t *testing.T, s *store.Store, run string) json.RawMessage {
	t.Helper()
	var raw []byte
	e := s.Pool.QueryRow(context.Background(), `SELECT json_build_object(
 'run_id',$1::uuid,
 'workflow',(SELECT json_build_object('state',state,'active_plan_version',active_plan_version,'revision',revision) FROM workflow_runs WHERE run_id=$1),
 'tasks',COALESCE((SELECT json_agg(json_build_object('task_id',task_id,'state',state,'epoch',epoch,'revision',revision) ORDER BY task_id) FROM task_instances WHERE run_id=$1),'[]'::json),
 'artifacts',(SELECT count(*) FROM artifacts WHERE run_id=$1),
 'verdicts',(SELECT count(*) FROM artifact_verdicts v JOIN artifacts a USING(artifact_id) WHERE a.run_id=$1),
 'model_calls',(SELECT count(*) FROM model_calls WHERE run_id=$1),
 'budget',(SELECT to_jsonb(b) FROM run_budgets b WHERE run_id=$1),
 'events',(SELECT count(*) FROM workflow_events WHERE run_id=$1))`, run).Scan(&raw)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

func recordFaultDeath(t *testing.T, point string, pid, pgid int, before, after json.RawMessage) {
	t.Helper()
	raw, e := json.Marshal(map[string]any{"point": point, "signal": "SIGKILL", "owned_pid": pid, "process_group": pgid, "before": before, "after_death": after})
	if e != nil {
		t.Fatal(e)
	}
	t.Log("CHAOS_KILL " + string(raw))
}

// The child uses the real CAS package's private after-fsync barrier. The parent
// owns a real executing task; neither its metadata nor output may be committed
// when the child dies. No test-only public production hook is introduced.
func TestArtifactStageCrash(t *testing.T) {
	s, run, owner := chaosFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, e := s.RefreshReady(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	claim, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "staging-worker", Role: "research"}, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "cas")
	cas, e := artifact.New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	snap, e := s.BuildSnapshot(ctx, claim, "mock.v1", "research.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	claim, e = s.StartTask(ctx, claim)
	if e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(t.TempDir(), "cas-child")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "../../internal/artifact")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build owned child: %v %s", e, out)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCASCrashProcessHelper$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "MGAR_CAS_CRASH_TEST_ROOT="+root)
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer in.Close()
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, e := bufio.NewReader(out).ReadString('\n')
	if e != nil || line != "STAGED\n" {
		t.Fatal("not at fsync barrier", line, e)
	}
	before := faultSnapshot(t, s, run)
	pgid, e := syscall.Getpgid(cmd.Process.Pid)
	if e != nil || pgid != cmd.Process.Pid || pgid == syscall.Getpgrp() {
		t.Fatal("unsafe process group")
	}
	if e = syscall.Kill(-pgid, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	e = cmd.Wait()
	reaped = true
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("not SIGKILL", e)
	}
	after := faultSnapshot(t, s, run)
	recordFaultDeath(t, "F-D", cmd.Process.Pid, pgid, before, after)
	if string(before) != string(after) {
		t.Fatal("uncommitted metadata changed")
	}
	objects, e := cas.Inventory()
	if e != nil || len(objects) != 0 {
		t.Fatal(objects, e)
	}
	staged, e := cas.StagingInventory()
	if e != nil || len(staged) != 1 {
		t.Fatal(staged, e)
	}
	if _, e = cas.Read(artifact.Digest([]byte("actual fsynced but unpublished synthetic bytes")), int64(len("actual fsynced but unpublished synthetic bytes"))); e == nil {
		t.Fatal("partial file visible")
	}
	awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", claim.TaskID)
	replacement, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "staging-replacement", Role: "research"}, time.Minute)
	if e != nil || replacement.TaskID != claim.TaskID || replacement.Epoch <= claim.Epoch {
		t.Fatal(replacement, e)
	}
	reused, e := s.BuildSnapshot(ctx, replacement, "mock.v1", "research.v1", cas)
	if e != nil || reused.ID != snap.ID {
		t.Fatal(reused, e)
	}
	replacement, e = s.StartTask(ctx, replacement)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishArtifact(ctx, replacement, cas, "finding", "text/plain", "research.v1", []byte("complete replacement result"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cas.Read(a.Hash, a.ByteLength); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishArtifact(ctx, claim, cas, "finding", "text/plain", "research.v1", []byte("stale result")); e == nil {
		t.Fatal("stale write")
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE producer_task=$1", claim.TaskID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	traceChaos(t, s, run, "F-D", claim.Epoch, replacement.Epoch)
	// Keep the orphan discoverable; cleanup is confined to testing.T.TempDir.
	staged, e = cas.StagingInventory()
	if e != nil || len(staged) != 1 {
		t.Fatal("orphan erased", staged, e)
	}
}
