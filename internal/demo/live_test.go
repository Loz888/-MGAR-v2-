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
			Maximum  int `json:"max_tokens"`
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
				Schema   string            `json:"schema_version"`
				Kind     string            `json:"final_kind"`
				Goal     string            `json:"task_goal"`
				Output   map[string]any    `json:"output_schema"`
			} `json:"dispatch_contract"`
		}
		if json.Unmarshal(raw, &request) != nil || json.Unmarshal(raw, &envelope) != nil {
			t.Error("dispatch")
			w.WriteHeader(400)
			return
		}
		d := envelope.Dispatch
		if d.Schema != dispatchVersion || d.Kind == "" || d.Goal == "" || d.Output == nil {
			t.Error("missing role-specific dispatch")
			w.WriteHeader(400)
			return
		}
		if d.Plan != nil && d.Kind != "plan" {
			t.Error("planner contract mixed with report")
			w.WriteHeader(400)
			return
		}
		if d.Node == "action" && d.Kind != "tool_observation" {
			t.Error("action must finish with test observation")
			w.WriteHeader(400)
			return
		}
		decide := mockDecision
		if d.Plan == nil && arm != "B4" {
			decide = baselineDecision(d.Version)
			if d.Node == "single" {
				decide = singleDecision(d.Version, d.Business)
			}
		}
		decision, e := decide(d.Node, request, d.Plan, "none")
		if (mode == "planner-business-wrapper" && d.Plan != nil) || (mode == "leaf-business-wrapper" && d.Plan == nil && (d.Node == "research" || d.Node == "data")) {
			decision = []byte(`{"type":"final","output":{"answer":{"decision":"answer","metrics":{},"reason":null,"input_version":1},"citations":[],"semantic_status":"unverified"}}`)
			e = nil
		}
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		finish, output := "stop", 5
		if mode == "bounded" || mode == "aggregate-length" || mode == "aggregate-invalid" {
			minimum, err := minimumReferenceTokens(decision)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			output = minimum
			if mode == "bounded" && d.Node == "aggregate" {
				t.Logf("aggregate output bytes=%d published-vocabulary minimum_tokens=%d max_tokens=%d", len(decision), minimum, wire.Maximum)
			}
			if minimum > wire.Maximum || (mode == "aggregate-length" && d.Node == "aggregate" && request.Turn > 1) {
				finish, output, decision = "length", wire.Maximum, []byte(`{"type":"final","output":{`)
			}
			if mode == "aggregate-invalid" && d.Node == "aggregate" && request.Turn > 1 {
				decision = []byte(`{"broken":`)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017", "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": string(decision)}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": output, "total_tokens": 10 + output}})
	}))
	t.Cleanup(server.Close)
	price := int64(1000)
	if mode == "unaffordable" {
		price = 100_000_000
	}
	proof := provider.OfficialTextInputBound()
	c, e := provider.NewLoopback(provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: provider.TextBoundPriceVersion("development-local.v1"), InputMicrosPerMillion: price, OutputMicrosPerMillion: price}, ContextTokens: 1048576, OutputTokens: 1024, MaxPromptBytes: 48000, InputBound: &proof}, server.URL)
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

func TestLiveDevelopmentRejectsWrongRoleOutput(t *testing.T) {
	for _, mode := range []string{"planner-business-wrapper", "leaf-business-wrapper"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			o.Case = "development-001"
			o.CostMicros = 20833
			o.Model, _ = developmentProvider(t, "B4", mode)
			r, e := RunExperiment(context.Background(), o, "B4")
			if e != nil || r.ExecutionStatus == "completed" || (r.WorkflowSuccess != nil && *r.WorkflowSuccess) {
				t.Fatal("wrong role output accepted", r.ExecutionStatus, e)
			}
			b := r.Result.(Report).Budget
			if !b.Conserved || b.UnknownModels != 0 || b.SettledModels < 1 {
				t.Fatal("known invalid output lost accounting", b)
			}
		})
	}
}
func TestLiveDevelopmentStoppedReadonlyFault(t *testing.T) {
	for _, arm := range []string{"B0", "B1", "B2", "B4"} {
		t.Run(arm, func(t *testing.T) {
			o := testOptions(t)
			o.Case = "development-004"
			o.CostMicros = 20833
			o.Model, _ = developmentProvider(t, arm, "")
			r, e := RunExperiment(context.Background(), o, arm)
			if e != nil || r.ExecutionStatus == "completed" {
				t.Fatal(r.ExecutionStatus, e)
			}
			var b budget.Report
			if arm == "B4" {
				b = r.Result.(Report).Budget
			} else {
				b = r.Result.(BaselineReport).Budget
			}
			if !b.Conserved || b.UnknownTools != 0 || b.PendingTools != 0 || b.UnknownModels != 0 || b.PendingModels != 0 || b.SettledTools < 1 {
				t.Fatal("stopped fault not known failed", b)
			}
		})
	}
}
