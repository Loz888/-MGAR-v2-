package chaos

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func chaosPlan() planner.Proposal {
	return planner.Proposal{SchemaVersion: "plan.v1", Rationale: "synthetic chaos plan", Nodes: []planner.Node{{Key: "A", Role: "research", Goal: "inspect synthetic input", RequiredInputs: []string{}, OutputKind: "finding"}, {Key: "B", Role: "data", Goal: "inspect synthetic data", RequiredInputs: []string{}, OutputKind: "sql_evidence"}, {Key: "C", Role: "aggregate", Goal: "merge evidence", RequiredInputs: []string{}, OutputKind: "report"}}, Edges: []planner.Edge{{From: "A", To: "C"}, {From: "B", To: "C"}}}
}

type barrier struct {
	Point    string                  `json:"point"`
	PID      int                     `json:"backend_pid"`
	Claim    store.TaskClaim         `json:"claim"`
	Owner    store.OrchestratorClaim `json:"orchestrator"`
	Snapshot string                  `json:"snapshot_id"`
	Hash     string                  `json:"snapshot_hash"`
	CallID   string                  `json:"call_id,omitempty"`
}

func childStore(t *testing.T) *store.Store {
	t.Helper()
	schema := os.Getenv("MGAR_CHAOS_SCHEMA")
	if !strings.HasPrefix(schema, "mgar_test_") {
		t.Fatal("owned schema required")
	}
	cfg, e := pgxpool.ParseConfig(testdb.DSN(t))
	if e != nil {
		t.Fatal("synthetic configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	return &store.Store{Pool: pool}
}
func TestChaosProcess(t *testing.T) {
	mode := os.Getenv("MGAR_CHAOS_MODE")
	if mode == "" {
		return
	}
	s := childStore(t)
	ctx := context.Background()
	run := os.Getenv("MGAR_CHAOS_RUN")
	in := bufio.NewReader(os.Stdin)
	send := func(b barrier) {
		t.Helper()
		if e := json.NewEncoder(os.Stdout).Encode(b); e != nil {
			t.Fatal(e)
		}
	}
	pause := func(b barrier) {
		t.Helper()
		send(b)
		if _, e := in.ReadString('\n'); e != nil {
			t.Fatal(e)
		}
	}
	var pid int
	if e := s.Pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); e != nil {
		t.Fatal(e)
	}
	pause(barrier{Point: "ready", PID: pid})
	switch mode {
	case "F-A", "F-B":
		c, e := s.TakeOrchestrator(ctx, run, "crashed-orchestrator", time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BeginPlanning(ctx, run, c); e != nil {
			t.Fatal(e)
		}
		if mode == "F-A" {
			pause(barrier{Point: mode, Owner: c})
		}
		if _, e = s.ProposeAndCommit(ctx, run, c, chaosPlan()); e != nil {
			t.Fatal(e)
		}
		pause(barrier{Point: mode, Owner: c})
	case "F-C", "F-H":
		ttl := time.Minute
		if mode == "F-C" {
			ttl = time.Second
		}
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "crashed-worker", Role: "research"}, ttl)
		if e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_CHAOS_CAS"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas)
		if e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		pause(barrier{Point: mode, Claim: c, Snapshot: snap.ID, Hash: snap.Hash()})
		if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", []byte("old worker output")); e != nil {
			t.Fatal(e)
		}
	case "F-E":
		var c store.OrchestratorClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_CHAOS_OWNER")), &c); e != nil {
			t.Fatal(e)
		}
		if _, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, os.Getenv("MGAR_CHAOS_EVENT")); e != nil {
			t.Fatal(e)
		}
		send(barrier{Point: "unexpected_commit"})
	case "stale":
		var c store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_CHAOS_CLAIM")), &c); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_CHAOS_CAS"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", []byte("stale resumed output")); !errors.Is(e, store.ErrFenced) {
			t.Fatal("stale process write", e)
		}
		send(barrier{Point: "fenced", Claim: c})
	default:
		if !modelChild(t, s, run, mode, send, pause) {
			t.Fatal("unknown owned test operation")
		}
	}
}

