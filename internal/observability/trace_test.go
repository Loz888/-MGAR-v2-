package observability

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTraceContextStrictAndStable(t *testing.T) {
	run := "11111111-1111-4111-8111-111111111111"
	task := "22222222-2222-4222-8222-222222222222"
	ctx, e := AgentContext(context.Background(), run, task)
	if e != nil {
		t.Fatal(e)
	}
	carrier := Traceparent(ctx)
	if len(carrier) != 55 || !strings.Contains(carrier, strings.ReplaceAll(run, "-", "")) {
		t.Fatal(carrier)
	}
	parsed, e := Extract(context.Background(), carrier)
	if e != nil || Traceparent(parsed) != carrier {
		t.Fatal(e)
	}
	for _, bad := range []string{"", carrier + " secret", "00-" + strings.Repeat("0", 32) + "-1111111111111111-01", "ff-" + carrier[3:]} {
		if _, e = Extract(context.Background(), bad); e == nil {
			t.Fatal("invalid parent accepted")
		}
	}
	if _, e = AgentContext(context.Background(), "bad", task); e == nil {
		t.Fatal("invalid run")
	}
}

func TestTraceMissingAndPartialAreExplicit(t *testing.T) {
	a := Audit{RunID: "11111111-1111-4111-8111-111111111111", TraceID: "11111111111141118111111111111111", CreatedAt: time.Now()}
	if Assess(a, nil) != "missing" {
		t.Fatal("empty trace")
	}
	a.MissingEventTimes = 1
	if Assess(a, nil) != "incomplete_audit" {
		t.Fatal("missing audit timestamp hidden")
	}
}
