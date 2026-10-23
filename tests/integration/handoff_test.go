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
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func handoffEvent(t *testing.T, s *store.Store, run, kind string) string {
	t.Helper()
	var id string
	if e := s.Pool.QueryRow(context.Background(), "SELECT event_id FROM workflow_events WHERE run_id=$1 AND kind=$2 ORDER BY entity_revision DESC LIMIT 1", run, kind).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestOutboxPublishedDoesNotHideMissingInbox(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, fanPlan()); e != nil {
		t.Fatal(e)
	}
	id := handoffEvent(t, s, run, "plan_committed")
	if e := s.MarkPublished(ctx, id); e != nil {
		t.Fatal(e)
	}
	batch, e := s.PendingHandoffs(ctx, run, handoff.SchedulerConsumer, 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range batch {
		found = found || v.ID == id
	}
	if !found {
		t.Fatal("published notification lost without receipt")
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO inbox_receipts(consumer_name,event_id,processing_status) VALUES($1,$2,'received')", handoff.SchedulerConsumer, id); e != nil {
		t.Fatal(e)
	}
	r, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id)
	if e != nil || r.Duplicate || !r.Applied {
		t.Fatal(r, e)
	}
	r, e = s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id)
	if e != nil || !r.Duplicate {
		t.Fatal(r, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='task_ready'", run).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
func TestInboxOldPlanAndFencing(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	p := simplePlan()
	if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
		t.Fatal(e)
	}
	old := handoffEvent(t, s, run, "plan_committed")
	p.BaseVersion = 1
	if _, e := s.ProposeAndCommit(ctx, run, c, p); e != nil {
		t.Fatal(e)
	}
	r, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, old)
	if e != nil || r.Applied || r.Reason != "stale" {
		t.Fatal(r, e)
	}
	id := handoffEvent(t, s, run, "plan_committed")
	wrong := c
	wrong.Epoch++
	if _, e = s.ConsumeHandoff(ctx, run, wrong, handoff.SchedulerConsumer, id); !errors.Is(e, store.ErrFenced) {
		t.Fatal(e)
	}
	if _, e = s.ConsumeHandoff(ctx, run, c, "arbitrary-consumer", id); !errors.Is(e, store.ErrPolicy) {
		t.Fatal(e)
	}
	other, _ := planFixture(t, s)
	if _, e = s.ConsumeHandoff(ctx, other, c, handoff.SchedulerConsumer, id); !errors.Is(e, store.ErrPolicy) {
		t.Fatal("cross-run", e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_instances WHERE run_id=$1 AND state='ready'", run).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION fail_inbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic inbox rollback'; END $$; CREATE TRIGGER fail_inbox BEFORE UPDATE ON inbox_receipts FOR EACH ROW EXECUTE FUNCTION fail_inbox()`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id); e == nil {
		t.Fatal("expected rollback")
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_instances WHERE run_id=$1 AND state='ready'", run).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial business commit", n, e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM inbox_receipts WHERE event_id=$1", id).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial receipt commit", n, e)
	}
}

func TestOutboxInboxProcessHelper(t *testing.T) {
	mode := os.Getenv("MGAR_HANDOFF_PROCESS")
	if mode == "" {
		return
	}
	s := attachedStore(t)
	ctx := context.Background()
	run := os.Getenv("MGAR_TEST_RUN")
	c := store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}
	var pid int
	if e := s.Pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); e != nil {
		t.Fatal(e)
	}
	fmt.Println("READY", pid)
	reader := bufio.NewReader(os.Stdin)
	if _, e := reader.ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	if mode == "result" || mode == "committed" {
		var claim store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &claim); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		if _, e = s.PublishArtifact(ctx, claim, cas, "finding", "text/plain", "fixture.v1", []byte("durable handoff")); e != nil {
			t.Fatal(e)
		}
	} else if mode == "sent" {
		d := handoff.Dispatcher{Outbox: s, Consumer: handoff.SchedulerConsumer, Deliver: func(ctx context.Context, event handoff.Event) error {
			if _, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, event.ID); e != nil {
				return e
			}
			if event.ID == os.Getenv("MGAR_TEST_EVENT") {
				fmt.Println("COMMITTED")
				_, e := reader.ReadString('\n')
				return e
			}
			return nil
		}}
		if _, e := d.Dispatch(ctx, run, 128); e != nil {
			t.Fatal(e)
		}
		return
	} else {
		if _, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, os.Getenv("MGAR_TEST_EVENT")); e != nil {
			t.Fatal(e)
		}
	}
	fmt.Println("COMMITTED")
	if mode != "consume" {
		if _, e := reader.ReadString('\n'); e != nil {
			t.Fatal(e)
		}
	}
}

