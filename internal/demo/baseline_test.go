package demo

import (
	"context"
	"testing"
	"time"
)

func TestBaselineRealProcessesNeverWriteVerdicts(t *testing.T) {
	o := testOptions(t)
	for _, fault := range []string{"none", "wrong-data", "zero-tools"} {
		t.Run(fault, func(t *testing.T) {
			o.Fault = fault
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			r, e := RunBaseline(ctx, o)
			if e != nil {
				t.Fatal(e)
			}
			want := "completed"
			if fault == "zero-tools" {
				want = "failed"
			}
			if r.ExecutionStatus != want || r.WorkflowSuccess != nil || r.WorkflowState != "interrupted" || r.VerdictCount != 0 || !r.SourceUnchanged || !r.Budget.Conserved || r.Parallel < 2 {
				t.Fatalf("invalid baseline status=%s errors=%v workflow=%s budget=%+v", r.ExecutionStatus, r.Errors, r.WorkflowState, r.Budget)
			}
			if r.TraceState != "complete_projection" || len(r.Audit.Models) < 2 {
				t.Fatal("missing actual trace or model ledger")
			}
			if fault == "zero-tools" {
				if len(r.Errors) == 0 || r.Budget.SettledTools != 0 || len(r.Candidates) != 0 {
					t.Fatal("failed budget changed output or disappeared")
				}
			}
			for _, c := range r.Candidates {
				if c.SemanticStatus != "unverified" || c.RunID != r.RunID {
					t.Fatal("candidate authority mismatch")
				}
			}
		})
	}
}
