package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
)

var ErrReplanRejected = errors.New("REPLAN_REJECTED")

type ReplanResult struct {
	Accepted bool                 `json:"accepted"`
	Stopped  bool                 `json:"stopped"`
	Version  int                  `json:"plan_version"`
	Attempt  int                  `json:"attempt"`
	Reason   string               `json:"reason"`
	Decision planner.Invalidation `json:"decision"`
}

// Replan freezes trusted policy in the existing immutable workflow event ledger.
// Attempts, optional new plan and their outbox records share one transaction.
// A committed rejection is returned as ErrReplanRejected with its durable result.
func (s *Store) Replan(ctx context.Context, run string, c OrchestratorClaim, raw []byte, configs map[string]inputcontext.ExecutionConfig, policy planner.ReplanPolicy, cas *artifact.CAS) (ReplanResult, error) {
	result := ReplanResult{}
	if !policy.Valid() || cas == nil || len(raw) > 1<<20 {
		return result, ErrPolicy
	}
	proposal, decodeErr := planner.DecodeReplan(raw)
	digest := planner.ReplanDigest(raw, proposal, configs, decodeErr == nil)
	tx, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	if w.State != "running" {
		return result, ErrBadState
	}
	if decodeErr == nil && proposal.Proposal.BaseVersion != w.ActivePlanVersion {
		return result, ErrPlanConflict
	}
	var count int
	var repeated bool
	var frozen string
	if e = tx.QueryRow(ctx, "SELECT count(*),COALESCE(bool_or(payload->>'candidate_hash'=$2),false) FROM workflow_events WHERE run_id=$1 AND kind='replan_attempt'", run, digest).Scan(&count, &repeated); e != nil {
		return result, e
	}
	if count > 0 {
		if e = tx.QueryRow(ctx, "SELECT (payload->'policy')::text FROM workflow_events WHERE run_id=$1 AND kind='replan_attempt' ORDER BY entity_revision LIMIT 1", run).Scan(&frozen); e != nil {
			return result, e
		}
		var have planner.ReplanPolicy
		if e = json.Unmarshal([]byte(frozen), &have); e != nil {
			return result, e
		}
		if have != policy {
			return result, ErrPolicy
		}
	}
	if e = lockLifecycleResults(ctx, tx, run, w.ActivePlanVersion); e != nil {
		return result, e
	}
	var unsafe bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_calls WHERE run_id=$1 AND status IN ('reserved','unknown')) OR EXISTS(SELECT 1 FROM tool_invocations v JOIN task_instances t ON t.task_id=v.task_id WHERE t.run_id=$1 AND t.state IN ('claimed','executing') AND v.effect_class='non_idempotent' AND v.status IN ('reserved','unknown','succeeded'))`, run).Scan(&unsafe); e != nil {
		return result, e
	}
	if unsafe {
		return result, ErrUnsafeReplay
	}
	result.Attempt = count + 1
	result.Version = w.ActivePlanVersion
	result.Reason = "accepted"
	switch {
	case count >= policy.MaxReplans:
		result.Reason = "max_replans"
	case repeated:
		result.Reason = "repeated_proposal"
	case decodeErr != nil:
		result.Reason = "invalid_plan"
	}
	removed := []string{}
	if result.Reason == "accepted" {
		var supported bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id
    WHERE n.run_id=$1 AND n.plan_version=$2 AND n.node_key=$3 AND
    (($4='permanent' AND t.state IN ('failed','blocked')) OR ($4='evidence_rejected' AND
     COALESCE((SELECT NOT v.passed FROM artifact_verdicts v WHERE v.artifact_id=t.result_artifact_id ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false))))`, run, w.ActivePlanVersion, proposal.FailedNode, proposal.FailureKind).Scan(&supported)
		if e != nil {
			return result, e
		}
		if !supported {
			result.Reason = "unsubstantiated_failure"
		}
	}
	if result.Reason == "accepted" {
		history, e := s.lineageTx(ctx, tx, run, w.ActivePlanVersion, cas)
		if e != nil {
			return result, e
		}
		decision, e := planner.Invalidate(proposal.Proposal, configs, history)
		if e != nil {
			result.Reason = "invalid_configuration"
		} else {
			result.Decision = decision
			if e = planner.CheckReplanBounds(proposal.Proposal, policy, len(decision.Recompute)); e != nil {
				result.Reason = "policy_bounds"
			}
			var remaining int
			if e = tx.QueryRow(ctx, "SELECT max_delegations-used_delegations FROM run_budgets WHERE run_id=$1", run).Scan(&remaining); e != nil {
				return result, e
			}
			if remaining < len(decision.Recompute) {
				result.Reason = "delegation_budget"
			}
			present := map[string]bool{}
			for _, n := range proposal.Proposal.Nodes {
				present[n.Key] = true
			}
			for key := range history {
				if !present[key] {
					removed = append(removed, key)
				}
			}
			sort.Strings(removed)
		}
	}
	if result.Reason == "accepted" {
		// Cancel only obsolete nonterminal instances, before allocating replacements.
		// Unknown side effects were rejected above; completed history is immutable.
		if e = cancelRemaining(ctx, tx, run); e != nil {
			return result, e
		}
		result.Version, result.Decision, e = s.proposeIncrementalTx(ctx, tx, run, c, proposal.Proposal, configs, cas)
		if e != nil {
			return result, e
		}
		result.Accepted = true
	}
	// Record the policy/count even for invalid model output; repeated rejection
	// cannot loop outside the durable budget. No raw model payload is logged.
	var revision int64
	if e = tx.QueryRow(ctx, "UPDATE workflow_runs SET revision=revision+1 WHERE run_id=$1 RETURNING revision", run).Scan(&revision); e != nil {
		return result, e
	}
	if e = emit(ctx, tx, run, "workflow", run, revision, "replan_attempt", map[string]any{"candidate_hash": digest, "policy": policy, "attempt": result.Attempt, "reason": result.Reason, "rationale": proposal.Proposal.Rationale, "failure_kind": proposal.FailureKind, "failed_node": proposal.FailedNode, "decision": result.Decision, "removed_nodes": removed, "plan_version": result.Version}); e != nil {
		return result, e
	}
	if !result.Accepted && (repeated || result.Attempt >= policy.MaxReplans) {
		if e = cancelRemaining(ctx, tx, run); e != nil {
			return result, e
		}
		w.Revision = revision
		if _, e = setWorkflow(ctx, tx, w, "failed", "replan_exhausted_or_repeated", &c); e != nil {
			return result, e
		}
		result.Stopped = true
	}
	if e = commitControl(ctx, tx, run, c); e != nil {
		return ReplanResult{}, e
	}
	if !result.Accepted {
		return result, ErrReplanRejected
	}
	return result, nil
}