type handoffChild struct {
	cmd    *exec.Cmd
	reader *bufio.Reader
	stdin  io.WriteCloser
	pid    int
	done   bool
}

func startHandoff(t *testing.T, s *store.Store, run, mode string) *handoffChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutboxInboxProcessHelper$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "MGAR_HANDOFF_PROCESS="+mode, "MGAR_TEST_RUN="+run, "MGAR_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	child := &handoffChild{cmd: cmd, reader: bufio.NewReader(out), stdin: in}
	t.Cleanup(func() {
		if !child.done {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = in.Close()
	})
	line, e := child.reader.ReadString('\n')
	if e == nil {
		_, e = fmt.Sscanf(line, "READY %d", &child.pid)
	}
	if e != nil || child.pid <= 0 {
		t.Fatal(line, e)
	}
	return child
}
func (c *handoffChild) start(t *testing.T) {
	t.Helper()
	if _, e := io.WriteString(c.stdin, "START\n"); e != nil {
		t.Fatal(e)
	}
}
func (c *handoffChild) committed(t *testing.T) {
	t.Helper()
	line, e := c.reader.ReadString('\n')
	if e != nil || line != "COMMITTED\n" {
		t.Fatal(line, e)
	}
}
func (c *handoffChild) kill(t *testing.T) {
	t.Helper()
	pgid, e := syscall.Getpgid(c.cmd.Process.Pid)
	if e != nil || pgid != c.cmd.Process.Pid || pgid == syscall.Getpgrp() {
		t.Fatal("refuse unsafe test kill", pgid, e)
	}
	if e = syscall.Kill(-pgid, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	e = c.cmd.Wait()
	c.done = true
	var exit *exec.ExitError
	if !errors.As(e, &exit) || !exit.Sys().(syscall.WaitStatus).Signaled() || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("not real SIGKILL", e)
	}
	t.Logf("owned test process %d backend %d terminated by SIGKILL", pgid, c.pid)
}
func TestOutboxInboxConcurrentConsumers(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, fanPlan()); e != nil {
		t.Fatal(e)
	}
	fixtureOutput(t, s, run, "A", true, 1)
	fixtureOutput(t, s, run, "B", true, 1)
	t.Setenv("MGAR_TEST_EVENT", handoffEvent(t, s, run, "plan_committed"))
	children := []*handoffChild{}
	for i := 0; i < 4; i++ {
		children = append(children, startHandoff(t, s, run, "consume"))
	}
	for _, child := range children {
		child.start(t)
	}
	for _, child := range children {
		child.committed(t)
		if e := child.cmd.Wait(); e != nil {
			t.Fatal(e)
		}
		child.done = true
	}
	var n int
	if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='task_ready'", run).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM task_inputs WHERE run_id=$1", run).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
