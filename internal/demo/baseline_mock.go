package demo

import (
	"encoding/json"
	"errors"
	"strings"

	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/evalbaseline"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

// The mock uses the same model version and actual tool observations as the full
// runtime; only the approved experiment's handoff/verification treatment differs.
func baselineDecision(version int) func(string, mockRequest, *planner.Proposal, string) ([]byte, error) {
	return func(role string, r mockRequest, p *planner.Proposal, fault string) ([]byte, error) {
		final := func(value any) []byte {
			return inputcontext.Canonical(map[string]any{"type": "final", "output": value})
		}
		tool := func(name string, args map[string]any) []byte {
			return inputcontext.Canonical(map[string]any{"type": "tool_call", "call_id": "step_" + string(rune('0'+r.Turn)), "name": name, "arguments": args})
		}
		if role == "research" || role == "data" {
			raw, e := mockDecision(role, r, p, fault)
			if e != nil {
				return nil, e
			}
			if role == "data" && fault == "wrong-data" && len(r.Observations) > 0 {
				var out verifier.Observation
				if verifier.StrictJSON([]byte(r.Observations[len(r.Observations)-1].Output), &out) != nil {
					return nil, errors.New("invalid_observation")
				}
				var rows [][]int64
				if json.Unmarshal(out.Rows, &rows) != nil || len(rows) != 1 || len(rows[0]) != 3 {
					return nil, errors.New("invalid_sql_result")
				}
				rows[0][0]++
				out.Rows = inputcontext.Canonical(rows)
				return final(out), nil
			}
			return raw, nil
		}
		if len(r.Observations) == 0 {
			return tool("candidate.read", map[string]any{}), nil
		}
		var inputs evalbaseline.Context
		if verifier.StrictJSON([]byte(r.Observations[0].Output), &inputs) != nil || inputs.Schema != "candidate_context.v1" || inputs.TaskID != r.TaskID || inputs.RunID != r.RunID {
			return nil, errors.New("candidate_context_invalid")
		}
		if role == "action" {
			if len(r.Observations) == 1 {
				rule, metrics := false, false
				for _, c := range inputs.Inputs {
					if c.Role == "research" {
						claims, e := handoff.DecodeClaims([]byte(c.Content))
						if e == nil {
							for _, claim := range claims {
								if claim.Key == "research.rule" && strings.Contains(string(claim.Value), "paid order once") {
									rule = true
								}
							}
						}
					}
					if c.Role == "data" {
						var o verifier.Observation
						var rows [][]int64
						if verifier.StrictJSON([]byte(c.Content), &o) == nil && json.Unmarshal(o.Rows, &rows) == nil && len(rows) == 1 && len(rows[0]) == 3 {
							metrics = true
						}
					}
				}
				if !rule || !metrics {
					return final(map[string]any{"refusal": "candidate_incomplete"}), nil
				}
				patch := "deduplicate-paid"
				if fault == "wrong-patch" {
					patch = "deduplicate-only"
				}
				return tool("action.patch", map[string]any{"patch": patch}), nil
			}
			if len(r.Observations) == 2 {
				return tool("action.test", map[string]any{}), nil
			}
			if fault == "forge-action" {
				return final(map[string]any{"fabricated": true}), nil
			}
			var out map[string]any
			if verifier.StrictJSON([]byte(r.Observations[len(r.Observations)-1].Output), &out) != nil {
				return nil, errors.New("invalid_test_observation")
			}
			return final(out), nil
		}
		if role == "aggregate" {
			metrics := map[string]any{}
			for _, c := range inputs.Inputs {
				if c.Role == "data" {
					var out verifier.Observation
					var rows [][]int64
					if verifier.StrictJSON([]byte(c.Content), &out) != nil || json.Unmarshal(out.Rows, &rows) != nil || len(rows) != 1 || len(rows[0]) != 3 {
						return final(map[string]any{"refusal": "candidate_invalid"}), nil
					}
					metrics = map[string]any{"paid_total": rows[0][0], "paid_orders": rows[0][1], "duplicate_paid_events": rows[0][2]}
				}
			}
			if len(metrics) == 0 {
				return final(map[string]any{"refusal": "candidate_missing"}), nil
			}
			if fault == "forge-aggregate" {
				metrics["paid_total"] = 999999
			}
			return final(map[string]any{"answer": map[string]any{"decision": "answer", "metrics": metrics, "reason": nil, "input_version": version}, "citations": inputs.Inputs, "semantic_status": "unverified"}), nil
		}
		return nil, errors.New("unsupported_baseline_role")
	}
}

// B0 has one worker and one accumulated observation context, with the same
// bounded business tools. It does not manufacture intermediate agent outputs.
func singleDecision(version int, business string) func(string, mockRequest, *planner.Proposal, string) ([]byte, error) {
	return func(_ string, r mockRequest, _ *planner.Proposal, _ string) ([]byte, error) {
		tool := func(name string, args map[string]any) []byte {
			return inputcontext.Canonical(map[string]any{"type": "tool_call", "call_id": "single_" + string(rune('0'+r.Turn)), "name": name, "arguments": args})
		}
		if len(r.Observations) == 0 {
			return tool("research.read", map[string]any{}), nil
		}
		if len(r.Observations) == 1 {
			return tool("data.query", map[string]any{"query": "paid-metrics"}), nil
		}
		claims, e := handoff.DecodeClaims([]byte(r.Observations[0].Output))
		if e != nil {
			return nil, e
		}
		rule := false
		for _, c := range claims {
			if c.Key == "research.rule" && strings.Contains(string(c.Value), "paid order once") {
				rule = true
			}
		}
		var observed verifier.Observation
		var rows [][]int64
		if !rule || verifier.StrictJSON([]byte(r.Observations[1].Output), &observed) != nil || json.Unmarshal(observed.Rows, &rows) != nil || len(rows) != 1 || len(rows[0]) != 3 {
			return nil, errors.New("single_missing_evidence")
		}
		if business == "incident" {
			if len(r.Observations) == 2 {
				return tool("action.patch", map[string]any{"patch": "deduplicate-paid"}), nil
			}
			if len(r.Observations) == 3 {
				return tool("action.test", map[string]any{}), nil
			}
		}
		answer := map[string]any{"decision": "answer", "metrics": map[string]any{"paid_total": rows[0][0], "paid_orders": rows[0][1], "duplicate_paid_events": rows[0][2]}, "reason": nil, "input_version": version}
		return inputcontext.Canonical(map[string]any{"type": "final", "output": map[string]any{"answer": answer, "semantic_status": "unverified"}}), nil
	}
}
