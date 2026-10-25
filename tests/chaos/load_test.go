package chaos

// Bounded control-plane load: trusted synthetic Go Worker processes share one
// PostgreSQL namespace. Real model reasoning and Python Agent latency are not
// measured by this harness; those are separate P7-03/P7-06 experiments.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

type loadObservation struct {
	Run      string            `json:"run_id"`
	Task     string            `json:"task_id"`
	PID      int               `json:"pid"`
	Latency  float64           `json:"latency_ms"`
	Artifact artifact.Metadata `json:"artifact"`
	Snapshot string            `json:"snapshot_id"`
}

func TestLoadWorker(t *testing.T) {
	if os.Getenv("MGAR_LOAD_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := childStore(t)
	cas, e := artifact.New(os.Getenv("MGAR_CHAOS_CAS"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println("READY")
	if _, e = bufio.NewReader(os.Stdin).ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	run := os.Getenv("MGAR_CHAOS_RUN")
	for i := 0; i < 4; i++ {
		started := time.Now()
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: fmt.Sprintf("load-worker-%d", os.Getpid()), Role: "data"}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		snap, e := s.BuildSnapshot(ctx, c, "load-mock.v1", "data.v1", cas)
		if e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		pricing := budget.Pricing{Version: "load-free.v1", Provider: "mock", Model: "load-mock.v1"}
		req, e := budget.PricedRequest(c.TaskID, pricing, 64, 64, artifact.Digest([]byte("synthetic load request")))
		if e != nil {
			t.Fatal(e)
		}
		_, e = resources.Model(ctx, c, req, func(context.Context) (budget.ModelUsage, bool, error) {
			return budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 32, InputTokens: 16, OutputTokens: 16, CostMicros: 0, PricingVersion: "load-free.v1", Source: "local_stub"}, true, nil
		})
		if e != nil {
			t.Fatal(e)
		}
		var sum int64
		e = resources.Run(ctx, c, "db", 0, func(ctx context.Context) (bool, error) {
			return true, s.Pool.QueryRow(ctx, "SELECT sum(i)::bigint FROM generate_series(1,20000) i").Scan(&sum)
		})
		if e != nil {
			t.Fatal(e)
		}
		e = resources.Run(ctx, c, "cpu", 0, func(ctx context.Context) (bool, error) {
			raw := make([]byte, 65536)
			for j := 0; j < 64; j++ {
				if ctx.Err() != nil {
					return true, ctx.Err()
				}
				hash := sha256.Sum256(raw)
				copy(raw, hash[:])
			}
			return true, nil
		})
		if e != nil {
			t.Fatal(e)
		}
		body := []byte(fmt.Sprintf(`{"sum":%d}`, sum))
		a, e := s.PublishArtifact(ctx, c, cas, "sql_evidence", "application/json", "load.v1", body)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.PublishArtifact(ctx, c, cas, "sql_evidence", "application/json", "load.v1", body); e == nil {
			t.Fatal("duplicate completion accepted")
		}
		if e = json.NewEncoder(os.Stdout).Encode(loadObservation{run, c.TaskID, os.Getpid(), float64(time.Since(started).Microseconds()) / 1000, a, snap.ID}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestOfflineLoad(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("MGAR_ALLOW_PAID") == "1" {
		t.Fatal("owned Linux offline harness required")
	}
	for _, concurrency := range []int{1, 4, 8, 16} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) { runLoad(t, concurrency) })
	}
}

func runLoad(t *testing.T, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s := testdb.New(t)
	capacity := min(runtime.GOMAXPROCS(0), 4)
	type job struct {
		run   string
		owner store.OrchestratorClaim
		root  string
	}
	jobs := make([]job, n)
	for i := range jobs {
		w, e := s.CreateRun(ctx, "", "bounded synthetic control-plane load", 8)
		if e != nil {
			t.Fatal(e)
		}
		oc, e := s.TakeOrchestrator(ctx, w.RunID, "load-controller", 5*time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BeginPlanning(ctx, w.RunID, oc); e != nil {
			t.Fatal(e)
		}
		if e = s.ConfigureBudget(ctx, w.RunID, oc, budget.Limits{ModelCalls: 4, ToolCalls: 0, Tokens: 1024, CostMicros: 0, ConcurrentCalls: 4, TaskConcurrency: 4, Delegations: 8}); e != nil {
			t.Fatal(e)
		}
		p := planner.Proposal{SchemaVersion: "plan.v1", Rationale: "four bounded independent read-only jobs", Edges: []planner.Edge{}}
		for j := 0; j < 4; j++ {
			p.Nodes = append(p.Nodes, planner.Node{Key: fmt.Sprintf("data%d", j), Role: "data", Goal: "compute fixed synthetic sum", RequiredInputs: []string{}, OutputKind: "sql_evidence"})
		}
		if _, e = s.ProposeAndCommit(ctx, w.RunID, oc, p); e != nil {
			t.Fatal(e)
		}
		if _, e = s.RefreshReady(ctx, w.RunID, oc); e != nil {
			t.Fatal(e)
		}
		jobs[i] = job{w.RunID, oc, filepath.Join(t.TempDir(), "cas")}
	}
	for _, class := range []string{"llm", "cpu", "db"} {
		if e := s.ConfigureResource(ctx, jobs[0].run, jobs[0].owner, class, capacity, 64); e != nil {
			t.Fatal(e)
		}
	}
	type child struct {
		cmd *exec.Cmd
		in  io.WriteCloser
		out *bufio.Reader
	}
	children := make([]child, n)
	for i, j := range jobs {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoadWorker$")
		cmd.Env = append(os.Environ(), "MGAR_LOAD_CHILD=1", "MGAR_CHAOS_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"], "MGAR_CHAOS_RUN="+j.run, "MGAR_CHAOS_CAS="+j.root, "GOMAXPROCS=1")
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
		t.Cleanup(func() {
			_ = in.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		reader := bufio.NewReader(out)
		if line, e := reader.ReadString('\n'); e != nil || line != "READY\n" {
			t.Fatal("child readiness", line, e)
		}
		children[i] = child{cmd, in, reader}
	}
	type sample struct {
		At            float64 `json:"at_ms"`
		Queue         int     `json:"queue"`
		Held          int     `json:"held"`
		MaxClassHeld  int     `json:"max_class_held"`
		DBConnections int     `json:"database_connections"`
		DBActive      int     `json:"database_active"`
		DBLocks       int     `json:"database_ungranted_locks"`
	}
	samples := []sample{}
	var telemetryErr error
	done := make(chan struct{})
	var monitor sync.WaitGroup
	monitor.Add(1)
	started := time.Now()
	var stopOnce sync.Once
	stopMonitor := func() { stopOnce.Do(func() { close(done) }); monitor.Wait() }
	defer stopMonitor()
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var v sample
			v.At = float64(time.Since(started).Microseconds()) / 1000
			telemetryErr = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM resource_requests WHERE state='queued'),(SELECT count(*) FROM resource_requests WHERE state='held'),COALESCE((SELECT max(n) FROM (SELECT count(*) n FROM resource_requests WHERE state='held' GROUP BY class) q),0),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active'),(SELECT count(*) FROM pg_locks WHERE NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&v.Queue, &v.Held, &v.MaxClassHeld, &v.DBConnections, &v.DBActive, &v.DBLocks)
			if telemetryErr != nil {
				return
			}
			samples = append(samples, v)
			select {
			case <-done:
				return
			case <-ctx.Done():
				telemetryErr = ctx.Err()
				return
			case <-ticker.C:
			}
		}
	}()
	observations := []loadObservation{}
	for _, child := range children {
		if _, e := io.WriteString(child.in, "START\n"); e != nil {
			t.Fatal(e)
		}
	}
	for _, child := range children {
		for i := 0; i < 4; i++ {
			line, e := child.out.ReadBytes('\n')
			if e != nil {
				t.Fatal(e)
			}
			var value loadObservation
			if e = json.Unmarshal(line, &value); e != nil {
				t.Fatalf("child failed: %q", line)
			}
			observations = append(observations, value)
		}
		// The test binary emits PASS after the four machine records.
		if _, e := io.Copy(io.Discard, child.out); e != nil {
			t.Fatal(e)
		}
		if e := child.cmd.Wait(); e != nil {
			t.Fatal("worker exit", e)
		}
	}
	executionMS := float64(time.Since(started).Microseconds()) / 1000
	stopMonitor()
	if telemetryErr != nil {
		t.Fatal(telemetryErr)
	}
	expected := artifact.Digest([]byte(`{"sum":200010000}`))
	workflows := []map[string]any{}
	for _, j := range jobs {
		cas, e := artifact.New(j.root)
		if e != nil {
			t.Fatal(e)
		}
		for _, o := range observations {
			if o.Run != j.run {
				continue
			}
			recipe := verifier.Recipe{Method: "cas_hash"}
			req := verifier.Requirement{Kind: "file_hash", ToolVersion: "load.v1", Recipe: recipe, Expected: verifier.Observation{Hash: expected}}
			evidence := verifier.Evidence{Schema: "evidence.v1", Claim: "SQL sum agrees with independent arithmetic formula", Kind: "file_hash", ArtifactID: o.Artifact.ID, Hash: o.Artifact.Hash, RunID: j.run, TaskID: o.Task, PlanVersion: 1, SnapshotID: o.Snapshot, ToolVersion: "load.v1", ExecutionStatus: "succeeded", Recipe: recipe, Observed: verifier.Observation{Hash: o.Artifact.Hash}, Expected: req.Expected}
			raw, _ := json.Marshal(evidence)
			v, e := s.VerifyArtifact(ctx, j.run, j.owner, o.Artifact.ID, raw, req, cas, verifier.Engine{})
			if e != nil || v.Status != "passed" {
				t.Fatal("independent check", v, e)
			}
		}
		_ = cas.Close()
		if e = s.ApplyVerdicts(ctx, j.run, j.owner); e != nil {
			t.Fatal(e)
		}
		if _, e = s.FinalizeWorkflow(ctx, j.run, j.owner); e != nil {
			t.Fatal(e)
		}
		w, e := s.FinalizeWorkflow(ctx, j.run, j.owner)
		if e != nil || w.State != "succeeded" {
			t.Fatal(w, e)
		}
		report, e := s.BudgetReport(ctx, j.run)
		if e != nil || !report.Conserved || report.SettledModels != 4 || report.ReservedModels != 0 || report.SettledCostMicros != 0 {
			t.Fatal(report, e)
		}
		workflows = append(workflows, map[string]any{"run_id": j.run, "state": w.State, "budget": report})
	}
	var tasks, outputs, calls, successEvents, leaked, duplicates int
	if e := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_instances WHERE state='succeeded'),(SELECT count(*) FROM artifacts WHERE kind='sql_evidence'),(SELECT count(*) FROM model_calls),(SELECT count(*) FROM workflow_events WHERE kind='workflow_succeeded'),(SELECT count(*) FROM resource_requests WHERE state IN ('queued','held','unknown')),(SELECT count(*) FROM (SELECT producer_task FROM artifacts WHERE kind='sql_evidence' GROUP BY producer_task HAVING count(*)>1) q)`).Scan(&tasks, &outputs, &calls, &successEvents, &leaked, &duplicates); e != nil || tasks != n*4 || outputs != n*4 || calls != n*4 || successEvents != n || leaked != 0 || duplicates != 0 {
		t.Fatal("load invariants", tasks, outputs, calls, successEvents, leaked, duplicates, e)
	}
	var dbVersion string
	var bytes int64
	if e := s.Pool.QueryRow(ctx, "SELECT current_setting('server_version'),pg_database_size(current_database())").Scan(&dbVersion, &bytes); e != nil {
		t.Fatal(e)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Task < observations[j].Task })
	for _, v := range samples {
		if v.MaxClassHeld > capacity {
			t.Fatal("resource capacity exceeded", v)
		}
	}
	report := map[string]any{"schema_version": "offline-load.v1", "concurrency": n, "resource_capacity": capacity, "tasks_per_worker": 4, "assigned": n * 4, "completed": tasks, "failed": 0, "failure_categories": map[string]int{}, "worker_observations": observations, "samples": samples, "execution_ms": executionMS, "including_verification_ms": float64(time.Since(started).Microseconds()) / 1000, "workflows": workflows, "model_calls": calls, "duplicate_outputs": duplicates, "resource_leaks": leaked, "terminal_events": successEvents, "database_version": dbVersion, "database_bytes_after": bytes, "go_version": runtime.Version(), "cpu_visible": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "namespace": s.Pool.Config().ConnConfig.RuntimeParams["search_path"], "workload": "trusted Go Worker control-plane + fixed synthetic SQL + CPU hash + local model stub; not real LLM or Python throughput"}
	raw, e := json.Marshal(report)
	if e != nil {
		t.Fatal(e)
	}
	t.Log("LOAD_REPORT " + string(raw))
}
