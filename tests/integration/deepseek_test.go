package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/google/uuid"
)

func deepseekConfig() provider.Config {
	return provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "synthetic-local.v1", InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}, ContextTokens: 60, OutputTokens: 40, MaxPromptBytes: 4096}
}
func deepseekBody() string {
	return `{"model":"flash-1017","choices":[{"finish_reason":"stop","message":{"content":"{\"type\":\"final\",\"output\":{\"ok\":true}}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
}
func TestDeepSeekDurableTransport(t *testing.T) {
	for _, mode := range []string{"success", "429", "503", "redirect", "missing_usage", "invalid_usage", "invalid_content", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			s, run, _, claims, _ := budgetFixture(t, 2)
			ctx := context.Background()
			var sent atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent.Add(1)
				var count int
				if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM model_calls WHERE run_id=$1 AND status='reserved'", run).Scan(&count); e != nil || count != 1 {
					t.Error("send before durable reserve", count, e)
				}
				raw, _ := io.ReadAll(r.Body)
				var wire map[string]any
				_ = json.Unmarshal(raw, &wire)
				thinking, _ := wire["thinking"].(map[string]any)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer loopback-test-only" || wire["model"] != "deepseek-flash" || thinking["type"] != "disabled" || wire["temperature"] != float64(0) || wire["max_tokens"] != float64(40) {
					t.Error("invalid transport")
				}
				body := deepseekBody()
				switch mode {
				case "429":
					w.WriteHeader(429)
				case "503":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "https://api.deepseek.com/chat/completions")
					w.WriteHeader(307)
				case "missing_usage":
					body = `{"model":"flash-1017","choices":[]}`
				case "invalid_usage":
					body = strings.Replace(body, `"prompt_tokens":10`, `"prompt_tokens":-1`, 1)
				case "invalid_content":
					body = strings.Replace(body, `{\"type\":\"final\",\"output\":{\"ok\":true}}`, `not json`, 1)
				case "oversized":
					body = strings.Repeat("x", (1<<20)+1)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			config := deepseekConfig()
			client, e := provider.NewLoopback(config, server.URL)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			config.Price.InputMicrosPerMillion = 999_000_000 // caller mutation must not rewrite frozen quote
			resources, e := scheduler.NewResources(ctx, s)
			if e != nil {
				t.Fatal(e)
			}
			id := uuid.NewString()
			out, call, e := client.Call(ctx, resources, claims[0], id, []byte(`{"goal":"synthetic"}`))
			if call.ID == "" || sent.Load() != 1 {
				t.Fatal("missing ledger/send", call.ID, sent.Load(), e)
			}
			if (mode == "success") != (e == nil) {
				t.Fatal("wrong result", mode, e)
			}
			if mode == "success" && !json.Valid(out) {
				t.Fatal("invalid decision")
			}
			if e != nil && strings.Contains(e.Error(), "loopback-test-only") {
				t.Fatal("credential leaked")
			}
			report, e := s.BudgetReport(ctx, run)
			if e != nil || !report.Conserved {
				t.Fatal(report, e)
			}
			if mode == "success" || mode == "invalid_content" {
				if report.SettledModels != 1 || report.SettledCostMicros != 15 || report.ReservedModels != 0 {
					t.Fatal(report)
				}
			} else if report.UnknownModels != 1 || report.ReservedCostMicros != 100 || report.SettledModels != 0 {
				t.Fatal("unknown released", report)
			}
			if _, _, e = client.Call(ctx, resources, claims[0], id, []byte(`{}`)); e == nil || sent.Load() != 1 {
				t.Fatal("replayed", e, sent.Load())
			}
			if mode != "success" && mode != "invalid_content" {
				if _, _, e = client.Call(ctx, resources, claims[0], uuid.NewString(), []byte(`{}`)); e == nil || sent.Load() != 1 {
					t.Fatal("unknown new logical call replayed")
				}
			}
			if e = s.FinishModelCall(ctx, claims[1], call.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e == nil {
				t.Fatal("cross-task settlement")
			}
			var audit string
			if e = s.Pool.QueryRow(ctx, "SELECT coalesce(jsonb_agg(payload),'[]')::text FROM workflow_events WHERE run_id=$1", run).Scan(&audit); e != nil || strings.Contains(audit, "loopback-test-only") {
				t.Fatal("audit leaked", e)
			}
			t.Logf("mode=%s physical_requests=%d known=%d unknown=%d reserved_micro_usd=%d settled_micro_usd=%d", mode, sent.Load(), report.SettledModels, report.UnknownModels, report.ReservedCostMicros, report.SettledCostMicros)
		})
	}
}
func TestDeepSeekConcurrentLastQuotaAndCrossRun(t *testing.T) {
	s, run, owner, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	if e := s.ConfigureBudget(ctx, run, owner, budget.Limits{ModelCalls: 1, ToolCalls: 1, Tokens: 100, CostMicros: 100, ConcurrentCalls: 2, TaskConcurrency: 2, Delegations: 24}); e != nil {
		t.Fatal(e)
	}
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1); _, _ = io.WriteString(w, deepseekBody()) }))
	defer server.Close()
	client, e := provider.NewLoopback(deepseekConfig(), server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	forged := cs[0]
	forged.RunID = uuid.NewString()
	if _, _, e = client.Call(ctx, resources, forged, uuid.NewString(), []byte(`{}`)); e == nil || sent.Load() != 0 {
		t.Fatal("cross-run sent")
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	var wins atomic.Int64
	for _, c := range cs {
		wg.Add(1)
		go func(c store.TaskClaim) {
			defer wg.Done()
			<-start
			if _, _, e := client.Call(ctx, resources, c, uuid.NewString(), []byte(`{}`)); e == nil {
				wins.Add(1)
			}
		}(c)
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || sent.Load() != 1 {
		t.Fatal(wins.Load(), sent.Load())
	}
	r, e := s.BudgetReport(ctx, run)
	if e != nil || !r.Conserved || r.SettledModels != 1 {
		t.Fatal(r, e)
	}
	t.Log(fmt.Sprintf("last quota: sent=%d winners=%d", sent.Load(), wins.Load()))
}
