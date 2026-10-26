package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5"
)

func developmentProvider(t *testing.T, arm, mode string) (*provider.Client, *atomic.Int64) {
	t.Helper()
	sent := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		if mode == "unknown" {
			w.WriteHeader(503)
			return
		}
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 {
			t.Error("wire")
			w.WriteHeader(400)
			return
		}
		raw := []byte(wire.Messages[1].Content)
		var request mockRequest
		var envelope struct {
			Dispatch struct {
				Node     string            `json:"node"`
				Business string            `json:"business"`
				Version  int               `json:"input_version"`
				Plan     *planner.Proposal `json:"required_plan"`
			} `json:"dispatch_contract"`
		}
		if json.Unmarshal(raw, &request) != nil || json.Unmarshal(raw, &envelope) != nil {
			t.Error("dispatch")
			w.WriteHeader(400)
			return
		}
		d := envelope.Dispatch
		decide := mockDecision
		if d.Plan == nil && arm != "B4" {
			decide = baselineDecision(d.Version)
			if d.Node == "single" {
				decide = singleDecision(d.Version, d.Business)
			}
		}
		decision, e := decide(d.Node, request, d.Plan, "none")
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(decision)}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	t.Cleanup(server.Close)
	price := int64(1000)
	if mode == "unaffordable" {
		price = 100_000_000
	}
	c, e := provider.NewLoopback(provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "development-local.v1", InputMicrosPerMillion: price, OutputMicrosPerMillion: price}, ContextTokens: 4096, OutputTokens: 1024, MaxPromptBytes: 65536}, server.URL)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c, sent
}
func TestLiveFourArmsDevelopmentClosure(t *testing.T) {
	for _, arm := range []string{"B0", "B1", "B2", "B4"} {
		for _, id := range []string{"development-001", "development-002"} {
			t.Run(arm+"/"+id, func(t *testing.T) {
				o := testOptions(t)
				o.Case = id
				o.CostMicros = 20833
				var sent *atomic.Int64
				o.Model, sent = developmentProvider(t, arm, "")
				r, e := RunExperiment(context.Background(), o, arm)
				if e != nil || r.ExecutionStatus != "completed" || int64(len(r.Models)) != sent.Load() {
					t.Fatalf("status=%s model_requests=%d report=%+v err=%v", r.ExecutionStatus, sent.Load(), r.Result, e)
				}
				if arm == "B4" {
					if r.WorkflowSuccess == nil || !*r.WorkflowSuccess {
						t.Fatal("missing strict verification")
					}
				} else if r.WorkflowSuccess != nil || r.Result.(BaselineReport).VerdictCount != 0 {
					t.Fatal("baseline elevated")
				}
				if r.ExternalOracle["passed"] != true {
					t.Fatal("external execution oracle", r.ExternalOracle)
				}
				root, _ := filepath.Abs("../..")
				raw, e := os.ReadFile(filepath.Join(root, "eval/oracle/v1_truth.json"))
				if e != nil {
					t.Fatal(e)
				}
				var truth map[string]json.RawMessage
				_ = json.Unmarshal(raw, &truth)
				var expected any
				_ = json.Unmarshal(truth[id], &expected)
				got, _ := json.Marshal(r.Answer)
				want, _ := json.Marshal(expected)
				if string(got) != string(want) {
					t.Fatal("independent truth mismatch")
				}
				for _, raw := range r.Models {
					var call struct {
						Provider string `json:"provider"`
						Source   string `json:"usage_source"`
					}
					_ = json.Unmarshal(raw, &call)
					if call.Provider != "deepseek" || call.Source != "local_stub" {
						t.Fatal("false live evidence")
					}
				}
				t.Logf("development case=%s arm=%s physical_local_requests=%d external_oracle=true strict=%t", id, arm, sent.Load(), arm == "B4")
			})
		}
	}
}
func TestLiveDevelopmentUnknownBudgetAndScope(t *testing.T) {
	for _, mode := range []string{"unknown", "unaffordable", "holdout", "extra_arm"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			o.Case = "development-002"
			o.CostMicros = 20833
			var sent *atomic.Int64
			o.Model, sent = developmentProvider(t, "B4", mode)
			arm := "B4"
			if mode == "holdout" {
				o.Case = "live-001"
			}
			if mode == "extra_arm" {
				arm = "B3"
			}
			r, e := RunExperiment(context.Background(), o, arm)
			if mode == "holdout" || mode == "extra_arm" {
				if e == nil || sent.Load() != 0 {
					t.Fatal("scope bypass")
				}
				return
			}
			if e != nil || r.ExecutionStatus == "completed" {
				t.Fatal(r, e)
			}
			b := r.Result.(Report).Budget
			if mode == "unknown" {
				if sent.Load() != 1 || b.UnknownModels != 1 || b.ReservedCostMicros == 0 {
					t.Fatal(sent.Load(), b)
				}
			} else if sent.Load() != 0 || b.ReservedModels != 0 || b.SettledModels != 0 {
				t.Fatal("sent without budget", sent.Load(), b)
			}
		})
	}
}

func TestLiveDevelopmentProviderReplan(t *testing.T) {
	for _, arm := range []string{"B2", "B4"} {
		t.Run(arm, func(t *testing.T) {
			o := testOptions(t)
			o.Case = "development-006"
			o.CostMicros = 20833
			var sent *atomic.Int64
			o.Model, sent = developmentProvider(t, arm, "")
			r, e := RunExperiment(context.Background(), o, arm)
			if e != nil || r.ExecutionStatus != "completed" || len(r.InputTransitions) < 2 || int64(len(r.Models)) != sent.Load() {
				t.Fatalf("status=%s result=%+v err=%v", r.ExecutionStatus, r.Result, e)
			}
			if arm == "B4" && (len(r.Replans) != 1 || !r.Replans[0].Accepted) {
				t.Fatal("missing strict replan")
			}
			for _, raw := range r.Models {
				var call map[string]any
				_ = json.Unmarshal(raw, &call)
				if call["provider"] != "deepseek" {
					t.Fatal("replan silently used mock")
				}
			}
			t.Logf("development replan arm=%s calls=%d transitions=%v", arm, sent.Load(), r.InputTransitions)
		})
	}
}
func TestLiveRetainsDurableStateAfterClosing(t *testing.T) {
	o := testOptions(t)
	f, e := newFixture(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	f.retain = true
	schema, dir := f.schema, f.temp
	t.Cleanup(func() {
		s, e := store.Open(context.Background(), os.Getenv("MGAR_TEST_DATABASE_URL"))
		if e != nil {
			t.Error(e)
			return
		}
		defer s.Pool.Close()
		if _, e = s.Pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error(e)
		}
		_ = os.RemoveAll(dir)
	})
	if e = f.close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "cas")); e != nil {
		t.Fatal("CAS removed", e)
	}
	s, e := store.Open(context.Background(), os.Getenv("MGAR_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Pool.Close()
	var exists bool
	if e = s.Pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name='model_calls')", schema).Scan(&exists); e != nil || !exists {
		t.Fatal("ledger removed", e)
	}
}
