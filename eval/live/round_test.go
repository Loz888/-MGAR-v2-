package live

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/demo"
)

func round2TestRoot(t *testing.T) string {
	t.Helper()
	root := copyLiveInputs(t)
	source, _ := filepath.Abs("../..")
	names := []string{"eval/live/round2/plan.json", "eval/live/pricing.json", "eval/live/official-evidence.json", "evidence/eval/live/" + experimentID + "/report.json", "internal/demo/dispatch.go", "internal/demo/worker.go", "internal/policy/gateway.go"}
	for _, name := range names {
		raw, e := os.ReadFile(filepath.Join(source, name))
		if e != nil {
			t.Fatal(e)
		}
		p := filepath.Join(root, name)
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(p, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return root
}
func TestRound2FreezeAndBudget(t *testing.T) {
	source, _ := filepath.Abs("../..")
	p, e := LoadPlanRound(source, "round2")
	if e != nil {
		t.Fatal(e)
	}
	old, e := LoadPlan(source)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, a := range old.Assignments {
		seen[a.RunID] = true
	}
	if p.CapMicros != 1900000 || p.Previous == nil || p.Previous.UpperCostMicros != 33850 || p.CapMicros+p.Previous.UpperCostMicros > 2000000 {
		t.Fatal(p)
	}
	for _, a := range p.Assignments {
		if seen[a.RunID] || a.CapMicros != 19791 || a.RunID != demo.LiveRunIDForRound("round2", a.Case, a.Arm) {
			t.Fatal(a)
		}
	}
	if p.allocationID() == old.allocationID() {
		t.Fatal("reused allocation marker")
	}
	if demo.LiveRunIDForRound("unknown", "live-001", "B0") != "" {
		t.Fatal("arbitrary round accepted")
	}
}
func TestRound2PreflightRejectsChangedCheckpoint(t *testing.T) {
	for _, mode := range []string{"valid", "budget", "old-identity", "changed-prior", "missing-prior", "changed-contract", "missing-contract", "changed-price", "journal"} {
		t.Run(mode, func(t *testing.T) {
			root := round2TestRoot(t)
			t.Setenv("MGAR_ALLOW_PAID", "1")
			t.Setenv("MGAR_MAX_COST_USD", "2.00")
			t.Setenv("MGAR_LIVE_CASELIST", "eval/cases/frozen-live-24.json")
			t.Setenv("DEEPSEEK_API_KEY", "synthetic-not-sent")
			plan, e := LoadPlanRound(root, "round2")
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "budget":
				plan.CapMicros = 2000000
			case "old-identity":
				plan.Assignments[0].RunID = demo.LiveRunID("live-001", "B0")
			case "changed-prior":
				if e = os.WriteFile(filepath.Join(root, "evidence/eval/live", experimentID, "report.json"), []byte(`{}`), 0600); e != nil {
					t.Fatal(e)
				}
			case "missing-prior":
				if e = os.Remove(filepath.Join(root, "evidence/eval/live", experimentID, "report.json")); e != nil {
					t.Fatal(e)
				}
			case "changed-contract":
				if e = os.WriteFile(filepath.Join(root, "internal/demo/dispatch.go"), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "missing-contract":
				if e = os.Remove(filepath.Join(root, "internal/demo/dispatch.go")); e != nil {
					t.Fatal(e)
				}
			case "changed-price":
				if e = os.WriteFile(filepath.Join(root, "eval/live/pricing.json"), []byte(`{}`), 0600); e != nil {
					t.Fatal(e)
				}
			case "journal":
				if e = os.MkdirAll(statePathFor(root, plan.ID), 0700); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "budget" || mode == "old-identity" {
				raw, _ := json.Marshal(plan)
				if e = os.WriteFile(filepath.Join(root, "eval/live/round2/plan.json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			r := PreflightRound(root, true, "round2")
			if r.Ready != (mode == "valid") {
				t.Fatal(mode, r.Blockers)
			}
			if mode == "valid" && (r.CapMicros != 1900000 || r.AllocatedMicros != 1899936 || r.PreviousUpperCostMicros != 33850 || r.CumulativeCapMicros != 1933850) {
				t.Fatal(r)
			}
		})
	}
}

func TestRound2TwoProcessesAndLostJournal(t *testing.T) {
	s := testdb.New(t)
	dir := filepath.Join(t.TempDir(), "round2")
	schema := s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	cmds := []*exec.Cmd{}
	for i := 0; i < 2; i++ {
		c := exec.Command(os.Args[0], "-test.run=^TestLiveJournalChild$")
		c.Env = append(os.Environ(), "MGAR_LIVE_TEST_ROUND=round2", "MGAR_LIVE_TEST_SCHEMA="+schema, "MGAR_LIVE_TEST_JOURNAL="+dir)
		if e := c.Start(); e != nil {
			t.Fatal(e)
		}
		cmds = append(cmds, c)
	}
	wins, denied := 0, 0
	for _, c := range cmds {
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
	source, _ := filepath.Abs("../..")
	p, e := LoadPlanRound(source, "round2")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	} // Only this test's private journal.
	if e = claimExperiment(context.Background(), s, p); e == nil {
		t.Fatal("lost journal minted round2 allocation")
	}
	w, e := s.Workflow(context.Background(), p.allocationID())
	if e != nil || w.State != "cancelled" {
		t.Fatal(w, e)
	}
	b, e := s.BudgetReport(context.Background(), p.allocationID())
	if e != nil || !b.Conserved || b.Limits.CostMicros != 0 || b.SettledModels != 0 {
		t.Fatal(b, e)
	}
	if allocationReadiness(context.Background(), s, p) == nil {
		t.Fatal("read-only readiness ignored existing marker")
	}
	t.Log("round2: one of two OS processes admitted; original round marker untouched; deleted private journal cannot recreate allocation")
}
