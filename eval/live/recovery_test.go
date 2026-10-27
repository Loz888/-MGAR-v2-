package live

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/demo"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func recoveryFixture(t *testing.T) (string, *store.Store, Plan, recoveryProof) {
	t.Helper()
	source, _ := filepath.Abs("../..")
	p, e := LoadPlan(source)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := loadRecoveryProof(source)
	if e != nil {
		t.Fatal(e)
	}
	s := testdb.New(t)
	proof.Schema = s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	proof.Namespaces = []string{proof.Schema}
	proof.Pattern = "^" + proof.Schema + "_child_[0-9a-f]{16}$"
	_, _, e = s.CreateRunClaimed(context.Background(), demo.LiveRunID("allocation", "all"), proof.Previous.Goal, 1, "live-allocation-gate", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Pool.Exec(context.Background(), "UPDATE orchestrator_leases SET lease_until=$2 WHERE run_id=$1", demo.LiveRunID("allocation", "all"), proof.Previous.LeaseUntil)
	if e != nil {
		t.Fatal(e)
	}
	return t.TempDir(), s, p, proof
}
func TestZeroSendRecoveryProofAndDenials(t *testing.T) {
	for _, mode := range []string{"valid", "active-lease", "epoch-two", "wrong-owner", "wrong-expiry", "wrong-goal", "wrong-revision", "wrong-state", "wrong-plan-version", "changed-plan", "budget", "hidden-run", "unknown-model", "missing-table", "missing-namespace", "unreadable-extra-namespace", "checksum", "journal", "cas-history", "symlink", "cancelled-context"} {
		t.Run(mode, func(t *testing.T) {
			root, s, p, proof := recoveryFixture(t)
			ctx := context.Background()
			id := demo.LiveRunID("allocation", "all")
			var e error
			switch mode {
			case "active-lease":
				_, e = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()+interval '1 minute'")
			case "epoch-two":
				_, e = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET epoch=2,revision=1")
			case "wrong-owner":
				_, e = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET owner_id='other'")
			case "wrong-expiry":
				_, e = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=lease_until-interval '1 second'")
			case "wrong-goal":
				_, e = s.Pool.Exec(ctx, "UPDATE workflow_runs SET goal='different'")
			case "wrong-revision":
				_, e = s.Pool.Exec(ctx, "UPDATE workflow_runs SET revision=1")
			case "wrong-state":
				_, e = s.Pool.Exec(ctx, "UPDATE workflow_runs SET state='planning'")
			case "wrong-plan-version":
				_, e = s.Pool.Exec(ctx, "UPDATE workflow_runs SET active_plan_version=1")
			case "unreadable-extra-namespace":
				name := proof.Schema + "_child_0000000000000001"
				ident := pgx.Identifier{name}.Sanitize()
				_, e = s.Pool.Exec(ctx, "CREATE SCHEMA "+ident)
				t.Cleanup(func() { _, _ = s.Pool.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE") })
			case "changed-plan":
				p.Assignments[0].CapMicros++
			case "budget":
				_, e = s.Pool.Exec(ctx, "UPDATE run_budgets SET max_cost_usd=1")
			case "hidden-run", "unknown-model":
				_, e = s.Pool.Exec(ctx, "INSERT INTO workflow_runs(run_id,goal) VALUES($1,'hidden execution')", p.Assignments[0].RunID)
				if e == nil && mode == "unknown-model" {
					_, e = s.Pool.Exec(ctx, `WITH task AS (INSERT INTO task_instances(run_id,node_key,role) VALUES($1,'research','research') RETURNING task_id) INSERT INTO model_calls(logical_call_id,run_id,task_id,provider,model,status,estimate_bound,reservation) SELECT gen_random_uuid(),$1,task_id,'deepseek','flash-1017','unknown',0.001,'{"tokens":10,"cost_micros":1000}' FROM task`, p.Assignments[0].RunID)
				}
			case "missing-table":
				_, e = s.Pool.Exec(ctx, "DROP TABLE model_calls")
			case "missing-namespace":
				proof.Namespaces = append(proof.Namespaces, proof.Schema+"_missing")
			case "checksum":
				_, e = s.Pool.Exec(ctx, "UPDATE schema_migrations SET checksum='changed' WHERE version='006_resource_admission'")
			case "journal":
				e = os.MkdirAll(statePath(root), 0700)
			case "cas-history":
				e = os.MkdirAll(filepath.Join(root, ".local/live-cas/history"), 0700)
			case "symlink":
				e = os.MkdirAll(filepath.Join(root, "evidence/eval"), 0700)
				if e == nil {
					e = os.Symlink(t.TempDir(), filepath.Join(root, "evidence/eval/live"))
				}
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if e != nil {
				t.Fatal(e)
			}
			c, e := claimZeroSend(ctx, root, s, p, proof)
			var epoch int64
			if scan := s.Pool.QueryRow(context.Background(), "SELECT epoch FROM orchestrator_leases WHERE run_id=$1", id).Scan(&epoch); scan != nil {
				t.Fatal(scan)
			}
			if mode == "valid" {
				if e != nil || c.Epoch != 2 || epoch != 2 {
					t.Fatal(c, epoch, e)
				}
				if _, e = claimZeroSend(ctx, root, s, p, proof); e == nil {
					t.Fatal("second recovery accepted")
				}
				// Expiring epoch 2 does not authorize a third epoch/recovery.
				if _, e = s.Pool.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", id); e != nil {
					t.Fatal(e)
				}
				if _, e = claimZeroSend(ctx, root, s, p, proof); e == nil {
					t.Fatal("expired second recovery accepted")
				}
				w, e := s.Workflow(ctx, id)
				if e != nil || w.State != "queued" || w.Revision != 0 {
					t.Fatal(w, e)
				}
			} else if e == nil || (epoch != 1 && mode != "epoch-two") {
				t.Fatalf("%s changed or accepted allocation: epoch=%d err=%v", mode, epoch, e)
			}
		})
	}
}
func TestZeroSendFrozenProofMissingAndChanged(t *testing.T) {
	source, _ := filepath.Abs("../..")
	if _, e := loadRecoveryProof(source); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if _, e := loadRecoveryProof(root); e == nil {
		t.Fatal("missing proof accepted")
	}
	for name := range recoveryEvidence {
		raw, e := os.ReadFile(filepath.Join(source, "evidence/batches/P7-06E/original", name))
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(root, "evidence/batches/P7-06E/original", name)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(root, "evidence/batches/P7-06E/original/actual-live-run.log")
	if e := os.WriteFile(path, []byte(`{"stdout":"unreviewed failure"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := loadRecoveryProof(root); e == nil {
		t.Fatal("changed proof accepted")
	}
}
func TestZeroSendRecoveryChild(t *testing.T) {
	schema := os.Getenv("MGAR_RECOVERY_TEST_SCHEMA")
	if schema == "" {
		return
	}
	source, _ := filepath.Abs("../..")
	p, e := LoadPlan(source)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := loadRecoveryProof(source)
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(testdb.DSN(t))
	if e != nil {
		t.Fatal("test DB config")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal("test DB connection")
	}
	defer pool.Close()
	proof.Schema = schema
	proof.Namespaces = []string{schema}
	proof.Pattern = "^" + schema + "_child_[0-9a-f]{16}$"
	c, e := claimZeroSend(context.Background(), os.Getenv("MGAR_RECOVERY_TEST_ROOT"), &store.Store{Pool: pool}, p, proof)
	if e != nil {
		os.Exit(23)
	}
	if c.Epoch != 2 {
		t.Fatal("unexpected recovered epoch")
	}
	fmt.Println("DURABLE_RECOVERY_EPOCH_2")
	if os.Getenv("MGAR_RECOVERY_TEST_KILL") == "1" {
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
	}
}
func TestZeroSendRecoveryTwoProcessesAndSIGKILL(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprint(kill), func(t *testing.T) {
			root, s, p, proof := recoveryFixture(t)
			child := func() *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestZeroSendRecoveryChild$")
				cmd.Env = append(os.Environ(), "MGAR_RECOVERY_TEST_SCHEMA="+proof.Schema, "MGAR_RECOVERY_TEST_ROOT="+root)
				return cmd
			}
			if !kill {
				a, b := child(), child()
				if e := a.Start(); e != nil {
					t.Fatal(e)
				}
				if e := b.Start(); e != nil {
					t.Fatal(e)
				}
				wins, denied := 0, 0
				for _, c := range []*exec.Cmd{a, b} {
					e := c.Wait()
					if e == nil {
						wins++
					} else if x, ok := e.(*exec.ExitError); ok && x.ExitCode() == 23 {
						denied++
					} else {
						t.Fatal(e)
					}
				}
				if wins != 1 || denied != 1 {
					t.Fatal(wins, denied)
				}
			} else {
				c := child()
				c.Env = append(c.Env, "MGAR_RECOVERY_TEST_KILL=1")
				out, e := c.StdoutPipe()
				if e != nil {
					t.Fatal(e)
				}
				in, e := c.StdinPipe()
				if e != nil {
					t.Fatal(e)
				}
				defer in.Close()
				if e = c.Start(); e != nil {
					t.Fatal(e)
				}
				var received string
				if _, e = fmt.Fscanln(out, &received); e != nil || !strings.Contains(received, "DURABLE_RECOVERY_EPOCH_2") {
					t.Fatal(received, e)
				}
				if e = c.Process.Kill(); e != nil {
					t.Fatal(e)
				}
				e = c.Wait()
				x, ok := e.(*exec.ExitError)
				if !ok || x.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatal("not SIGKILL", e)
				}
			}
			if _, e := s.Pool.Exec(context.Background(), "UPDATE orchestrator_leases SET lease_until=clock_timestamp()-interval '1 second'"); e != nil {
				t.Fatal(e)
			}
			if _, e := claimZeroSend(context.Background(), root, s, p, proof); e == nil {
				t.Fatal("crash/restart replayed recovery")
			}
			var epoch int
			if e := s.Pool.QueryRow(context.Background(), "SELECT epoch FROM orchestrator_leases").Scan(&epoch); e != nil || epoch != 2 {
				t.Fatal(epoch, e)
			}
			// No workflow task, budget reservation, or journal was minted by claim alone.
			var n int
			if e := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM model_calls").Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
			t.Logf("two-process/SIGKILL=%v: original marker retained at epoch 2, restart denied, model calls=0", kill)
		})
	}
}

func TestZeroSendInitializationAndOriginalAssignment(t *testing.T) {
	root, s, p, proof := recoveryFixture(t)
	ctx := context.Background()
	if e := finishZeroSend(ctx, root, s, p, proof); e != nil {
		t.Fatal(e)
	}
	id := demo.LiveRunID("allocation", "all")
	w, e := s.Workflow(ctx, id)
	if e != nil || w.RunID != id || w.State != "cancelled" || w.Revision != 1 {
		t.Fatal(w, e)
	}
	b, e := s.BudgetReport(ctx, id)
	if e != nil || !b.Conserved || b.Limits.CostMicros != 0 || b.Limits.ModelCalls != 0 || b.Limits.ToolCalls != 0 {
		t.Fatal(b, e)
	}
	if e := claimExperiment(ctx, s, p); e == nil {
		t.Fatal("normal restart minted allocation")
	}
	if e := finishZeroSend(ctx, root, s, p, proof); e == nil {
		t.Fatal("recovery repeated initialization")
	}
	path, e := begin(statePath(root), p)
	if e != nil {
		t.Fatal(e)
	}
	// Journal still has the exact original 96 UUIDs and shares.
	var saved Plan
	if e := readJSON(filepath.Join(path, "assignment.json"), &saved); e != nil || saved.Validate() != nil {
		t.Fatal("assignment altered", e)
	}
	if _, e := begin(path, p); e == nil {
		t.Fatal("second journal accepted")
	}
	t.Log("original marker cancelled once, epoch 2 retained, zero-spend budget and exact 96-slot journal; both normal and recovery replays refused")
}
