package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIExecutableFullDiagnosticTimeline(t *testing.T) {
	ctx := context.Background()
	s, w, oc := lifecycleFixture(t, false)
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(t.TempDir(), "mgarctl")
	build := exec.Command("go", "build", "-o", binary, "./cmd/mgarctl")
	build.Dir = root
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %s %v", b, e)
	}
	casRoot := filepath.Join(t.TempDir(), "cas")
	cas, e := artifact.New(casRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	p := lifecyclePlan(false)
	p.BaseVersion = 1
	p.Nodes = append(p.Nodes, planner.Node{Key: "B", Role: "data", Goal: "synthetic data", RequiredInputs: []string{}, OutputKind: "sql_evidence"})
	if _, e = s.ProposeAndCommit(ctx, w.RunID, oc, p); e != nil {
		t.Fatal(e)
	}
	if e = s.ConfigureBudget(ctx, w.RunID, oc, budget.Limits{ModelCalls: 2, ToolCalls: 2, Tokens: 100, CostMicros: 100, ConcurrentCalls: 2, TaskConcurrency: 4, Delegations: 24}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "worker-first", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "worker-takeover", Role: "research"}, time.Minute)
	if e != nil || c.Epoch != 2 {
		t.Fatal(c, e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishArtifact(ctx, c, cas, "finding", "application/json", "research.v1", []byte(`{"synthetic":"E2E_PRIVATE_BODY"}`))
	if e != nil {
		t.Fatal(e)
	}
	schema := s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	cli := func(role string, want int, args ...string) map[string]any {
		t.Helper()
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		command := exec.CommandContext(bounded, binary, args...)
		command.Dir = root
		env := []string{}
		for _, v := range os.Environ() {
			key, _, _ := strings.Cut(v, "=")
			if key != "PGOPTIONS" && key != "MGAR_ARTIFACT_ROOT" && key != "MGAR_PROVIDER" {
				env = append(env, v)
			}
		}
		opts := "-c search_path=" + schema
		if role != "" {
			opts += " -c role=" + role
		}
		command.Env = append(env, "PGOPTIONS="+opts, "MGAR_ARTIFACT_ROOT="+casRoot, "MGAR_PROVIDER=mock")
		var out, errs bytes.Buffer
		command.Stdout = &out
		command.Stderr = &errs
		e := command.Run()
		code := 0
		if e != nil {
			if exit, ok := e.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(e)
			}
		}
		if code != want {
			t.Fatalf("CLI %s exit=%d want=%d stderr=%s output=%s", args[0], code, want, errs.String(), out.String())
		}
		t.Logf("actual mgarctl %s exit=%d", args[0], code)
		if out.Len() == 0 {
			return nil
		}
		var result map[string]any
		if e = json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal("CLI output not JSON", e)
		}
		if strings.Contains(out.String(), "E2E_PRIVATE_BODY") {
			t.Fatal("artifact body leaked")
		}
		return result
	}
	report := cli("", 0, "trace", w.RunID, "--format", "json")
	if report["active_plan_version"] != float64(2) || len(report["plans"].([]any)) != 2 || len(report["artifacts"].([]any)) != 1 {
		t.Fatal("plan/artifact timeline incomplete")
	}
	takeover := false
	for _, item := range report["tasks"].([]any) {
		task := item.(map[string]any)
		if task["Epoch"] == float64(2) && task["Attempts"] == float64(2) {
			takeover = true
		}
	}
	if !takeover {
		t.Fatal("retry/takeover absent")
	}
	inspected := cli("", 0, "inspect-artifact", a.ID, "--run", w.RunID)
	if inspected["bytes_verified"] != true {
		t.Fatal("CAS not checked")
	}
	cli("", 0, "status", w.RunID)
	cli("", 0, "budget-report", w.RunID)
	healthy := cli("", 0, "doctor")
	if healthy["status"] != "ok" || len(healthy["actions_taken"].([]any)) != 0 {
		t.Fatal("doctor actions")
	}
	// Read-only DB role can diagnose; an Agent DB role cannot self-authorize.
	cli("mgar_reader", 0, "trace", w.RunID)
	denied := cli("mgar_worker", 1, "trace", w.RunID)
	if denied["error"] != "NOT_AUTHORIZED" {
		t.Fatal("wrong identity not rejected")
	}
	other, e := s.CreateRun(ctx, "", "PRIVATE_OTHER_RUN", 4)
	if e != nil {
		t.Fatal(e)
	}
	denied = cli("", 1, "inspect-artifact", a.ID, "--run", other.RunID)
	if denied["error"] != "NOT_FOUND_OR_UNREADABLE" {
		t.Fatal("foreign artifact")
	}
	cli("", 1, "trace", "ffffffff-ffff-4fff-8fff-ffffffffffff")
	data, e := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "data", Role: "data"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildSnapshot(ctx, data, "mock.v1", "data.v1", cas); e != nil {
		t.Fatal(e)
	}
	data, e = s.StartTask(ctx, data)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RecordToolDenial(ctx, data, artifact.Digest([]byte("denied")), "policy_denied"); e != nil {
		t.Fatal(e)
	}
	call, e := s.ReserveModelCall(ctx, data, budget.ModelRequest{LogicalID: "11111111-1111-4111-8111-111111111111", Provider: "mock", Model: "mock.v1", Tokens: 10, CostMicros: 10})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishModelCall(ctx, data, call.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
		t.Fatal(e)
	}
	report = cli("", 0, "trace", w.RunID)
	if report["denied_tool_actions"] != float64(1) || report["budget"].(map[string]any)["unknown_models"] != float64(1) {
		t.Fatal("unknown/denied hidden")
	}
	cli("", 1, "doctor")
	if e = s.RelinquishOrchestrator(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	cli("", 1, "resume", w.RunID)
	cli("", 0, "cancel", w.RunID)
	cli("", 1, "resume", w.RunID)
}