type chaosChild struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	pid  int
	done bool
}

func startChaos(t *testing.T, s *store.Store, run, mode string) *chaosChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChaosProcess$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "MGAR_CHAOS_MODE="+mode, "MGAR_CHAOS_RUN="+run, "MGAR_CHAOS_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
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
	child := &chaosChild{cmd: cmd, in: in, out: bufio.NewReader(out)}
	t.Cleanup(func() {
		if !child.done {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = in.Close()
	})
	ready := child.read(t)
	if ready.Point != "ready" || ready.PID < 1 {
		t.Fatal(ready)
	}
	child.pid = ready.PID
	if _, e = io.WriteString(in, "START\n"); e != nil {
		t.Fatal(e)
	}
	return child
}
func (c *chaosChild) read(t *testing.T) barrier {
	t.Helper()
	var b barrier
	line, e := c.out.ReadBytes('\n')
	if e != nil {
		t.Fatal("child barrier", e)
	}
	if e = json.Unmarshal(line, &b); e != nil {
		t.Fatalf("unexpected child output %q: %v", line, e)
	}
	return b
}
func (c *chaosChild) kill(t *testing.T) {
	t.Helper()
	pgid, e := syscall.Getpgid(c.cmd.Process.Pid)
	if e != nil || pgid != c.cmd.Process.Pid || pgid == syscall.Getpgrp() {
		t.Fatal("refuse unsafe kill", pgid, e)
	}
	if e = syscall.Kill(-pgid, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	e = c.cmd.Wait()
	c.done = true
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("not SIGKILL", e)
	}
	t.Logf("SIGKILL owned_pid=%d process_group=%d postgres_backend=%d", c.cmd.Process.Pid, pgid, c.pid)
}
func (c *chaosChild) finish(t *testing.T) {
	t.Helper()
	if e := c.cmd.Wait(); e != nil {
		t.Fatal(e)
	}
	c.done = true
}
func chaosFixture(t *testing.T, planned bool) (*store.Store, string, store.OrchestratorClaim) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	w, e := s.CreateRun(ctx, "", "synthetic kill matrix", 24)
	if e != nil {
		t.Fatal(e)
	}
	c := store.OrchestratorClaim{}
	if planned {
		c, e = s.TakeOrchestrator(ctx, w.RunID, "fixture-owner", time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BeginPlanning(ctx, w.RunID, c); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ProposeAndCommit(ctx, w.RunID, c, chaosPlan()); e != nil {
			t.Fatal(e)
		}
	}
	return s, w.RunID, c
}
func awaitDB(t *testing.T, s *store.Store, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var ok bool
		if e := s.Pool.QueryRow(ctx, query, args...).Scan(&ok); e != nil {
			t.Fatal(e)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("database condition not observed", ctx.Err())
		case <-ticker.C:
		}
	}
}
func traceChaos(t *testing.T, s *store.Store, run, point string, oldEpoch, newEpoch int64) {
	t.Helper()
	ctx := context.Background()
	w, e := s.Workflow(ctx, run)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := s.Pool.Query(ctx, "SELECT entity_type,entity_revision,kind,trace_id FROM workflow_events WHERE run_id=$1 ORDER BY entity_type,entity_id,entity_revision,kind", run)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	events := []any{}
	for rows.Next() {
		var entity, kind, trace string
		var revision int64
		if e = rows.Scan(&entity, &revision, &kind, &trace); e != nil {
			t.Fatal(e)
		}
		events = append(events, map[string]any{"entity": entity, "revision": revision, "kind": kind, "trace_id": trace})
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	report := map[string]any{"point": point, "run_id": run, "old_epoch": oldEpoch, "new_epoch": newEpoch, "state": w.State, "active_plan_version": w.ActivePlanVersion, "events": events}
	body, e := json.Marshal(report)
	if e != nil {
		t.Fatal(e)
	}
	t.Log(string(body))
}
func TestKillMatrixCore(t *testing.T) {
	for _, point := range []string{"F-A", "F-B", "F-C", "F-E", "F-H"} {
		t.Run(point, func(t *testing.T) {
			s, run, c := chaosFixture(t, point != "F-A" && point != "F-B")
			ctx := context.Background()
			other, e := s.CreateRun(ctx, "", "unrelated sentinel run", 1)
			if e != nil {
				t.Fatal(e)
			}
			sentinel, e := json.Marshal(other)
			if e != nil {
				t.Fatal(e)
			}
			root := filepath.Join(t.TempDir(), "cas")
			t.Setenv("MGAR_CHAOS_CAS", root)
			oldEpoch, newEpoch := int64(0), int64(0)
			switch point {
			case "F-A", "F-B":
				child := startChaos(t, s, run, point)
				b := child.read(t)
				if b.Point != point {
					t.Fatal(b)
				}
				oldEpoch = b.Owner.Epoch
				child.kill(t)
				w, e := s.Workflow(ctx, run)
				if e != nil {
					t.Fatal(e)
				}
				want := 0
				if point == "F-B" {
					want = 1
				}
				if w.ActivePlanVersion != want {
					t.Fatal(w)
				}
				awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM orchestrator_leases WHERE run_id=$1", run)
				if _, e = (scheduler.Scheduler{Store: s}).RecoverRun(ctx, run, "replacement-orchestrator"); e != nil {
					t.Fatal(e)
				}
				if e = s.Pool.QueryRow(ctx, "SELECT epoch FROM orchestrator_leases WHERE run_id=$1", run).Scan(&newEpoch); e != nil || newEpoch <= oldEpoch {
					t.Fatal(newEpoch, e)
				}
				if point == "F-B" {
					var receipts, ready int
					if e = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM inbox_receipts i JOIN workflow_events w USING(event_id) WHERE w.run_id=$1 AND w.kind='plan_committed' AND i.processing_status='processed'),(SELECT count(*) FROM task_instances WHERE run_id=$1 AND state='ready')`, run).Scan(&receipts, &ready); e != nil || receipts != 1 || ready != 2 {
						t.Fatal("lost handoff", receipts, ready, e)
					}
				}
			case "F-C":
				if _, e = s.RefreshReady(ctx, run, c); e != nil {
					t.Fatal(e)
				}
				child := startChaos(t, s, run, point)
				b := child.read(t)
				oldEpoch = b.Claim.Epoch
				child.kill(t)
				awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", b.Claim.TaskID)
				replacement, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "replacement-worker", Role: "research"}, time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				newEpoch = replacement.Epoch
				if newEpoch <= oldEpoch || replacement.TaskID != b.Claim.TaskID {
					t.Fatal(replacement, b)
				}
				cas, e := artifact.New(root)
				if e != nil {
					t.Fatal(e)
				}
				defer cas.Close()
				snap, e := s.BuildSnapshot(ctx, replacement, "mock.v1", "research.v1", cas)
				if e != nil || snap.ID != b.Snapshot || snap.Hash() != b.Hash {
					t.Fatal("context not reused", snap, e)
				}
				var used, running int
				if e = s.Pool.QueryRow(ctx, "SELECT used_delegations,running_count FROM run_budgets WHERE run_id=$1", run).Scan(&used, &running); e != nil || used != 1 || running != 1 {
					t.Fatal("takeover double charged", used, running, e)
				}
			case "F-E":
				var id string
				if e = s.Pool.QueryRow(ctx, "SELECT event_id FROM workflow_events WHERE run_id=$1 AND kind='plan_committed'", run).Scan(&id); e != nil {
					t.Fatal(e)
				}
				if e = s.MarkPublished(ctx, id); e != nil {
					t.Fatal(e)
				}
				encoded, e := json.Marshal(c)
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("MGAR_CHAOS_OWNER", string(encoded))
				t.Setenv("MGAR_CHAOS_EVENT", id)
				tx, e := s.Pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback(context.Background())
				if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(923422)"); e != nil {
					t.Fatal(e)
				}
				if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION inbox_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(923422); RETURN NEW; END $$; CREATE TRIGGER inbox_barrier BEFORE UPDATE ON inbox_receipts FOR EACH ROW EXECUTE FUNCTION inbox_barrier()`); e != nil {
					t.Fatal(e)
				}
				child := startChaos(t, s, run, point)
				awaitDB(t, s, "SELECT cardinality(pg_blocking_pids($1))>0", child.pid)
				child.kill(t)
				if e = tx.Rollback(ctx); e != nil {
					t.Fatal(e)
				}
				if _, e = s.Pool.Exec(ctx, "DROP TRIGGER inbox_barrier ON inbox_receipts"); e != nil {
					t.Fatal(e)
				}
				receipt, e := s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id)
				if e != nil || receipt.Duplicate || !receipt.Applied {
					t.Fatal(receipt, e)
				}
				receipt, e = s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, id)
				if e != nil || !receipt.Duplicate {
					t.Fatal(receipt, e)
				}
				oldEpoch = c.Epoch
				newEpoch = c.Epoch
				var n int
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='task_ready'", run).Scan(&n); e != nil || n != 2 {
					t.Fatal(n, e)
				}
			case "F-H":
				if _, e = s.RefreshReady(ctx, run, c); e != nil {
					t.Fatal(e)
				}
				child := startChaos(t, s, run, point)
				b := child.read(t)
				oldEpoch = b.Claim.Epoch
				if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET state='failed',revision=revision+1 WHERE run_id=$1 AND node_key='B'", run); e != nil {
					t.Fatal(e)
				}
				p := chaosPlan()
				p.BaseVersion = 1
				p.Nodes[0].Goal = "new plan input"
				raw, e := json.Marshal(planner.ReplanSuggestion{SchemaVersion: "replan.v1", FailureKind: "permanent", FailedNode: "B", Proposal: p})
				if e != nil {
					t.Fatal(e)
				}
				configs := map[string]inputcontext.ExecutionConfig{}
				for _, n := range p.Nodes {
					configs[n.Key] = inputcontext.ExecutionConfig{ModelVersion: "mock.v1", PolicyVersion: n.Role + ".v1", OutputToolVersion: n.Role + ".v1"}
				}
				cas, e := artifact.New(root)
				if e != nil {
					t.Fatal(e)
				}
				defer cas.Close()
				if _, e = s.Replan(ctx, run, c, raw, configs, planner.ReplanPolicy{MaxReplans: 3, MaxDepth: 6, MaxCostMicros: 24, CostPerTaskMicros: 1}, cas); e != nil {
					t.Fatal(e)
				}
				child.kill(t)
				encoded, e := json.Marshal(b.Claim)
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("MGAR_CHAOS_CLAIM", string(encoded))
				stale := startChaos(t, s, run, "stale")
				if result := stale.read(t); result.Point != "fenced" {
					t.Fatal(result)
				}
				stale.finish(t)
				if _, e = s.RefreshReady(ctx, run, c); e != nil {
					t.Fatal(e)
				}
				replacement, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "new-plan-worker", Role: "research"}, time.Minute)
				if e != nil || replacement.TaskID == b.Claim.TaskID || replacement.PlanVersion != 2 {
					t.Fatal(replacement, e)
				}
				newEpoch = replacement.Epoch
				var n int
				if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE producer_task=$1", b.Claim.TaskID).Scan(&n); e != nil || n != 0 {
					t.Fatal("stale process published", n, e)
				}
			}
			after, e := s.Workflow(ctx, other.RunID)
			if e != nil {
				t.Fatal(e)
			}
			data, e := json.Marshal(after)
			if e != nil || string(data) != string(sentinel) {
				t.Fatal("unrelated workflow changed", e)
			}
			if e = s.Pool.Ping(ctx); e != nil {
				t.Fatal("PostgreSQL harmed", e)
			}
			traceChaos(t, s, run, point, oldEpoch, newEpoch)
		})
	}
}
