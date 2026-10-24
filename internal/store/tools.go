package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/jackc/pgx/v5"
)

var toolUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var toolHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ToolRequest struct{ ID, Capability, Version, Resource, Hash, Effect string }
type toolScope struct {
	Run, Owner, Version, Resource string
	Epoch                         int64
	Plan                          int
}

func scopeFor(c TaskClaim, r ToolRequest) toolScope {
	return toolScope{c.RunID, c.Owner, r.Version, r.Resource, c.Epoch, c.PlanVersion}
}
func (s *Store) ValidateToolClaim(ctx context.Context, c TaskClaim) error {
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return ErrBadState
	}
	return commitModelTask(ctx, tx, c)
}

// BeginTool commits authorization before any external action. Cached success is
// returned only for the exact same request and fenced principal; uncertainty
// never creates another execution authorization, including after a restart.
func (s *Store) BeginTool(ctx context.Context, c TaskClaim, r ToolRequest) (bool, error) {
	if r.Effect == "non_idempotent" {
		return false, ErrPolicy
	}
	return s.beginTool(ctx, c, r, "", "")
}
func (s *Store) BeginApprovedTool(ctx context.Context, c TaskClaim, r ToolRequest, approvalID, token string) (bool, error) {
	if c.Role != "action" || r.Effect != "non_idempotent" {
		return false, ErrPolicy
	}
	return s.beginTool(ctx, c, r, approvalID, token)
}
func (s *Store) beginTool(ctx context.Context, c TaskClaim, r ToolRequest, approvalID, token string) (bool, error) {
	if !toolUUID.MatchString(r.ID) || !toolHash.MatchString(r.Hash) || r.Capability == "" || r.Version == "" || r.Resource == "" || (r.Effect != "readonly" && r.Effect != "idempotent_write" && r.Effect != "non_idempotent") {
		return false, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return false, ErrBadState
	}
	var task, hash, capability, effect, status string
	var raw []byte
	e = tx.QueryRow(ctx, "SELECT task_id,request_hash,capability,effect_class,status,scope FROM tool_invocations WHERE invocation_id=$1", r.ID).Scan(&task, &hash, &capability, &effect, &status, &raw)
	if e == nil {
		var scope toolScope
		if json.Unmarshal(raw, &scope) != nil || scope != scopeFor(c, r) || task != c.TaskID || hash != r.Hash || capability != r.Capability || effect != r.Effect {
			return false, ErrPolicy
		}
		if status != "succeeded" || r.Effect == "non_idempotent" {
			return false, ErrUnsafeReplay
		}
		return true, commitModelTask(ctx, tx, c)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return false, e
	}
	var snapshot bool
	var count int
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM context_snapshots WHERE task_id=$1 AND run_id=$2 AND plan_version=$3),(SELECT count(*) FROM tool_invocations WHERE task_id=$1)", c.TaskID, c.RunID, c.PlanVersion).Scan(&snapshot, &count); e != nil {
		return false, e
	}
	if !snapshot {
		return false, ErrStaleInput
	}
	if count >= 64 {
		return false, ErrBudget
	}
	tag, e := tx.Exec(ctx, "UPDATE run_budgets SET reserved_tool_calls=reserved_tool_calls+1 WHERE run_id=$1 AND reserved_tool_calls+settled_tool_calls<max_tool_calls AND reserved_tool_calls+reserved_model_calls<max_call_concurrency", c.RunID)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() != 1 {
		return false, ErrBudget
	}
	if r.Effect == "non_idempotent" {
		if e = consumeApproval(ctx, tx, c, r, approvalID, token); e != nil {
			if errors.Is(e, ErrApprovalExpired) {
				if _, refundErr := tx.Exec(ctx, "UPDATE run_budgets SET reserved_tool_calls=reserved_tool_calls-1 WHERE run_id=$1", c.RunID); refundErr != nil {
					return false, refundErr
				}
				if commitErr := commitModelTask(ctx, tx, c); commitErr != nil {
					return false, commitErr
				}
			}
			return false, e
		}
	}
	raw, _ = json.Marshal(scopeFor(c, r))
	if _, e = tx.Exec(ctx, "INSERT INTO tool_invocations(invocation_id,task_id,capability,scope,request_hash,effect_class,status) VALUES($1,$2,$3,$4,$5,$6,'reserved')", r.ID, c.TaskID, r.Capability, raw, r.Hash, r.Effect); e != nil {
		return false, e
	}
	if e = emit(ctx, tx, c.RunID, "tool_invocation", r.ID, 0, "tool_reserved", map[string]any{"task_id": c.TaskID, "capability": r.Capability, "version": r.Version, "resource": r.Resource, "request_hash": r.Hash, "effect": r.Effect}); e != nil {
		return false, e
	}
	return false, commitModelTask(ctx, tx, c)
}

