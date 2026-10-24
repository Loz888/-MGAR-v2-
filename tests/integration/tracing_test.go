package integration

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/observability"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type traceSink struct {
	mu         sync.Mutex
	spans      []*tracepb.Span
	raw        []byte
	badHeaders bool
}

func (s *traceSink) handler(w http.ResponseWriter, r *http.Request) {
	body, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var msg collector.ExportTraceServiceRequest
	if e != nil || r.URL.Path != "/v1/traces" || proto.Unmarshal(body, &msg) != nil {
		http.Error(w, "INVALID_OTLP", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, body...)
	s.badHeaders = s.badHeaders || r.Header.Get("Authorization") != "" || r.Header.Get("Baggage") != ""
	for _, resource := range msg.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				s.spans = append(s.spans, span)
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(200)
}
func (s *traceSink) snapshot() ([]*tracepb.Span, []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*tracepb.Span{}, s.spans...), append([]byte{}, s.raw...), s.badHeaders
}
func TestTraceOTLPAuditHierarchyAndPrivacy(t *testing.T) {
	s, run, oc, cs, cas := budgetFixture(t, 2)
	ctx := context.Background()
	canary := "PRIVATE_SYNTHETIC_CONTENT"
	if _, e := s.Pool.Exec(ctx, "UPDATE workflow_runs SET goal=$2 WHERE run_id=$1", run, canary); e != nil {
		t.Fatal(e)
	}
	req := budgetRequest(0)
	req.Model = canary
	req.Pricing.Model = canary
	call, e := s.ReserveModelCall(ctx, cs[0], req)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishModelCall(ctx, cs[0], call.ID, budget.ModelUsage{Known: true, Status: "succeeded", Source: "local_stub", InputTokens: 10, OutputTokens: 5, Tokens: 15, CostMicros: 15, PricingVersion: req.Pricing.Version}); e != nil {
		t.Fatal(e)
	}
	tool := store.ToolRequest{ID: budgetRequest(1).LogicalID, Capability: canary, Version: "test.v1", Resource: canary, Hash: artifact.Digest([]byte(canary)), Effect: "readonly"}
	if _, e = s.BeginTool(ctx, cs[1], tool); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishTool(ctx, cs[1], tool, cas, []byte(`{"text":"PRIVATE_SYNTHETIC_CONTENT"}`), "succeeded"); e != nil {
		t.Fatal(e)
	}
	pending, e := s.PendingHandoffs(ctx, run, handoff.SchedulerConsumer, 10)
	if e != nil || len(pending) == 0 {
		t.Fatal(e)
	}
	if _, e = s.ConsumeHandoff(ctx, run, oc, handoff.SchedulerConsumer, pending[0].ID); e != nil {
		t.Fatal(e)
	}
	audit, e := observability.ReadAudit(ctx, s, run)
	if e != nil || len(audit.Models) != 1 || len(audit.Handoffs) != 1 {
		t.Fatal(audit, e)
	}
	if observability.Assess(audit, nil) != "missing" {
		t.Fatal("empty trace not missing")
	}
	var before int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", run).Scan(&before); e != nil {
		t.Fatal(e)
	}
	sink := &traceSink{}
	server := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=PRIVATE_SYNTHETIC_CONTENT")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "sensitive=PRIVATE_SYNTHETIC_CONTENT")
	count, e := observability.ExportLocal(ctx, audit, server.URL)
	if e != nil {
		t.Fatal(e)
	}
	spans, raw, bad := sink.snapshot()
	if count != len(spans) || bad || strings.Contains(string(raw), canary) {
		t.Fatal("telemetry privacy or count failed")
	}
	ids := []string{}
	seen := map[string]bool{}
	operations := map[string]bool{}
	for _, span := range spans {
		if hex.EncodeToString(span.TraceId) != audit.TraceID {
			t.Fatal("trace split")
		}
		id := hex.EncodeToString(span.SpanId)
		if seen[id] {
			t.Fatal("duplicate span ID")
		}
		seen[id] = true
		ids = append(ids, id)
		for _, attr := range span.Attributes {
			if attr.Key == "gen_ai.operation.name" {
				operations[attr.Value.GetStringValue()] = true
			}
		}
	}
	for _, span := range spans {
		if len(span.ParentSpanId) > 0 && !seen[hex.EncodeToString(span.ParentSpanId)] {
			t.Fatal("orphaned span")
		}
	}
	if !operations["invoke_agent"] || !operations["chat"] || !operations["execute_tool"] || observability.Assess(audit, ids) != "complete_projection" || observability.Assess(audit, ids[:1]) != "partial" {
		t.Fatal(operations)
	}
	var after int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", run).Scan(&after); e != nil || after != before {
		t.Fatal("export changed audit")
	}
	for _, endpoint := range []string{"http://example.org:4318", "http://localhost:4318", "http://user:pass@127.0.0.1:4318", "http://127.0.0.1:4318?token=value"} {
		if _, e = observability.ExportLocal(ctx, audit, endpoint); e == nil {
			t.Fatal("unsafe collector")
		}
	}
	// HTTP failures cannot be represented as acknowledged spans.
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "synthetic", 503) }))
	defer failed.Close()
	if n, e := observability.ExportLocal(ctx, audit, failed.URL); e == nil || n != 0 {
		t.Fatal("false export success")
	}
}
func TestTraceVerifyEventHasTaskParent(t *testing.T) {
	s, run, oc, cas, evidence, requirement := verifierFixture(t)
	ctx := context.Background()
	if _, e := s.VerifyArtifact(ctx, run, oc, evidence.ArtifactID, verifierRaw(t, evidence), requirement, cas, verifier.Engine{}); e != nil {
		t.Fatal(e)
	}
	a, e := observability.ReadAudit(ctx, s, run)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, event := range a.Events {
		if event.Kind == "verdict_recorded" {
			found = true
			if event.TaskID != evidence.TaskID {
				t.Fatal("verify detached")
			}
		}
	}
	if !found {
		t.Fatal("verdict absent")
	}
}
func TestTraceCrossLanguageProcessContext(t *testing.T) {
	run := "11111111-1111-4111-8111-111111111111"
	task := "22222222-2222-4222-8222-222222222222"
	ctx, e := observability.AgentContext(context.Background(), run, task)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("python", "-c", "import sys; from mgar_agents.core.telemetry import TraceContext; print(TraceContext.for_agent(sys.argv[1],sys.argv[2]).traceparent)", run, task)
	output, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(string(output)) != observability.Traceparent(ctx) {
		t.Fatal("cross-process parent mismatch")
	}
}
func TestTraceCrashProcess(t *testing.T) {
	if os.Getenv("MGAR_TRACE_CRASH") != "1" {
		return
	}
	s := attachedStore(t)
	var c store.TaskClaim
	if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &c); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReserveModelCall(context.Background(), c, budgetRequest(0)); e != nil {
		t.Fatal(e)
	}
	os.Stdout.WriteString("COMMITTED\n")
	if _, e := bufio.NewReader(os.Stdin).ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	t.Fatal("parent must kill before export")
}
func TestTraceKilledProcessRebuildsCommittedAudit(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	claim, _ := json.Marshal(cs[0])
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestTraceCrashProcess$")
	cmd.Env = append(os.Environ(), "MGAR_TRACE_CRASH=1", "MGAR_TEST_CLAIM="+string(claim), "MGAR_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer stdin.Close()
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "COMMITTED" {
		t.Fatal("commit barrier absent")
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Wait(); e == nil {
		t.Fatal("process was not killed")
	}
	// Controlled lease expiration after confirmed OS-process death.
	if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", cs[0].TaskID); e != nil {
		t.Fatal(e)
	}
	recovered, e := s.ReconcileCalls(ctx, run, oc.Owner, oc.Epoch, oc.Revision)
	if e != nil || recovered.Unknown != 1 {
		t.Fatal(recovered, e)
	}
	a, e := observability.ReadAudit(ctx, s, run)
	if e != nil || len(a.Models) != 1 || a.Models[0].Known || a.Models[0].Status != "unknown" || observability.Assess(a, nil) != "missing" {
		t.Fatal(a, e)
	}
	sink := &traceSink{}
	server := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer server.Close()
	if _, e = observability.ExportLocal(ctx, a, server.URL); e != nil {
		t.Fatal(e)
	}
	spans, _, _ := sink.snapshot()
	found := false
	for _, span := range spans {
		if span.Name == "mgar.model_unknown" {
			found = true
		}
	}
	if !found {
		t.Fatal("committed unknown event not reconstructed")
	}
	t.Log("owned process killed after PG commit and before export; original live spans missing; committed audit rebuilt via OTLP")
}

func TestTraceExportProcess(t *testing.T) {
	if os.Getenv("MGAR_TRACE_EXPORT") != "1" {
		return
	}
	s := attachedStore(t)
	var c store.TaskClaim
	if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &c); e != nil {
		t.Fatal(e)
	}
	ctx, e := observability.Extract(context.Background(), os.Getenv("MGAR_TRACE_PARENT"))
	if e != nil {
		t.Fatal(e)
	}
	expected, e := observability.AgentContext(context.Background(), c.RunID, c.TaskID)
	if e != nil || observability.Traceparent(expected) != observability.Traceparent(ctx) {
		t.Fatal("unbound parent")
	}
	a, e := observability.ReadAudit(ctx, s, c.RunID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = observability.ExportLocal(ctx, a, os.Getenv("MGAR_TRACE_ENDPOINT")); e != nil {
		t.Fatal(e)
	}
}
func TestTraceAnotherOSProcessExportsSameAgentParent(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	claim, _ := json.Marshal(cs[0])
	parent, e := observability.AgentContext(ctx, run, cs[0].TaskID)
	if e != nil {
		t.Fatal(e)
	}
	sink := &traceSink{}
	server := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer server.Close()
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestTraceExportProcess$")
	cmd.Env = append(os.Environ(), "MGAR_TRACE_EXPORT=1", "MGAR_TRACE_PARENT="+observability.Traceparent(parent), "MGAR_TEST_CLAIM="+string(claim), "MGAR_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"], "MGAR_TRACE_ENDPOINT="+server.URL)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("child export: %s %v", output, e)
	}
	spans, _, _ := sink.snapshot()
	found := false
	for _, span := range spans {
		if span.Name == "invoke_agent" {
			found = true
			if hex.EncodeToString(span.SpanId) != strings.Split(observability.Traceparent(parent), "-")[2] {
				t.Fatal("parent link changed across processes")
			}
		}
	}
	if !found {
		t.Fatal("child did not export agent")
	}
}
