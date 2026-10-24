// Package observability projects committed audit records; telemetry grants no authority.
package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"regexp"
	"strings"
)

const SchemaURL = "https://opentelemetry.io/schemas/1.37.0"

var ErrTrace = errors.New("INVALID_TRACE_CONTEXT")
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var parentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

func SpanID(run, kind, id string) string {
	sum := sha256.Sum256([]byte(run + "\x00" + kind + "\x00" + id))
	return hex.EncodeToString(sum[:8])
}
func AgentContext(ctx context.Context, run, task string) (context.Context, error) {
	if !uuid.MatchString(run) || !uuid.MatchString(task) {
		return ctx, ErrTrace
	}
	return Extract(ctx, "00-"+strings.ReplaceAll(run, "-", "")+"-"+SpanID(run, "agent", task)+"-01")
}
func Traceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}
func Extract(ctx context.Context, parent string) (context.Context, error) {
	if !parentPattern.MatchString(parent) {
		return ctx, ErrTrace
	}
	result := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent})
	if !trace.SpanContextFromContext(result).IsValid() {
		return ctx, ErrTrace
	}
	return result, nil
}
