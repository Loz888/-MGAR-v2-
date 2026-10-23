package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ReadSnapshot(ctx context.Context, c TaskClaim, id string) (inputcontext.Snapshot, error) {
	var snap inputcontext.Snapshot
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return snap, e
	}
	defer tx.Rollback(ctx)
	if state != "claimed" && state != "executing" {
		return snap, ErrBadState
	}
	var body, hash string
	e = tx.QueryRow(ctx, "SELECT body,content_sha256 FROM context_snapshots WHERE snapshot_id=$1 AND run_id=$2 AND task_id=$3 AND plan_version=$4", id, c.RunID, c.TaskID, c.PlanVersion).Scan(&body, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return snap, ErrPolicy
	}
	if e != nil {
		return snap, e
	}
	if e = json.Unmarshal([]byte(body), &snap); e != nil || snap.Hash() != hash || snap.Role != c.Role || snap.Selection != nil {
		return inputcontext.Snapshot{}, ErrStaleInput
	}
	return snap, tx.Commit(ctx)
}

// PrepareContext reads authorized, hash-checked bytes, selects before any model
// call, and persists lossy derived summaries without changing Task success.
func (s *Store) PrepareContext(ctx context.Context, c TaskClaim, cas *artifact.CAS, id, goal, mode string, maxBytes, maxTokens int) (inputcontext.SelectionResult, error) {
	var result inputcontext.SelectionResult
	snap, e := s.ReadSnapshot(ctx, c, id)
	if e != nil {
		return result, e
	}
	content := map[string][]byte{}
	for _, ref := range snap.Artifacts {
		body, e := s.ReadInput(ctx, c, cas, id, ref.ID)
		if e != nil {
			return result, e
		}
		content[ref.ID] = body
	}
	result, e = inputcontext.Select(snap, goal, mode, maxBytes, maxTokens, content)
	if e != nil || result.Blocked {
		return result, e
	}
	for _, summary := range result.Summaries {
		if e = s.persistDerivedSummary(ctx, c, cas, snap, summary); e != nil {
			return inputcontext.SelectionResult{}, e
		}
	}
	// Recheck all authority after filesystem work, before releasing the wire input.
	if _, e = s.ReadSnapshot(ctx, c, id); e != nil {
		return inputcontext.SelectionResult{}, e
	}
	return result, nil
}

func (s *Store) persistDerivedSummary(ctx context.Context, c TaskClaim, cas *artifact.CAS, snap inputcontext.Snapshot, summary inputcontext.DerivedSummary) error {
	body := summary.Bytes()
	hash, e := cas.Put(body)
	if e != nil {
		return e
	}
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if state != "claimed" && state != "executing" {
		return ErrBadState
	}
	var previous string
	e = tx.QueryRow(ctx, "SELECT content_sha256 FROM artifacts WHERE artifact_id=$1 AND run_id=$2 AND producer_task=$3 AND kind='summary'", summary.ID, c.RunID, c.TaskID).Scan(&previous)
	if e == nil {
		if previous != hash {
			return ErrStaleInput
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO artifacts(artifact_id,run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length,content_type,tool_schema_version)
 SELECT $1::uuid,$2,'summary',$3,'artifact://'||($1::uuid)::text,'artifact.v1',$4,$5,$6,'application/json','summary-rule.v1'
 WHERE EXISTS(SELECT 1 FROM task_instances WHERE task_id=$4 AND revision=$7 AND lease_until>clock_timestamp())`, summary.ID, c.RunID, hash, c.TaskID, c.PlanVersion, len(body), c.Revision)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseExpired
	}
	if _, e = tx.Exec(ctx, "INSERT INTO artifact_contexts(artifact_id,run_id,snapshot_id) VALUES($1,$2,$3)", summary.ID, c.RunID, snap.ID); e != nil {
		return e
	}
	if e = emit(ctx, tx, c.RunID, "artifact", summary.ID, 0, "derived_summary_created", map[string]any{"source_artifact_id": summary.SourceID, "lossy": true, "verified": false, "content_sha256": hash}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
