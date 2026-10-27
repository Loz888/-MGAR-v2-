package live

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFrozenLivePlanAllocation(t *testing.T) {
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Assignments) != 96 || p.CapMicros != 2_000_000 || p.Model != "flash-1017" {
		t.Fatal("wrong authorization")
	}
	var total int64
	ids := map[string]bool{}
	for _, a := range p.Assignments {
		if a.CapMicros != 20833 || ids[a.RunID] {
			t.Fatal("unfair or duplicate allocation")
		}
		ids[a.RunID] = true
		total += a.CapMicros
	}
	if total != 1_999_968 {
		t.Fatal(total)
	}
	for _, mode := range []string{"missing", "duplicate", "cost", "model", "identity"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(p)
			var q Plan
			_ = json.Unmarshal(raw, &q)
			switch mode {
			case "missing":
				q.Assignments = q.Assignments[:95]
			case "duplicate":
				q.Assignments[1] = q.Assignments[0]
			case "cost":
				q.Assignments[0].CapMicros++
			case "model":
				q.Model = "different"
			case "identity":
				q.Assignments[0].RunID = q.Assignments[1].RunID
			}
			if q.Validate() == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}
func TestLivePreflightNoKeyNoSend(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("MGAR_ALLOW_PAID", "0")
	root, _ := filepath.Abs("../..")
	r := Preflight(root, false)
	if r.Ready || len(r.Blockers) < 3 || r.Assigned != 96 {
		t.Fatal(r)
	}
	if _, e := Run(context.Background(), root, false); e == nil {
		t.Fatal("live default enabled")
	}
}

