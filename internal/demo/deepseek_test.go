package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
)

// The server is a finite offline test fixture, not a real model experiment.
func TestDeepSeekBrokerWorkerClosure(t *testing.T) {
	o := testOptions(t)
	o.Case = "development-002"
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 {
			t.Error("bad request")
			w.WriteHeader(400)
			return
		}
		var request mockRequest
		if json.Unmarshal([]byte(wire.Messages[1].Content), &request) != nil {
			t.Error("bad payload")
			w.WriteHeader(400)
			return
		}
		var tools []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(request.Tools), &tools)
		role := "planner"
		p := proposal("analysis")
		candidate := &p
		if len(tools) > 0 {
			role = strings.Split(tools[0].Name, ".")[0]
			candidate = nil
		}
		decision, e := mockDecision(role, request, candidate, "none")
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		sent.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(decision)}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer server.Close()
	client, e := provider.NewLoopback(provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "local-worker.v1", InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}, ContextTokens: 4096, OutputTokens: 1024, MaxPromptBytes: 65536}, server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	o.Model = client
	r, e := Run(context.Background(), o)
	if e != nil || r.State != "succeeded" || r.Provider != "deepseek" || r.Budget.SettledModels != sent.Load() || sent.Load() < 5 || !r.Budget.Conserved {
		t.Fatalf("state=%s error=%s requests=%d budget=%+v err=%v", r.State, r.ErrorKind, sent.Load(), r.Budget, e)
	}
	if r.Parallel < 2 || len(r.Artifacts) < 4 || r.Budget.UnknownModels != 0 {
		t.Fatal("closure incomplete", r)
	}
	t.Logf("development closure: requests=%d workers=%d independent_verdicts=%d state=%s (local_stub only)", sent.Load(), len(r.Workers), len(r.Artifacts), r.State)
}
