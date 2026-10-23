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

// BuildSnapshot is the trusted execution-entry builder, not a Worker-supplied
// list of arbitrary artifacts. guardTask rechecks current direct dependencies.
func (s *Store) BuildSnapshot(ctx context.Context, c TaskClaim, modelVersion, toolVersion string, cas *artifact.CAS) (inputcontext.Snapshot, error) {
	var snapshot inputcontext.Snapshot
	if cas == nil || !inputcontext.ValidVersion(modelVersion) || !inputcontext.ValidVersion(toolVersion) {
		return snapshot, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return snapshot, e
	}
	defer tx.Rollback(ctx)
	if state != "claimed" && state != "executing" {
		return snapshot, ErrBadState
	}
	var requiredModel, requiredTool string
	e = tx.QueryRow(ctx, "SELECT cfg.model_config_version,cfg.tool_policy_version FROM plan_node_configs cfg JOIN plan_nodes n USING(run_id,plan_version,node_key) WHERE cfg.run_id=$1 AND cfg.plan_version=$2 AND n.task_instance_id=$3", c.RunID, c.PlanVersion, c.TaskID).Scan(&requiredModel, &requiredTool)
	if e == nil && (requiredModel != modelVersion || requiredTool != toolVersion) {
		return snapshot, ErrPolicy
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return snapshot, e
	}
	snapshot = inputcontext.Snapshot{SchemaVersion: "context.v1", RunID: c.RunID, TaskID: c.TaskID, Role: c.Role, PlanVersion: c.PlanVersion, ModelVersion: modelVersion, ToolVersion: toolVersion, Artifacts: []inputcontext.Ref{}}
	rows, e := tx.Query(ctx, `SELECT a.artifact_id,a.content_sha256,i.producer_node,a.producer_task,a.source_plan_version,a.tool_schema_version,a.byte_length,a.kind,a.content_type
 FROM task_inputs i JOIN artifacts a ON a.run_id=i.run_id AND a.artifact_id=i.artifact_id WHERE i.run_id=$1 AND i.task_id=$2 ORDER BY a.artifact_id`, c.RunID, c.TaskID)
	if e != nil {
		return snapshot, e
	}
	for rows.Next() {
		var r inputcontext.Ref
		if e = rows.Scan(&r.ID, &r.Hash, &r.ProducerNode, &r.ProducerTaskID, &r.SourcePlanVersion, &r.ToolVersion, &r.ByteLength, &r.Kind, &r.ContentType); e != nil {
			rows.Close()
			return snapshot, e
		}
		r.Purpose = "direct_dependency:" + r.ProducerNode
		snapshot.Artifacts = append(snapshot.Artifacts, r)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return snapshot, e
	}
	if len(snapshot.Artifacts) > 64 {
		return snapshot, ErrPolicy
	}
	for _, r := range snapshot.Artifacts {
		if !inputcontext.ValidVersion(r.ToolVersion) {
			return snapshot, ErrStaleInput
		}
		if _, e = cas.Read(r.Hash, r.ByteLength); e != nil {
			return snapshot, e
		}
	}
	var body, hash, id string
	e = tx.QueryRow(ctx, "SELECT snapshot_id,body,content_sha256 FROM context_snapshots WHERE task_id=$1", c.TaskID).Scan(&id, &body, &hash)
	if e == nil {
		snapshot.ID = id
		if hash != snapshot.Hash() || !bytes.Equal([]byte(body), snapshot.Bytes()) {
			return inputcontext.Snapshot{}, ErrStaleInput
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		if e = tx.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&snapshot.ID); e != nil {
			return snapshot, e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO context_snapshots(snapshot_id,run_id,task_id,plan_version,content_sha256,body)
  SELECT $1,$2,$3,$4,$5,$6 WHERE EXISTS(SELECT 1 FROM task_instances WHERE task_id=$3 AND revision=$7 AND lease_until>clock_timestamp())`, snapshot.ID, c.RunID, c.TaskID, c.PlanVersion, snapshot.Hash(), string(snapshot.Bytes()), c.Revision)
		if e != nil {
			return inputcontext.Snapshot{}, e
		}
		if tag.RowsAffected() != 1 {
			return inputcontext.Snapshot{}, ErrLeaseExpired
		}
		if e = emit(ctx, tx, c.RunID, "snapshot", snapshot.ID, 0, "context_snapshot_created", map[string]string{"task_id": c.TaskID, "content_sha256": snapshot.Hash()}); e != nil {
			return inputcontext.Snapshot{}, e
		}
	} else {
		return inputcontext.Snapshot{}, e
	}
	var live bool
	if e = tx.QueryRow(ctx, "SELECT lease_until>clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&live); e != nil {
		return inputcontext.Snapshot{}, e
	}
	if !live {
		return inputcontext.Snapshot{}, ErrLeaseExpired
	}
	if e = tx.Commit(ctx); e != nil {
		return inputcontext.Snapshot{}, e
	}
	return snapshot, nil
}

// ReadInput authorizes against a persisted snapshot and the current fenced
// claim before returning any content. The disk hash alone grants no access.
func (s *Store) ReadInput(ctx context.Context, c TaskClaim, cas *artifact.CAS, snapshotID, artifactID string) ([]byte, error) {
	if cas == nil {
		return nil, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if state != "claimed" && state != "executing" {
		return nil, ErrBadState
	}
	var body, hash string
	e = tx.QueryRow(ctx, "SELECT body,content_sha256 FROM context_snapshots WHERE snapshot_id=$1 AND task_id=$2 AND run_id=$3 AND plan_version=$4", snapshotID, c.TaskID, c.RunID, c.PlanVersion).Scan(&body, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrPolicy
	}
	if e != nil {
		return nil, e
	}
	var snap inputcontext.Snapshot
	if e = json.Unmarshal([]byte(body), &snap); e != nil || snap.Hash() != hash || snap.Role != c.Role {
		return nil, ErrStaleInput
	}
	for _, r := range snap.Artifacts {
		if r.ID == artifactID {
			content, e := cas.Read(r.Hash, r.ByteLength)
			if e != nil {
				return nil, e
			}
			var live bool
			if e = tx.QueryRow(ctx, "SELECT lease_until>clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&live); e != nil {
				return nil, e
			}
			if !live {
				return nil, ErrLeaseExpired
			}
			if e = tx.Commit(ctx); e != nil {
				return nil, e
			}
			return content, nil
		}
	}
	return nil, ErrPolicy
}
