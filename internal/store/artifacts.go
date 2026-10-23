package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/jackc/pgx/v5"
)

// PublishArtifact writes immutable bytes before a short fenced DB transaction.
// Metadata, output_ready, delegation release and audit/outbox commit together.
// A failed/unknown DB commit can leave an orphan; no destructive GC is automatic.
func (s *Store) PublishArtifact(ctx context.Context, c TaskClaim, cas *artifact.CAS, kind, contentType, toolVersion string, body []byte) (artifact.Metadata, error) {
	var a artifact.Metadata
	if cas == nil || len(body) > artifact.MaxBytes || !inputcontext.ValidVersion(toolVersion) {
		return a, ErrPolicy
	}
	body = bytes.Clone(body)
	if contentType != "application/json" && contentType != "text/plain" && contentType != "application/octet-stream" {
		return a, ErrPolicy
	}
	if contentType == "application/json" && !json.Valid(body) {
		return a, ErrPolicy
	}
	pre, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return a, e
	}
	if state != "executing" {
		pre.Rollback(ctx)
		return a, ErrBadState
	}
	if e = requireSettledModels(ctx, pre, c); e != nil {
		pre.Rollback(ctx)
		return a, e
	}
	var preKind string
	e = pre.QueryRow(ctx, "SELECT requirements->>'required_output_kind' FROM plan_nodes WHERE run_id=$1 AND plan_version=$2 AND task_instance_id=$3", c.RunID, c.PlanVersion, c.TaskID).Scan(&preKind)
	if e != nil {
		pre.Rollback(ctx)
		return a, e
	}
	if kind != preKind {
		pre.Rollback(ctx)
		return a, ErrPolicy
	}
	var snapshotID string
	e = pre.QueryRow(ctx, "SELECT snapshot_id FROM context_snapshots WHERE run_id=$1 AND task_id=$2 AND plan_version=$3", c.RunID, c.TaskID, c.PlanVersion).Scan(&snapshotID)
	pre.Rollback(ctx)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, ErrStaleInput
	}
	if e != nil {
		return a, e
	}
	var requiredTool string
	e = s.Pool.QueryRow(ctx, "SELECT cfg.output_tool_version FROM plan_node_configs cfg JOIN plan_nodes n USING(run_id,plan_version,node_key) WHERE cfg.run_id=$1 AND cfg.plan_version=$2 AND n.task_instance_id=$3", c.RunID, c.PlanVersion, c.TaskID).Scan(&requiredTool)
	if e == nil && requiredTool != toolVersion {
		return a, ErrPolicy
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return a, e
	}
	hash, e := cas.Put(body)
	if e != nil {
		return a, e
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return a, ErrBadState
	}
	if e = requireSettledModels(ctx, tx, c); e != nil {
		return a, e
	}
	var expected string
	if e = tx.QueryRow(ctx, "SELECT requirements->>'required_output_kind' FROM plan_nodes WHERE run_id=$1 AND plan_version=$2 AND task_instance_id=$3", c.RunID, c.PlanVersion, c.TaskID).Scan(&expected); e != nil {
		return a, e
	}
	if kind != expected {
		return a, ErrPolicy
	}
	a = artifact.Metadata{RunID: c.RunID, Kind: kind, Hash: hash, SchemaVersion: "artifact.v1", ProducerTaskID: c.TaskID, SourcePlanVersion: c.PlanVersion, ByteLength: int64(len(body)), ContentType: contentType, ToolVersion: toolVersion}
	e = tx.QueryRow(ctx, `INSERT INTO artifacts(artifact_id,run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length,content_type,tool_schema_version)
 SELECT id,$1,$2,$3,'artifact://'||id::text,'artifact.v1',$4,$5,$6,$7,$8 FROM (SELECT gen_random_uuid() id) ids
 WHERE EXISTS(SELECT 1 FROM task_instances WHERE task_id=$4 AND revision=$9 AND lease_until>clock_timestamp()) RETURNING artifact_id,storage_uri`, c.RunID, kind, hash, c.TaskID, c.PlanVersion, len(body), contentType, toolVersion, c.Revision).Scan(&a.ID, &a.StorageURI)
	if errors.Is(e, pgx.ErrNoRows) {
		return artifact.Metadata{}, ErrLeaseExpired
	}
	if e != nil {
		return artifact.Metadata{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO artifact_contexts(artifact_id,run_id,snapshot_id) VALUES($1,$2,$3)", a.ID, c.RunID, snapshotID); e != nil {
		return artifact.Metadata{}, e
	}
	tag, e := tx.Exec(ctx, "UPDATE task_instances SET state='output_ready',result_artifact_id=$2,revision=revision+1 WHERE task_id=$1 AND revision=$3 AND lease_until>clock_timestamp()", c.TaskID, a.ID, c.Revision)
	if e != nil {
		return artifact.Metadata{}, e
	}
	if tag.RowsAffected() != 1 {
		return artifact.Metadata{}, ErrLeaseExpired
	}
	tag, e = tx.Exec(ctx, "UPDATE run_budgets SET running_count=running_count-1 WHERE run_id=$1 AND running_count>0", c.RunID)
	if e != nil {
		return artifact.Metadata{}, e
	}
	if tag.RowsAffected() != 1 {
		return artifact.Metadata{}, ErrBudget
	}
	if e = emit(ctx, tx, c.RunID, "artifact", a.ID, 0, "artifact_published", map[string]any{"artifact_id": a.ID, "content_sha256": hash, "producer_task": c.TaskID}); e != nil {
		return artifact.Metadata{}, e
	}
	if e = emit(ctx, tx, c.RunID, "task", c.TaskID, c.Revision+1, "output_ready", map[string]string{"artifact_id": a.ID}); e != nil {
		return artifact.Metadata{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return artifact.Metadata{}, e
	}
	return a, nil
}
