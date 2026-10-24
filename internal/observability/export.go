package observability

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type spec struct {
	id, parent, name string
	at               time.Time
	attrs            []attribute.KeyValue
}
type ids struct {
	tid trace.TraceID
	sid trace.SpanID
}
type idKey struct{}
type stableIDs struct{}

func (stableIDs) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	v := ctx.Value(idKey{}).(ids)
	return v.tid, v.sid
}
func (stableIDs) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	return ctx.Value(idKey{}).(ids).sid
}

// The SDK merges OTEL_RESOURCE_ATTRIBUTES even with WithResource. Filter again
// at the final export boundary, without mutating process-global environment.
type safeSpan struct{ sdktrace.ReadOnlySpan }

func (s safeSpan) Resource() *resource.Resource {
	return resource.NewSchemaless(semconv.ServiceName("mgar-audit"))
}

type capture struct{ spans []sdktrace.ReadOnlySpan }

func (c *capture) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (c *capture) OnEnd(s sdktrace.ReadOnlySpan)                   { c.spans = append(c.spans, safeSpan{s}) }
func (c *capture) Shutdown(context.Context) error                  { return nil }
func (c *capture) ForceFlush(context.Context) error                { return nil }

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func specs(a Audit) ([]spec, error) {
	if !uuid.MatchString(a.RunID) || a.TraceID != strings.ReplaceAll(a.RunID, "-", "") || a.Truncated || a.CreatedAt.IsZero() {
		return nil, ErrTrace
	}
	root := SpanID(a.RunID, "workflow", a.RunID)
	base := []attribute.KeyValue{attribute.String("mgar.run.id", a.RunID), attribute.Bool("mgar.reconstructed", true), attribute.String("mgar.source", "postgresql_committed_audit"), attribute.String("mgar.duration.kind", "audit_record_instant")}
	out := []spec{{root, "", "mgar.workflow.audit", a.CreatedAt, base}}
	tasks := map[string]bool{}
	for _, t := range a.Tasks {
		if !uuid.MatchString(t.ID) {
			return nil, ErrTrace
		}
		tasks[t.ID] = true
		taskID := SpanID(a.RunID, "task", t.ID)
		agentID := SpanID(a.RunID, "agent", t.ID)
		attrs := append([]attribute.KeyValue{attribute.String("mgar.task.id", t.ID), attribute.Int("mgar.plan.version", t.Version), attribute.Int64("mgar.task.epoch", t.Epoch)}, base...)
		out = append(out, spec{taskID, root, "mgar.task.audit", a.CreatedAt, attrs})
		// These are correlation anchors, explicitly not evidence of execution.
		role := "unrecognized"
		switch t.Role {
		case "research", "data", "action", "verify", "aggregate":
			role = t.Role
		}
		attrs = append(append([]attribute.KeyValue{}, attrs...), semconv.GenAIOperationNameKey.String("invoke_agent"), semconv.GenAIAgentIDKey.String(t.ID), attribute.String("mgar.agent.role", role), attribute.Bool("mgar.correlation_anchor", true))
		out = append(out, spec{agentID, taskID, "invoke_agent", a.CreatedAt, attrs})
	}
	for _, e := range a.Events {
		if !uuid.MatchString(e.ID) || !uuid.MatchString(e.EntityID) {
			return nil, ErrTrace
		}
		if e.At == nil {
			continue
		}
		parent := root
		if e.TaskID != "" {
			if !tasks[e.TaskID] {
				return nil, ErrTrace
			}
			parent = SpanID(a.RunID, "agent", e.TaskID)
		}
		attrs := append([]attribute.KeyValue{attribute.String("mgar.event.id", e.ID), attribute.Int64("mgar.entity.revision", e.Revision), attribute.Int("mgar.plan.version", e.PlanVersion), attribute.Bool("mgar.trace_id.matches_run", e.TraceMatches)}, base...)
		kind := safeKind(e.Kind)
		entity := safeEntity(e.Entity)
		if entity == "model_call" {
			attrs = append(attrs, semconv.GenAIOperationNameKey.String("chat"))
		}
		if entity == "tool_invocation" {
			attrs = append(attrs, semconv.GenAIOperationNameKey.String("execute_tool"), attribute.String("mgar.tool.invocation_id", e.EntityID))
		}
		if entity == "verdict" {
			attrs = append(attrs, attribute.String("mgar.stage", "verify"))
		}
		out = append(out, spec{SpanID(a.RunID, "event", e.ID), parent, "mgar." + kind, *e.At, attrs})
	}
	for _, m := range a.Models {
		if !uuid.MatchString(m.ID) || !tasks[m.TaskID] || !hashPattern.MatchString(m.ModelDigest) {
			return nil, ErrTrace
		}
		attrs := append([]attribute.KeyValue{semconv.GenAIOperationNameKey.String("chat"), attribute.String("mgar.model.digest", m.ModelDigest), attribute.Bool("mgar.usage.known", m.Known), attribute.Int64("mgar.reservation.tokens", m.ReservedTokens), attribute.Int64("mgar.reservation.cost_micros", m.ReservedCostMicros), attribute.String("mgar.ledger.view", "snapshot_not_historical_event")}, base...)
		if m.Provider == "mock" {
			attrs = append(attrs, semconv.GenAIProviderNameKey.String("mock"))
		}
		if m.Known {
			if m.Tokens != nil {
				attrs = append(attrs, attribute.Int64("mgar.usage.tokens", *m.Tokens))
			}
			if m.CostMicros != nil {
				attrs = append(attrs, attribute.Int64("mgar.cost_micros", *m.CostMicros))
			}
			if m.InputTokens != nil {
				attrs = append(attrs, semconv.GenAIUsageInputTokensKey.Int64(*m.InputTokens))
			}
			if m.OutputTokens != nil {
				attrs = append(attrs, semconv.GenAIUsageOutputTokensKey.Int64(*m.OutputTokens))
			}
		}
		out = append(out, spec{SpanID(a.RunID, "model_ledger", m.ID), SpanID(a.RunID, "agent", m.TaskID), "chat.ledger", a.SnapshotAt, attrs})
	}
	for _, h := range a.Handoffs {
		if !uuid.MatchString(h.EventID) || !hashPattern.MatchString(h.ConsumerDigest) || (h.State != "received" && h.State != "processed") {
			return nil, ErrTrace
		}
		out = append(out, spec{SpanID(a.RunID, "handoff", h.EventID+h.ConsumerDigest), SpanID(a.RunID, "event", h.EventID), "mgar.handoff", h.At, append([]attribute.KeyValue{attribute.String("mgar.handoff.state", h.State)}, base...)})
	}
	return out, nil
}

