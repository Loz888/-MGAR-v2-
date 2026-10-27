package demo

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

type mergeReceipt struct {
	Schema string `json:"schema_version"`
	Hash   string `json:"merge_sha256"`
}

var errMergeReceipt = errors.New("aggregate_receipt_rejected")
var errMergeDecision = errors.New("aggregate_receipt_invalid")

func receiptFor(body []byte) []byte {
	return inputcontext.Canonical(mergeReceipt{Schema: "merge-receipt.v1", Hash: artifact.Digest(body)})
}

// The model only echoes a small receipt. Full content/provenance remains in
// trusted memory and is published only after same-claim durable success + CAS
// checks. Returning these bytes does not replace the subsequent Verifier.
func (f *fixture) resolveAggregate(ctx context.Context, c store.TaskClaim, state *taskTools, output []byte) ([]byte, error) {
	if c.Role != "aggregate" {
		return nil, errMergeReceipt
	}
	state.mu.Lock()
	body, executed := bytes.Clone(state.expected), state.executed
	state.mu.Unlock()
	var merged handoff.MergeResult
	if executed != 1 || len(body) == 0 || len(body) > 60000 || verifier.StrictJSON(body, &merged) != nil || merged.SchemaVersion != "merge_result.v1" || !merged.CanFinalize {
		return nil, errMergeReceipt
	}
	if f.s.ValidateToolClaim(ctx, c) != nil {
		return nil, errMergeReceipt
	}
	b, e := f.s.BudgetReport(ctx, c.RunID)
	if e != nil || !b.Conserved || b.PendingModels+b.UnknownModels+b.PendingTools+b.UnknownTools != 0 {
		return nil, errMergeReceipt
	}
	var pending bool
	if f.s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM resource_requests WHERE task_id=$1 AND state IN ('queued','held','unknown'))", c.TaskID).Scan(&pending) != nil || pending {
		return nil, errMergeReceipt
	}
	rows, e := f.s.Pool.Query(ctx, `SELECT invocation_id,request_hash FROM tool_invocations WHERE task_id=$1 AND capability='aggregate.merge' AND status='succeeded' AND scope->>'Owner'=$2 AND scope->>'Epoch'=$3 AND scope->>'Plan'=$4 ORDER BY invocation_id LIMIT 65`, c.TaskID, c.Owner, strconv.FormatInt(c.Epoch, 10), strconv.Itoa(c.PlanVersion))
	if e != nil {
		return nil, errMergeReceipt
	}
	requests := []store.ToolRequest{}
	for rows.Next() {
		r := store.ToolRequest{Capability: "aggregate.merge", Version: "demo.v1", Resource: "fixture", Effect: "readonly"}
		if rows.Scan(&r.ID, &r.Hash) != nil {
			rows.Close()
			return nil, errMergeReceipt
		}
		requests = append(requests, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(requests) != 1 {
		return nil, errMergeReceipt
	}
	observed, e := f.s.ToolResult(ctx, c, requests[0], f.cas)
	if e != nil || !bytes.Equal(observed, receiptFor(body)) {
		return nil, errMergeReceipt
	}
	var receipt mergeReceipt
	if len(output) > 256 || verifier.StrictJSON(output, &receipt) != nil || receipt.Schema != "merge-receipt.v1" || receipt.Hash != artifact.Digest(body) {
		// Binding and accounting are confirmed. The caller may preserve the
		// untrusted model bytes for a failed Verifier verdict, never use the
		// trusted full result as if the model had returned a valid receipt.
		return nil, errMergeDecision
	}
	return body, nil
}