// FinishTool stores bounded output as an immutable intermediate artifact. It
// does not finish the task. A lost settlement stays reserved and cannot replay.
func (s *Store) FinishTool(ctx context.Context, c TaskClaim, r ToolRequest, cas *artifact.CAS, body []byte, status string) error {
	if status != "succeeded" && status != "failed" && status != "unknown" {
		return ErrPolicy
	}
	if cas == nil || len(body) > 65536 || (status == "succeeded" && !json.Valid(body)) {
		return ErrPolicy
	}
	if e := s.ValidateToolClaim(ctx, c); e != nil {
		return e
	}
	body = bytes.Clone(body)
	hash := ""
	var e error
	if status == "succeeded" {
		hash, e = cas.Put(body)
		if e != nil {
			return e
		}
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return ErrBadState
	}
	var raw []byte
	var current, requestHash, capability, effect string
	if e = tx.QueryRow(ctx, "SELECT status,request_hash,scope,capability,effect_class FROM tool_invocations WHERE invocation_id=$1 AND task_id=$2 FOR UPDATE", r.ID, c.TaskID).Scan(&current, &requestHash, &raw, &capability, &effect); e != nil {
		return e
	}
	var scope toolScope
	if json.Unmarshal(raw, &scope) != nil || scope != scopeFor(c, r) || requestHash != r.Hash || current != "reserved" || capability != r.Capability || effect != r.Effect {
		return ErrPolicy
	}
	var id string
	if status == "succeeded" {
		e = tx.QueryRow(ctx, `INSERT INTO artifacts(artifact_id,run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length,content_type,tool_schema_version)
 SELECT id,$1,'tool_result',$2,'artifact://'||id::text,'artifact.v1',$3,$4,$5,'application/json',$6 FROM (SELECT gen_random_uuid() id) x RETURNING artifact_id`, c.RunID, hash, c.TaskID, c.PlanVersion, len(body), r.Version).Scan(&id)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO artifact_contexts(artifact_id,run_id,snapshot_id) SELECT $1,run_id,snapshot_id FROM context_snapshots WHERE run_id=$2 AND task_id=$3 AND plan_version=$4", id, c.RunID, c.TaskID, c.PlanVersion); e != nil {
			return e
		}
	}
	if status != "unknown" {
		tag, err := tx.Exec(ctx, "UPDATE run_budgets SET reserved_tool_calls=reserved_tool_calls-1,settled_tool_calls=settled_tool_calls+1 WHERE run_id=$1 AND reserved_tool_calls>=1", c.RunID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrBudget
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE tool_invocations SET status=$2,result_artifact_id=NULLIF($3,'')::uuid WHERE invocation_id=$1", r.ID, status, id); e != nil {
		return e
	}
	if e = emit(ctx, tx, c.RunID, "tool_invocation", r.ID, 1, "tool_"+status, map[string]string{"result_artifact_id": id, "output_hash": hash}); e != nil {
		return e
	}
	return commitModelTask(ctx, tx, c)
}
func (s *Store) ToolResult(ctx context.Context, c TaskClaim, r ToolRequest, cas *artifact.CAS) ([]byte, error) {
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return nil, ErrBadState
	}
	var hash string
	var size int64
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT a.content_sha256,a.byte_length,v.scope FROM tool_invocations v JOIN artifacts a ON a.artifact_id=v.result_artifact_id WHERE v.invocation_id=$1 AND v.task_id=$2 AND v.request_hash=$3 AND v.status='succeeded' AND a.run_id=$4 AND a.producer_task=$2`, r.ID, c.TaskID, r.Hash, c.RunID).Scan(&hash, &size, &raw)
	if e != nil {
		return nil, e
	}
	var scope toolScope
	if json.Unmarshal(raw, &scope) != nil || scope != scopeFor(c, r) || cas == nil {
		return nil, ErrPolicy
	}
	b, e := cas.Read(hash, size)
	if e != nil {
		return nil, e
	}
	return b, commitModelTask(ctx, tx, c)
}

// Denials are immutable digest-only events; expired tasks may still be audited.
func (s *Store) RecordToolDenial(ctx context.Context, c TaskClaim, digest, reason string) error {
	if !toolHash.MatchString(digest) || len(reason) > 64 {
		return ErrPolicy
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, _, _, e = lockWorkflow(ctx, tx, c.RunID); e != nil {
		return e
	}
	var id string
	if e = tx.QueryRow(ctx, "SELECT gen_random_uuid() FROM task_instances WHERE run_id=$1 AND task_id=$2", c.RunID, c.TaskID).Scan(&id); e != nil {
		return e
	}
	if e = emit(ctx, tx, c.RunID, "policy_decision", id, 0, "tool_denied", map[string]any{"task_id": c.TaskID, "owner": c.Owner, "epoch": c.Epoch, "request_hash": digest, "reason": reason}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