// Assess compares actually observed span IDs, without changing authoritative DB history.
func Assess(a Audit, observed []string) string {
	expected, e := specs(a)
	if e != nil || a.MissingEventTimes > 0 {
		return "incomplete_audit"
	}
	if len(observed) == 0 {
		return "missing"
	}
	seen := map[string]bool{}
	for _, id := range observed {
		seen[id] = true
	}
	n := 0
	for _, s := range expected {
		if seen[s.id] {
			n++
		}
	}
	if n == len(expected) {
		return "complete_projection"
	}
	if n == 0 {
		return "missing"
	}
	return "partial"
}

// ExportLocal performs one bounded OTLP request. Acknowledgement is not durable
// backend retention; failures neither modify DB history nor authorize a retry of work.
func ExportLocal(ctx context.Context, a Audit, endpoint string) (int, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return 0, ErrTrace
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return 0, ErrTrace
	}
	items, e := specs(a)
	if e != nil {
		return 0, e
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OTLP_REDIRECT_DENIED") }}
	exp, e := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithInsecure(), otlptracehttp.WithURLPath("/v1/traces"), otlptracehttp.WithHeaders(map[string]string{}), otlptracehttp.WithCompression(otlptracehttp.NoCompression), otlptracehttp.WithHTTPClient(client), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if e != nil {
		return 0, errors.New("OTLP_INITIALIZATION_FAILED")
	}
	defer exp.Shutdown(context.WithoutCancel(ctx))
	collector := &capture{}
	provider := sdktrace.NewTracerProvider(sdktrace.WithIDGenerator(stableIDs{}), sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName("mgar-audit"))), sdktrace.WithSpanProcessor(collector))
	defer provider.Shutdown(context.WithoutCancel(ctx))
	tracer := provider.Tracer("mgar.audit", trace.WithInstrumentationVersion("1.0.0"), trace.WithSchemaURL(SchemaURL))
	tid, e := trace.TraceIDFromHex(a.TraceID)
	if e != nil || !tid.IsValid() {
		return 0, ErrTrace
	}
	for _, item := range items {
		sid, _ := trace.SpanIDFromHex(item.id)
		parent := trace.SpanContext{}
		if item.parent != "" {
			pid, _ := trace.SpanIDFromHex(item.parent)
			parent = trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: pid, TraceFlags: trace.FlagsSampled, Remote: true})
		}
		c := trace.ContextWithSpanContext(ctx, parent)
		c = context.WithValue(c, idKey{}, ids{tid, sid})
		_, span := tracer.Start(c, item.name, trace.WithTimestamp(item.at), trace.WithAttributes(item.attrs...))
		span.End(trace.WithTimestamp(item.at))
	}
	if e = exp.ExportSpans(ctx, collector.spans); e != nil {
		return 0, errors.New("OTLP_EXPORT_FAILED")
	}
	return len(collector.spans), nil
}
