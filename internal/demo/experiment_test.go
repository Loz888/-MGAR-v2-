package demo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func experimentTest(t *testing.T, caseID, arm string) ExperimentReport {
	t.Helper()
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r, e := RunExperiment(ctx, Options{Root: root, Case: caseID, Fault: "none"}, arm)
	if e != nil {
		t.Fatalf("%s %s: %v", arm, caseID, e)
	}
	return r
}
func TestExperimentSevenArmsRealAnalysis(t *testing.T) {
	namespaces := map[string]bool{}
	for _, arm := range []string{"B0", "B1", "B2", "B3", "B4", "A1", "A2"} {
		t.Run(arm, func(t *testing.T) {
			r := experimentTest(t, "offline-002", arm)
			if r.ExecutionStatus != "completed" {
				raw, _ := json.Marshal(r.Result)
				t.Fatalf("status %s: %.2200s", r.ExecutionStatus, raw)
			}
			if namespaces[r.Namespace] || r.Namespace == "" || len(r.Models) == 0 || len(r.Tools) == 0 {
				t.Fatal("missing isolation/accounting")
			}
			namespaces[r.Namespace] = true
			if !r.Treatment.Verify && r.WorkflowSuccess != nil {
				t.Fatal("baseline elevated to workflow success")
			}
			if arm == "B0" {
				b := r.Result.(BaselineReport)
				if len(b.Workers) != 1 || b.Parallel != 1 {
					t.Fatal("B0 was decomposed")
				}
			}
		})
	}
}
func TestExperimentInputChangeReplanAblation(t *testing.T) {
	for _, arm := range []string{"B0", "B1", "B2", "B3", "B4", "A1", "A2"} {
		t.Run(arm, func(t *testing.T) {
			r := experimentTest(t, "offline-026", arm)
			if len(r.InputTransitions) < 2 {
				t.Fatal("input change not executed")
			}
			if arm == "A1" {
				if r.ExecutionStatus == "completed" || len(r.Replans) != 0 {
					t.Fatal("no-replan arm bypassed failed evidence")
				}
				return
			}
			if r.ExecutionStatus != "completed" {
				raw, _ := json.Marshal(r.Result)
				t.Fatalf("status %s: %.2600s", r.ExecutionStatus, raw)
			}
			if arm == "B4" && (len(r.Replans) != 1 || !r.Replans[0].Accepted || len(r.Replans[0].Decision.Reuse) == 0) {
				t.Fatalf("no real partial replan: %+v", r.Replans)
			}
			root, _ := filepath.Abs("../..")
			raw, e := os.ReadFile(filepath.Join(root, "eval/oracle/v1_truth.json"))
			if e != nil {
				t.Fatal(e)
			}
			var truth map[string]map[string]any
			_ = json.Unmarshal(raw, &truth)
			got, _ := json.Marshal(r.Answer)
			want, _ := json.Marshal(truth["offline-026"])
			if (string(got) == string(want)) == (!r.Treatment.Replan) {
				t.Fatal("stale answer handling mismatch")
			}
		})
	}
}
func TestExperimentActualFaultsRetained(t *testing.T) {
	for _, arm := range []string{"B0", "B3", "B4"} {
		for _, caseID := range []string{"offline-020", "offline-038", "offline-044", "offline-050", "offline-056"} {
			t.Run(arm+"/"+caseID, func(t *testing.T) {
				r := experimentTest(t, caseID, arm)
				if caseID == "offline-020" {
					failed := false
					for _, raw := range r.Tools {
						var tool map[string]any
						_ = json.Unmarshal(raw, &tool)
						// This injected readonly fault explicitly proves execution stopped.
						// It remains a failed record; the SDK still stops without replay.
						failed = failed || (tool["capability"] == "data.query" && tool["effect"] == "readonly" && tool["status"] == "failed")
					}
					if r.ExecutionStatus != "failed" || !failed {
						t.Fatal("tool fault missing")
					}
				}
				if caseID == "offline-038" && (r.DuplicateDeliveries == 0 || r.ExecutionStatus != "completed") {
					t.Fatal("duplicate injection missing")
				}
				if caseID == "offline-044" && (len(r.Models) != 0 || r.ExecutionStatus == "completed") {
					t.Fatal("zero budget bypass")
				}
				if (caseID == "offline-050" || caseID == "offline-056") && (r.ExecutionStatus != "blocked" || len(r.Tools) != 0) {
					t.Fatal("refusal/policy boundary")
				}
			})
		}
	}
}

func TestExperimentSeedReproducesSemanticDecisions(t *testing.T) {
	for _, arm := range []string{"B0", "B3", "B4"} {
		t.Run(arm, func(t *testing.T) {
			a, b := experimentTest(t, "offline-026", arm), experimentTest(t, "offline-026", arm)
			ar, _ := json.Marshal(a.Answer)
			br, _ := json.Marshal(b.Answer)
			if string(ar) != string(br) || a.ExecutionStatus != b.ExecutionStatus || len(a.Models) != len(b.Models) || len(a.Tools) != len(b.Tools) {
				t.Fatal("same seed changed mock semantic decisions")
			}
			if a.Namespace == b.Namespace || a.CASNamespace == b.CASNamespace || a.RunID == b.RunID {
				t.Fatal("replicate reused state")
			}
		})
	}
}

func TestExperimentInvalidConfiguration(t *testing.T) {
	root, _ := filepath.Abs("../..")
	for _, entry := range []struct{ arm, caseID, fault string }{{"unknown", "offline-002", "none"}, {"B0", "live-001", "none"}, {"B4", "offline-002", "forge-data"}} {
		if _, e := RunExperiment(context.Background(), Options{Root: root, Case: entry.caseID, Fault: entry.fault}, entry.arm); e == nil {
			t.Fatal("unfrozen config accepted")
		}
	}
}
