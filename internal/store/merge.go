package store

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
)

var ErrMergeConflict = errors.New("MERGE_NOT_FINALIZABLE")

// MergeContext reads only the current claim's fixed, verified direct inputs.
func (s *Store) MergeContext(ctx context.Context, c TaskClaim, cas *artifact.CAS, id string) (handoff.MergeResult, error) {
	empty := handoff.MergeResult{}
	if c.Role != "aggregate" {
		return empty, ErrPolicy
	}
	snap, e := s.ReadSnapshot(ctx, c, id)
	if e != nil {
		return empty, e
	}
	sources := []handoff.Source{}
	for _, ref := range snap.Artifacts {
		if ref.ContentType != "application/json" {
			return empty, ErrPolicy
		}
		body, e := s.ReadInput(ctx, c, cas, id, ref.ID)
		if e != nil {
			return empty, e
		}
		claims, e := handoff.DecodeClaims(body)
		if e != nil {
			return empty, e
		}
		sources = append(sources, handoff.Source{Ref: ref, Verified: true, Claims: claims})
	}
	result, e := handoff.Merge(sources, nil)
	if e != nil {
		return empty, e
	}
	if _, e = s.ReadSnapshot(ctx, c, id); e != nil {
		return empty, e
	}
	return result, nil
}
func (s *Store) PublishMerge(ctx context.Context, c TaskClaim, cas *artifact.CAS, id string) (artifact.Metadata, handoff.MergeResult, error) {
	result, e := s.MergeContext(ctx, c, cas, id)
	if e != nil {
		return artifact.Metadata{}, result, e
	}
	if !result.CanFinalize {
		tx, state, e := s.guardTask(ctx, c, true)
		if e != nil {
			return artifact.Metadata{}, result, e
		}
		defer tx.Rollback(ctx)
		if state != "executing" {
			return artifact.Metadata{}, result, ErrBadState
		}
		if e = moveTask(ctx, tx, c.RunID, c.TaskID, c.Revision, "failed", "merge_conflict_or_missing_evidence"); e != nil {
			return artifact.Metadata{}, result, e
		}
		tag, e := tx.Exec(ctx, "UPDATE run_budgets SET running_count=running_count-1 WHERE run_id=$1 AND running_count>0", c.RunID)
		if e != nil {
			return artifact.Metadata{}, result, e
		}
		if tag.RowsAffected() != 1 {
			return artifact.Metadata{}, result, ErrBudget
		}
		if e = emit(ctx, tx, c.RunID, "task", c.TaskID, c.Revision+1, "merge_rejected", map[string]any{"snapshot_id": id, "result_hash": artifact.Digest(inputcontext.Canonical(result)), "conflict_count": len(result.Conflicts), "missing_count": len(result.Missing)}); e != nil {
			return artifact.Metadata{}, result, e
		}
		var live bool
		if e = tx.QueryRow(ctx, "SELECT lease_until>clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&live); e != nil {
			return artifact.Metadata{}, result, e
		}
		if !live {
			return artifact.Metadata{}, result, ErrLeaseExpired
		}
		if e = tx.Commit(ctx); e != nil {
			return artifact.Metadata{}, result, e
		}
		return artifact.Metadata{}, result, ErrMergeConflict
	}
	// This publishes an unverified report for the independent Verifier, not a
	// final Workflow answer. PublishArtifact repeats all current input guards.
	a, e := s.PublishArtifact(ctx, c, cas, "report", "application/json", "merge.v1", inputcontext.Canonical(result))
	return a, result, e
}