func TestOutboxInboxSIGKILLWindows(t *testing.T) {
	for _, mode := range []string{"result", "committed", "sent", "inbox"} {
		t.Run(mode, func(t *testing.T) {
			s := testdb.New(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			run, c := planFixture(t, s)
			if _, e := s.ProposeAndCommit(ctx, run, c, simplePlan()); e != nil {
				t.Fatal(e)
			}
			id := handoffEvent(t, s, run, "plan_committed")
			t.Setenv("MGAR_TEST_EVENT", id)
			var task string
			if mode == "result" || mode == "committed" {
				if _, e := s.RefreshReady(ctx, run, c); e != nil {
					t.Fatal(e)
				}
				claim, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker", Role: "research"}, time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				claim, e = s.StartTask(ctx, claim)
				if e != nil {
					t.Fatal(e)
				}
				task = claim.TaskID
				root := filepath.Join(t.TempDir(), "cas")
				cas, e := artifact.New(root)
				if e != nil {
					t.Fatal(e)
				}
				defer cas.Close()
				if _, e = s.BuildSnapshot(ctx, claim, "mock.v1", "research.v1", cas); e != nil {
					t.Fatal(e)
				}
				body, e := json.Marshal(claim)
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("MGAR_TEST_CLAIM", string(body))
				t.Setenv("MGAR_TEST_CAS_ROOT", root)
			}
			blocker, e := s.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback(context.Background())
			if mode == "result" || mode == "inbox" {
				if _, e = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(923411)"); e != nil {
					t.Fatal(e)
				}
				trigger := `CREATE FUNCTION handoff_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='output_ready' THEN PERFORM pg_advisory_xact_lock(923411); END IF; RETURN NEW; END $$; CREATE TRIGGER handoff_barrier BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION handoff_barrier()`
				if mode == "inbox" {
					trigger = `CREATE FUNCTION handoff_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(923411); RETURN NEW; END $$; CREATE TRIGGER handoff_barrier BEFORE UPDATE ON inbox_receipts FOR EACH ROW EXECUTE FUNCTION handoff_barrier()`
				}
				if _, e = s.Pool.Exec(ctx, trigger); e != nil {
					t.Fatal(e)
				}
			}
			child := startHandoff(t, s, run, mode)
			child.start(t)
			if mode == "result" || mode == "inbox" {
				for {
					var waiting bool
					if e = s.Pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", child.pid).Scan(&waiting); e != nil {
						t.Fatal(e)
					}
					if waiting {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(time.Millisecond):
					}
				}
			} else {
				child.committed(t)
			}
			child.kill(t)
			if e = blocker.Rollback(ctx); e != nil {
				t.Fatal(e)
			}
			if mode == "result" || mode == "inbox" {
				table := "outbox_events"
				if mode == "inbox" {
					table = "inbox_receipts"
				}
				if _, e = s.Pool.Exec(ctx, "DROP TRIGGER handoff_barrier ON "+table); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "result" {
				var n int
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE run_id=$1", run).Scan(&n); e != nil || n != 0 {
					t.Fatal("partial result", n, e)
				}
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE entity_id=$1 AND kind='output_ready'", task).Scan(&n); e != nil || n != 0 {
					t.Fatal("partial outbox", n, e)
				}
			}
			if mode == "committed" {
				id = handoffEvent(t, s, run, "output_ready")
				var result string
				if e = s.Pool.QueryRow(ctx, "SELECT result_artifact_id FROM task_instances WHERE task_id=$1", task).Scan(&result); e != nil || result == "" {
					t.Fatal(result, e)
				}
			}
			if mode == "sent" {
				var published bool
				if e = s.Pool.QueryRow(ctx, "SELECT published_at IS NOT NULL FROM outbox_events WHERE event_id=$1", id).Scan(&published); e != nil || published {
					t.Fatal("wrong dispatcher crash window", published, e)
				}
			}
			receipt, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id)
			if e != nil {
				t.Fatal(e)
			}
			if (mode == "sent") != receipt.Duplicate {
				t.Fatal("receipt recovery", mode, receipt)
			}
			var status string
			if e = s.Pool.QueryRow(ctx, "SELECT processing_status FROM inbox_receipts WHERE consumer_name=$1 AND event_id=$2", handoff.SchedulerConsumer, id).Scan(&status); e != nil || status != "processed" {
				t.Fatal(status, e)
			}
			if mode == "inbox" || mode == "sent" {
				var n int
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='task_ready'", run).Scan(&n); e != nil || n != 1 {
					t.Fatal("duplicate or lost release", n, e)
				}
			}
			if e = s.Pool.Ping(ctx); e != nil {
				t.Fatal("database harmed", e)
			}
		})
	}
}

func TestOutboxDispatcherRetriesFailedDelivery(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, simplePlan()); e != nil {
		t.Fatal(e)
	}
	d := handoff.Dispatcher{Outbox: s, Consumer: handoff.SchedulerConsumer, Deliver: func(context.Context, handoff.Event) error { return errors.New("local transport unavailable") }}
	if _, e := d.Dispatch(ctx, run, 100); e == nil {
		t.Fatal("missing transport failure")
	}
	var n int
	if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	d.Deliver = func(ctx context.Context, event handoff.Event) error {
		_, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, event.ID)
		return e
	}
	if n, e := d.Dispatch(ctx, run, 100); e != nil || n == 0 {
		t.Fatal(n, e)
	}
	oldReady := handoffEvent(t, s, run, "task_ready")
	if _, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "new-owner", Role: "research"}, time.Minute); e != nil {
		t.Fatal(e)
	}
	receipt, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, oldReady)
	if e != nil || receipt.Applied || receipt.Reason != "stale" {
		t.Fatal("old entity revision", receipt, e)
	}
	// A lost acknowledgement can replay an older event without regressing state.
	_, e = s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, handoffEvent(t, s, run, "plan_committed"))
	if e != nil {
		t.Fatal(e)
	}
}