// Public pricing evidence is available, but it cannot make a conservative
// full-context reservation fit the already frozen cost/token share. This test
// has no network or database writes and must never consume the live allocation.
func TestOfficialFlash1017QuoteRejectsBeforeAllocation(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "synthetic-never-sent")
	t.Setenv("MGAR_ALLOW_PAID", "1")
	t.Setenv("MGAR_MAX_COST_USD", "2.00")
	t.Setenv("MGAR_LIVE_CASELIST", "eval/cases/frozen-live-24.json")
	source, _ := filepath.Abs("../..")
	root := copyLiveInputs(t)
	var frozenPrice PricingEvidence
	if readJSON(filepath.Join(source, "eval/live/pricing.json"), &frozenPrice) != nil {
		t.Fatal("read published price")
	}
	frozenPrice.Config.InputBound = nil
	frozenPrice.Config.MaxPromptBytes = 65536
	frozenPrice.Config.Price.Version = strings.Split(frozenPrice.Config.Price.Version, ".u8-")[0]
	for _, name := range []string{"official-evidence.json"} {
		raw, e := os.ReadFile(filepath.Join(source, "eval/live", name))
		if e != nil || os.WriteFile(filepath.Join(root, "eval/live", name), raw, 0600) != nil {
			t.Fatal("copy published evidence")
		}
	}
	raw, _ := json.Marshal(frozenPrice)
	if os.WriteFile(filepath.Join(root, "eval/live/pricing.json"), raw, 0600) != nil {
		t.Fatal("copy legacy price")
	}
	r := Preflight(root, true)
	if r.Ready || r.Pricing.Status != "VERIFIED" || !slices.Contains(r.Blockers, "conservative_call_bound_exceeds_slot_budget") {
		t.Fatal("published quote must be verified but unaffordable under the frozen policy", r.Blockers)
	}
	bound, e := r.Pricing.Config.Price.Cost(r.Pricing.Config.ContextTokens, r.Pricing.Config.OutputTokens)
	if e != nil || bound != 315802 || len(r.Assignments) != 96 || r.AllocatedMicros != 1999968 {
		t.Fatal("unexpected conservative reservation/allocation", bound, e)
	}
	if e := r.Pricing.Config.Price.Valid(); !e {
		t.Fatal("invalid published price")
	}
	if _, e := os.Lstat(statePath(root)); !os.IsNotExist(e) {
		t.Fatal("preflight consumed or found a live allocation")
	}
}
func TestLiveJournalChild(t *testing.T) {
	dir := os.Getenv("MGAR_LIVE_TEST_JOURNAL")
	if dir == "" {
		return
	}
	root, _ := filepath.Abs("../..")
	p, e := LoadPlanRound(root, os.Getenv("MGAR_LIVE_TEST_ROUND"))
	if e != nil {
		t.Fatal(e)
	}
	if schema := os.Getenv("MGAR_LIVE_TEST_SCHEMA"); schema != "" {
		cfg, e := pgxpool.ParseConfig(testdb.DSN(t))
		if e != nil {
			t.Fatal("test DB config")
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer pool.Close()
		if claimExperiment(context.Background(), &store.Store{Pool: pool}, p) != nil {
			os.Exit(23)
		}
	}
	if _, e = begin(dir, p); e != nil {
		os.Exit(23)
	}
	if os.Getenv("MGAR_LIVE_TEST_KILL") == "1" {
		if e := saveNew(filepath.Join(dir, "000.started.json"), p.Assignments[0]); e != nil {
			t.Fatal(e)
		}
		fmt.Println("DURABLE_STARTED")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
}
func TestLiveJournalTwoProcessesRestartAndLostAssignment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "experiment")
	commands := []*exec.Cmd{}
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLiveJournalChild$")
		cmd.Env = append(os.Environ(), "MGAR_LIVE_TEST_JOURNAL="+dir)
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		commands = append(commands, cmd)
	}
	wins, denied := 0, 0
	for _, cmd := range commands {
		e := cmd.Wait()
		if e == nil {
			wins++
		} else if exit, ok := e.(*exec.ExitError); ok && exit.ExitCode() == 23 {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || denied != 1 {
		t.Fatal(wins, denied)
	}
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = begin(dir, p); e == nil {
		t.Fatal("restart minted new allocation")
	}
	if e = os.Remove(filepath.Join(dir, "assignment.json")); e != nil {
		t.Fatal(e)
	}
	if _, e = begin(dir, p); e == nil {
		t.Fatal("missing assignment recreated budget")
	}
	t.Log("two OS processes: one admission, one denial; restart and lost-manifest both refused")
}

func TestLiveJournalActualSIGKILL(t *testing.T) {
	s := testdb.New(t)
	dir := filepath.Join(t.TempDir(), "experiment")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLiveJournalChild$")
	cmd.Env = append(os.Environ(), "MGAR_LIVE_TEST_JOURNAL="+dir, "MGAR_LIVE_TEST_KILL=1", "MGAR_LIVE_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer in.Close()
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, e := bufio.NewReader(out).ReadString('\n')
	if e != nil || line != "DURABLE_STARTED\n" {
		t.Fatal("barrier", line, e)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	e = cmd.Wait()
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("not SIGKILL", e)
	}
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = begin(dir, p); e == nil {
		t.Fatal("crash recreated allocation")
	}
	if e = claimExperiment(context.Background(), s, p); e == nil {
		t.Fatal("crash recreated PG allocation")
	}
	if _, e = os.Stat(filepath.Join(dir, "000.started.json")); e != nil {
		t.Fatal("lost durable intent")
	}
	t.Logf("actual SIGKILL pid=%d; started assignment retained and restart denied", cmd.Process.Pid)
}
func TestLiveJournalUnknownStopsAndKeepsAllAssignments(t *testing.T) {
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	path, e := begin(filepath.Join(t.TempDir(), "experiment"), p)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	report, e := execute(context.Background(), path, p, func(ctx context.Context, a Assignment) (demo.ExperimentReport, error) {
		calls++
		return demo.ExperimentReport{RunID: a.RunID, Arm: a.Arm, Case: demo.ExperimentCase{ID: a.Case}, ExecutionStatus: "failed", Models: []json.RawMessage{json.RawMessage(`{"status":"unknown"}`)}, Result: map[string]any{"budget": budget.Report{Conserved: true, Limits: budget.Limits{CostMicros: a.CapMicros}, ReservedModels: 1, UnknownModels: 1, ReservedCostMicros: 100}}}, nil
	})
	if e != nil || calls != 1 || len(report.Records) != 96 || report.Status != "STOPPED_UNKNOWN" || report.UpperCostMicros != 20833 {
		t.Fatal(calls, report.Status, report.UpperCostMicros, e)
	}
	for _, r := range report.Records[1:] {
		if r.Status != "NOT_RUN" {
			t.Fatal("dropped or fabricated assignment")
		}
	}
	if _, e = begin(path, p); e == nil {
		t.Fatal("unknown rerun")
	}
	raw, e := os.ReadFile(filepath.Join(path, "000.raw.json"))
	if e != nil || artifact.Digest(raw) != report.Records[0].RawSHA {
		t.Fatal("raw digest mismatch", e)
	}
}
func TestLivePreflightPriceBoundsAndEvidence(t *testing.T) {
	root := copyLiveInputs(t)
	t.Setenv("DEEPSEEK_API_KEY", "synthetic-never-sent")
	t.Setenv("MGAR_ALLOW_PAID", "1")
	t.Setenv("MGAR_MAX_COST_USD", "2.00")
	t.Setenv("MGAR_LIVE_CASELIST", "eval/cases/frozen-live-24.json")
	evidence := []byte(`{"fixture":"SYNTHETIC UNIT TEST, NOT OFFICIAL PRICING"}`)
	if e := os.WriteFile(filepath.Join(root, "eval/live/official-evidence.json"), evidence, 0600); e != nil {
		t.Fatal(e)
	}
	price := PricingEvidence{Status: "VERIFIED", ReviewedAt: time.Now().UTC().Format(time.RFC3339), PricingURL: "https://api-docs.deepseek.com/quick_start/pricing", ModelsURL: "https://api.deepseek.com/models", Config: provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "synthetic-test.v1", InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}, ContextTokens: 4096, OutputTokens: 1024, MaxPromptBytes: 65536, EvidenceSHA256: artifact.Digest(evidence)}}
	for _, mode := range []string{"valid", "unaffordable", "context", "price", "hash", "stale", "model", "byte-bound", "bad-bound", "unbound-price"} {
		t.Run(mode, func(t *testing.T) {
			q := price
			switch mode {
			case "unaffordable":
				q.Config.Price.InputMicrosPerMillion = 100_000_000
			case "context":
				q.Config.ContextTokens = 100000
			case "price":
				q.Config.Price.InputMicrosPerMillion = 0
			case "hash":
				q.Config.EvidenceSHA256 = "bad"
			case "stale":
				q.ReviewedAt = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
			case "model":
				q.Config.Price.Model = "another-model"
			case "byte-bound", "bad-bound", "unbound-price":
				proof := provider.OfficialTextInputBound()
				q.Config.InputBound = &proof
				q.Config.ContextTokens = 1048576
				q.Config.MaxPromptBytes = 48000
				q.Config.Price.Version = provider.TextBoundPriceVersion(q.Config.Price.Version)
				if mode == "bad-bound" {
					q.Config.InputBound.TokenizerSHA256 = "changed"
				}
				if mode == "unbound-price" {
					q.Config.Price.Version = "old-unbound.v1"
				}
			}
			raw, _ := json.Marshal(q)
			if e := os.WriteFile(filepath.Join(root, "eval/live/pricing.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			r := Preflight(root, true)
			if r.Ready != (mode == "valid" || mode == "byte-bound") {
				t.Fatal(mode, r.Blockers)
			}
		})
	}
}

func TestLiveKnownFailureAccountingAndUncertainTools(t *testing.T) {
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"known-failure", "known-failure-error", "contradictory-error", "unknown-model-error", "unknown-tool", "pending-tool", "wrong-budget-run", "missing-model", "missing-tool"} {
		t.Run(mode, func(t *testing.T) {
			path, e := begin(filepath.Join(t.TempDir(), "experiment"), p)
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			report, e := execute(context.Background(), path, p, func(_ context.Context, a Assignment) (demo.ExperimentReport, error) {
				calls++
				b := budget.Report{RunID: a.RunID, Conserved: true, Limits: budget.Limits{CostMicros: a.CapMicros}, SettledModels: 1, SettledCostMicros: 15}
				observed := demo.ExperimentReport{RunID: a.RunID, Arm: a.Arm, Case: demo.ExperimentCase{ID: a.Case}, ExecutionStatus: "failed", Models: []json.RawMessage{json.RawMessage(`{"status":"failed"}`)}}
				var invokeErr error
				switch mode {
				case "known-failure-error":
					invokeErr = errors.New("synthetic_control_error")
				case "contradictory-error":
					observed.ExecutionStatus = "completed"
					invokeErr = errors.New("synthetic_control_error")
				case "unknown-model-error":
					b.UnknownModels, b.ReservedModels, b.ReservedCostMicros = 1, 1, 10
					observed.Models = append(observed.Models, json.RawMessage(`{"status":"unknown"}`))
					invokeErr = errors.New("synthetic_control_error")
				case "unknown-tool", "pending-tool":
					b.ReservedTools = 1
					if mode == "unknown-tool" {
						b.UnknownTools = 1
					} else {
						b.PendingTools = 1
					}
					observed.Tools = []json.RawMessage{json.RawMessage(`{"status":"unknown"}`)}
				case "wrong-budget-run":
					b.RunID = "other-run"
				case "missing-model":
					observed.Models = nil
				case "missing-tool":
					b.SettledTools = 1
				}
				observed.Result = map[string]any{"budget": b}
				return observed, invokeErr
			})
			knownFailure := mode == "known-failure" || mode == "known-failure-error"
			wantKnown := int64(15)
			if mode == "wrong-budget-run" || mode == "missing-model" || mode == "missing-tool" {
				wantKnown = 0
			}
			if knownFailure {
				if e != nil || calls != 96 || report.Status != "EXECUTED_PENDING_INDEPENDENT_EVALUATION" || report.KnownCostMicros != 96*15 || report.UpperCostMicros != 96*15 {
					t.Fatalf("known failed assignments not retained: calls=%d report=%+v err=%v", calls, report, e)
				}
				for _, record := range report.Records {
					if record.Status != "failed" {
						t.Fatal("failed result was promoted or lost")
					}
				}
			} else {
				if e != nil || calls != 1 || report.Status != "STOPPED_UNKNOWN" || report.KnownCostMicros != wantKnown || report.UpperCostMicros != 20833 {
					t.Fatalf("uncertain assignment did not stop with preserved cost: calls=%d known=%d upper=%d status=%s err=%v", calls, report.KnownCostMicros, report.UpperCostMicros, report.Status, e)
				}
				for _, record := range report.Records[1:] {
					if record.Status != "NOT_RUN" {
						t.Fatal("uncertain result admitted another assignment")
					}
				}
			}
		})
	}
}

func copyLiveInputs(t *testing.T) string {
	t.Helper()
	source, _ := filepath.Abs("../..")
	root := t.TempDir()
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	raw, e := os.ReadFile(filepath.Join(source, "eval/dataset_manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	_ = json.Unmarshal(raw, &manifest)
	for _, p := range append([]string{"eval/dataset_manifest.json", "eval/live/plan.json"}, mapKeys(manifest.Files)...) {
		raw, e := os.ReadFile(filepath.Join(source, p))
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(root, p)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return root
}
func mapKeys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLivePostgresGuardSurvivesWholeJournalLoss(t *testing.T) {
	s := testdb.New(t)
	dir := filepath.Join(t.TempDir(), "experiment")
	cmds := []*exec.Cmd{}
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLiveJournalChild$")
		cmd.Env = append(os.Environ(), "MGAR_LIVE_TEST_JOURNAL="+dir, "MGAR_LIVE_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		cmds = append(cmds, cmd)
	}
	win, deny := 0, 0
	for _, cmd := range cmds {
		e := cmd.Wait()
		if e == nil {
			win++
		} else if exit, ok := e.(*exec.ExitError); ok && exit.ExitCode() == 23 {
			deny++
		} else {
			t.Fatal(e)
		}
	}
	if win != 1 || deny != 1 {
		t.Fatal(win, deny)
	}
	if e := os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	}
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = claimExperiment(context.Background(), s, p); e == nil {
		t.Fatal("lost journal minted budget")
	}
	w, e := s.Workflow(context.Background(), demo.LiveRunID("allocation", "all"))
	if e != nil || w.State != "cancelled" {
		t.Fatal(w.State, e)
	}
	r, e := s.BudgetReport(context.Background(), w.RunID)
	if e != nil || r.Limits.CostMicros != 0 || r.ReservedModels != 0 || r.SettledModels != 0 {
		t.Fatal(r, e)
	}
	t.Log("two PG-backed OS processes: 1 winner; entire local journal removed; fixed zero-spend Run still refuses a second allocation")
}

func TestLivePostgresSchemaReadinessBeforeAllocation(t *testing.T) {
	ctx := context.Background()
	root, _ := filepath.Abs("../..")
	p, e := LoadPlan(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"legacy-004", "missing-migration-table", "changed-checksum", "missing-budget-column", "missing-resource-pools", "missing-resource-requests", "ready"} {
		t.Run(mode, func(t *testing.T) {
			admin := testdb.New(t)
			schema := "mgar_live_ready_" + strings.ToLower(rand.Text())
			ident := pgx.Identifier{schema}.Sanitize()
			if _, e := admin.Pool.Exec(ctx, "CREATE SCHEMA "+ident); e != nil {
				t.Fatal(e)
			}
			cfg := admin.Pool.Config().Copy()
			cfg.ConnConfig.RuntimeParams["search_path"] = schema
			pool, e := pgxpool.NewWithConfig(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				pool.Close()
				if _, e := admin.Pool.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); e != nil {
					t.Error(e)
				}
			})
			s := &store.Store{Pool: pool}
			if mode == "legacy-004" {
				for _, m := range []struct{ version, sql string }{{"001_control", migrations.ControlSQL}, {"002_artifact_cas", migrations.ArtifactSQL}, {"003_context_snapshots", migrations.ContextSQL}, {"004_plan_node_configs", migrations.PlanConfigSQL}} {
					if e := store.ApplyMigration(ctx, pool, m.version, m.sql); e != nil {
						t.Fatal(e)
					}
				}
			} else if e := store.Migrate(ctx, pool); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "missing-migration-table":
				_, e = pool.Exec(ctx, "DROP TABLE schema_migrations")
			case "changed-checksum":
				_, e = pool.Exec(ctx, "UPDATE schema_migrations SET checksum='changed' WHERE version='005_global_budgets'")
			case "missing-budget-column":
				_, e = pool.Exec(ctx, "ALTER TABLE run_budgets DROP COLUMN max_call_concurrency CASCADE")
			case "missing-resource-pools":
				_, e = pool.Exec(ctx, "DROP TABLE resource_pools CASCADE")
			case "missing-resource-requests":
				_, e = pool.Exec(ctx, "DROP TABLE resource_requests")
			}
			if e != nil {
				t.Fatal(e)
			}
			claimErr := claimExperiment(ctx, s, p)
			var count int
			if e := pool.QueryRow(ctx, "SELECT count(*) FROM workflow_runs WHERE run_id=$1", demo.LiveRunID("allocation", "all")).Scan(&count); e != nil {
				t.Fatal(e)
			}
			if mode == "ready" {
				if claimErr != nil || count != 1 {
					t.Fatal("ready schema failed admission", count, claimErr)
				}
			} else if claimErr == nil || count != 0 {
				t.Fatalf("unready schema consumed allocation: marker_rows=%d err=%v", count, claimErr)
			}
		})
	}
}
