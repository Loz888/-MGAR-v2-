package chaos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/google/uuid"
)

func deepSeekChild(t *testing.T, s *store.Store, mode string, send, pause func(barrier)) bool {
	if mode != "deepseek-send" {
		return false
	}
	var claim store.TaskClaim
	if e := json.Unmarshal([]byte(os.Getenv("MGAR_CHAOS_CLAIM")), &claim); e != nil {
		t.Fatal(e)
	}
	cfg := provider.Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "local-chaos.v1", InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}, ContextTokens: 40, OutputTokens: 24, MaxPromptBytes: 1024}
	client, e := provider.NewLoopback(cfg, os.Getenv("MGAR_CHAOS_HTTP"))
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	resources, e := scheduler.NewResources(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	pause(barrier{Point: "before_provider"})
	_, call, e := client.Call(context.Background(), resources, claim, uuid.NewString(), []byte(`{"goal":"synthetic"}`))
	result := "denied"
	if e == nil {
		result = "settled"
	}
	send(barrier{Point: result, CallID: call.ID})
	return true
}
func TestDeepSeekPhysicalCallSIGKILL(t *testing.T) {
	s, run, owner := modelFixture(t)
	ctx := context.Background()
	c, _ := claimModel(t, s, run, time.Second)
	raw, _ := json.Marshal(c)
	t.Setenv("MGAR_CHAOS_CLAIM", string(raw))
	var sent atomic.Int64
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	t.Setenv("MGAR_CHAOS_HTTP", server.URL)
	child := startChaos(t, s, run, "deepseek-send")
	if child.read(t).Point != "before_provider" {
		t.Fatal("barrier")
	}
	_, _ = io.WriteString(child.in, "SEND\n")
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("no physical request")
	}
	child.kill(t)
	var status string
	if e := s.Pool.QueryRow(ctx, "SELECT status FROM model_calls WHERE run_id=$1", run).Scan(&status); e != nil || status != "reserved" {
		t.Fatal(status, e)
	}
	awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID)
	if _, e := s.ReconcileCalls(ctx, run, owner.Owner, owner.Epoch, owner.Revision); e != nil {
		t.Fatal(e)
	}
	report, e := s.BudgetReport(ctx, run)
	if e != nil || !report.Conserved || report.UnknownModels != 1 || report.ReservedCostMicros != 64 || report.SettledModels != 0 {
		t.Fatal(report, e)
	}
	again := startChaos(t, s, run, "deepseek-send")
	if again.read(t).Point != "before_provider" {
		t.Fatal("barrier")
	}
	_, _ = io.WriteString(again.in, "SEND\n")
	if again.read(t).Point != "denied" {
		t.Fatal("replayed")
	}
	again.finish(t)
	if sent.Load() != 1 {
		t.Fatal("duplicate physical requests", sent.Load())
	}
	t.Logf("actual DeepSeek transport (loopback): SIGKILL, unknown retained=64 microUSD, requests=%d", sent.Load())
}
func TestDeepSeekTwoProcessesLastQuota(t *testing.T) {
	s, run, _ := modelFixture(t)
	c, _ := claimModel(t, s, run, time.Minute)
	raw, _ := json.Marshal(c)
	t.Setenv("MGAR_CHAOS_CLAIM", string(raw))
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		_, _ = io.WriteString(w, `{"model":"flash-1017","choices":[{"finish_reason":"stop","message":{"content":"{\"type\":\"final\",\"output\":{}}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()
	t.Setenv("MGAR_CHAOS_HTTP", server.URL)
	a := startChaos(t, s, run, "deepseek-send")
	b := startChaos(t, s, run, "deepseek-send")
	for _, c := range []*chaosChild{a, b} {
		if c.read(t).Point != "before_provider" {
			t.Fatal("barrier")
		}
	}
	for _, c := range []*chaosChild{a, b} {
		_, _ = io.WriteString(c.in, "SEND\n")
	}
	outcomes := map[string]int{}
	for _, c := range []*chaosChild{a, b} {
		outcomes[c.read(t).Point]++
		c.finish(t)
	}
	if outcomes["settled"] != 1 || outcomes["denied"] != 1 || sent.Load() != 1 {
		t.Fatal(outcomes, sent.Load())
	}
	t.Logf("2 OS processes, durable last quota: %+v physical_requests=%d", outcomes, sent.Load())
}
