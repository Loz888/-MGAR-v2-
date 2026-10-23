package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/jackc/pgx/v5"
)

// lineageTx runs after workflow/task/artifact locks. It reads fresh verdicts
// after the artifact locks so concurrent revocations cannot be missed.
func (s *Store) lineageTx(ctx context.Context, tx pgx.Tx, run string, version int, cas *artifact.CAS) (map[string]inputcontext.Lineage, error) {
	type node struct{ key, task, state, role, requirements, result, reuse string }
	rows, e := tx.Query(ctx, `SELECT n.node_key,t.task_id,t.state,t.role,n.requirements::text,COALESCE(t.result_artifact_id::text,''),COALESCE(n.reuse_artifact_id::text,'') FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id WHERE n.run_id=$1 AND n.plan_version=$2 ORDER BY n.node_key`, run, version)
	if e != nil {
		return nil, e
	}
	nodes := []node{}
	for rows.Next() {
		var n node
		if e = rows.Scan(&n.key, &n.task, &n.state, &n.role, &n.requirements, &n.result, &n.reuse); e != nil {
			rows.Close()
			return nil, e
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	history := map[string]inputcontext.Lineage{}
	for _, n := range nodes {
		h := inputcontext.Lineage{TaskID: n.task, Parents: []string{}, Inputs: []inputcontext.Ref{}}
		var spec planner.Node
		if e = json.Unmarshal([]byte(n.requirements), &spec); e != nil {
			return nil, e
		}
		h.RequirementsHash = artifact.Digest(inputcontext.Canonical(spec))
		parents, e := tx.Query(ctx, "SELECT from_key FROM plan_edges WHERE run_id=$1 AND plan_version=$2 AND to_key=$3 ORDER BY from_key", run, version, n.key)
		if e != nil {
			return nil, e
		}
		for parents.Next() {
			var key string
			if e = parents.Scan(&key); e != nil {
				parents.Close()
				return nil, e
			}
			h.Parents = append(h.Parents, key)
		}
		parents.Close()
		if e = parents.Err(); e != nil {
			return nil, e
		}
		if n.state != "succeeded" || n.result == "" {
			history[n.key] = h
			continue
		}
		var body, hash string
		var passed bool
		e = tx.QueryRow(ctx, `SELECT a.artifact_id,a.run_id,a.kind,a.content_sha256,a.storage_uri,a.schema_version,a.producer_task,a.source_plan_version,a.byte_length,a.content_type,a.tool_schema_version,
   c.body,c.content_sha256,COALESCE((SELECT v.passed FROM artifact_verdicts v WHERE v.artifact_id=a.artifact_id ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false)
   FROM artifacts a JOIN artifact_contexts x ON x.artifact_id=a.artifact_id JOIN context_snapshots c ON c.snapshot_id=x.snapshot_id
   WHERE a.run_id=$1 AND a.artifact_id=$2`, run, n.result).Scan(&h.Artifact.ID, &h.Artifact.RunID, &h.Artifact.Kind, &h.Artifact.Hash, &h.Artifact.StorageURI, &h.Artifact.SchemaVersion, &h.Artifact.ProducerTaskID, &h.Artifact.SourcePlanVersion, &h.Artifact.ByteLength, &h.Artifact.ContentType, &h.Artifact.ToolVersion, &body, &hash, &passed)
		if errors.Is(e, pgx.ErrNoRows) {
			history[n.key] = h
			continue
		}
		if e != nil {
			return nil, e
		}
		var snap inputcontext.Snapshot
		if e = json.Unmarshal([]byte(body), &snap); e != nil {
			return nil, e
		}
		h.Config = inputcontext.ExecutionConfig{ModelVersion: snap.ModelVersion, PolicyVersion: snap.ToolVersion, OutputToolVersion: h.Artifact.ToolVersion}
		h.Inputs = snap.Artifacts
		_, contentErr := cas.Read(h.Artifact.Hash, h.Artifact.ByteLength)
		h.Valid = passed && contentErr == nil && snap.Hash() == hash && snap.TaskID == n.task && snap.RunID == run && snap.Role == n.role && spec.Role == n.role && snap.PlanVersion == h.Artifact.SourcePlanVersion && h.Artifact.ProducerTaskID == n.task && h.Artifact.Kind == spec.OutputKind && (h.Artifact.SourcePlanVersion == version || (n.reuse == h.Artifact.ID && h.Artifact.SourcePlanVersion < version))
		history[n.key] = h
	}
	return history, nil
}

// ProposeIncremental is a deterministic trusted control-plane operation, not
// P4's autonomous replanner. No active executions may be silently superseded.
func (s *Store) ProposeIncremental(ctx context.Context, run string, claim OrchestratorClaim, p planner.Proposal, configs map[string]inputcontext.ExecutionConfig, cas *artifact.CAS) (int, planner.Invalidation, error) {
	empty := planner.Invalidation{}
	if cas == nil {
		return 0, empty, ErrPolicy
	}
	if e := planner.Validate(p); e != nil {
		return 0, empty, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return 0, empty, e
	}
	defer tx.Rollback(ctx)
	state, version, revision, e := lockWorkflow(ctx, tx, run)
	if e != nil {
		return 0, empty, e
	}
	if state != "running" {
		return 0, empty, ErrBadState
	}
	if version != p.BaseVersion {
		return 0, empty, ErrPlanConflict
	}
	if e = checkOrchestrator(ctx, tx, run, claim); e != nil {
		return 0, empty, e
	}
	if e = lockLifecycleResults(ctx, tx, run, version); e != nil {
		return 0, empty, e
	}
	var live bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM task_instances WHERE run_id=$1 AND state IN ('claimed','executing','output_ready','verifying'))", run).Scan(&live); e != nil {
		return 0, empty, e
	}
	if live {
		return 0, empty, ErrBadState
	}
	history, e := s.lineageTx(ctx, tx, run, version, cas)
	if e != nil {
		return 0, empty, e
	}
	decision, e := planner.Invalidate(p, configs, history)
	if e != nil {
		return 0, empty, e
	}
	next := version + 1
	body := inputcontext.Canonical(map[string]any{"proposal": p, "configs": configs, "decision": decision})
	if _, e = tx.Exec(ctx, "INSERT INTO plans(run_id,plan_version,plan_hash,proposer,schema_version) VALUES($1,$2,$3,$4,'plan.v1')", run, next, artifact.Digest(body), claim.Owner); e != nil {
		return 0, empty, e
	}
	for _, n := range p.Nodes {
		var task string
		reuse := decision.Reuse[n.Key]
		if reuse != "" {
			task = history[n.Key].TaskID
		} else {
			if e = tx.QueryRow(ctx, "INSERT INTO task_instances(run_id,node_key,role) VALUES($1,$2,$3) RETURNING task_id", run, n.Key, n.Role).Scan(&task); e != nil {
				return 0, empty, e
			}
		}
		if _, e = tx.Exec(ctx, "INSERT INTO plan_nodes(run_id,plan_version,node_key,task_instance_id,role,requirements,reuse_artifact_id) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid)", run, next, n.Key, task, n.Role, inputcontext.Canonical(n), reuse); e != nil {
			return 0, empty, e
		}
		cfg := configs[n.Key]
		if _, e = tx.Exec(ctx, "INSERT INTO plan_node_configs(run_id,plan_version,node_key,model_config_version,tool_policy_version,output_tool_version) VALUES($1,$2,$3,$4,$5,$6)", run, next, n.Key, cfg.ModelVersion, cfg.PolicyVersion, cfg.OutputToolVersion); e != nil {
			return 0, empty, e
		}
	}
	for _, edge := range p.Edges {
		if _, e = tx.Exec(ctx, "INSERT INTO plan_edges VALUES($1,$2,$3,$4)", run, next, edge.From, edge.To); e != nil {
			return 0, empty, e
		}
	}
	tag, e := tx.Exec(ctx, "UPDATE workflow_runs SET active_plan_version=$2,revision=revision+1 WHERE run_id=$1 AND revision=$3", run, next, revision)
	if e != nil {
		return 0, empty, e
	}
	if tag.RowsAffected() != 1 {
		return 0, empty, ErrCASConflict
	}
	if e = emit(ctx, tx, run, "workflow", run, revision+1, "plan_committed", map[string]any{"plan_version": next, "base_version": version, "mode": "incremental", "rationale": p.Rationale, "decision": decision}); e != nil {
		return 0, empty, e
	}
	if e = commitControl(ctx, tx, run, claim); e != nil {
		return 0, empty, e
	}
	return next, decision, nil
}
