package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/jackc/pgx/v5"
)

// VerifyArtifact is a controller-only entrypoint. Agent claims and Reviewer
// output cannot supply its orchestrator authority or trusted requirement.
// Long-running independent checks occur outside the database transaction.
func (s *Store) VerifyArtifact(ctx context.Context, run string, c OrchestratorClaim, id string, raw []byte, requirement verifier.Requirement, cas *artifact.CAS, engine verifier.Engine) (verifier.Verdict, error) {
	var v verifier.Verdict
	if cas == nil || len(raw) > verifier.MaxEvidenceBytes {
		return v, ErrPolicy
	}
	raw = bytes.Clone(raw)
	// Freeze caller-owned slices/pointers before releasing database locks.
	policy, e := json.Marshal(requirement)
	if e != nil {
		return v, ErrPolicy
	}
	var frozen verifier.Requirement
	if e = json.Unmarshal(policy, &frozen); e != nil {
		return v, ErrPolicy
	}
	requirement = frozen
	pre, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return v, e
	}
	p, e := verificationProof(ctx, pre, w, id, cas)
	if e == nil {
		e = checkOrchestrator(ctx, pre, run, c)
	}
	pre.Rollback(ctx)
	if e != nil {
		return v, e
	}
	v = engine.Check(ctx, raw, p, requirement)
	tx, current, e := s.controlTx(ctx, run, c)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if current.ActivePlanVersion != w.ActivePlanVersion {
		return v, ErrStaleInput
	}
	again, e := verificationProof(ctx, tx, current, id, cas)
	if e != nil {
		return v, e
	}
	before, _ := json.Marshal(p)
	after, _ := json.Marshal(again)
	if !bytes.Equal(before, after) {
		return v, ErrStaleInput
	}
	var verdictID string
	if e = tx.QueryRow(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed,evidence_artifact_id) VALUES($1,$2,$3,$1) RETURNING verdict_id", id, v.Version, v.Status == "passed").Scan(&verdictID); e != nil {
		return v, e
	}
	// Detailed pass/fail/unverifiable checks are immutable event history. The
	// referenced evidence artifact is the checked object, not an Agent verdict.
	if e = emit(ctx, tx, run, "verdict", verdictID, 0, "verdict_recorded", map[string]any{"verdict": v, "evidence_sha256": artifact.Digest(raw), "requirement_sha256": artifact.Digest(policy)}); e != nil {
		return v, e
	}
	return v, commitControl(ctx, tx, run, c)
}
func verificationProof(ctx context.Context, tx pgx.Tx, w Workflow, id string, cas *artifact.CAS) (verifier.Proof, error) {
	p := verifier.Proof{Sources: map[string]verifier.Source{}}
	// Authorize the ID before touching CAS or returning any reference bytes.
	var node string
	e := tx.QueryRow(ctx, `SELECT n.node_key FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id
 JOIN artifacts a ON a.artifact_id=t.result_artifact_id AND a.producer_task=t.task_id AND a.run_id=t.run_id
 WHERE n.run_id=$1 AND n.plan_version=$2 AND a.artifact_id=$3 AND a.kind=n.requirements->>'required_output_kind'
 AND t.state IN ('output_ready','verifying','succeeded') AND (a.source_plan_version=$2 OR (n.reuse_artifact_id=a.artifact_id AND a.source_plan_version<$2))`, w.RunID, w.ActivePlanVersion, id).Scan(&node)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, ErrPolicy
	}
	if e != nil {
		return p, e
	}
	if e = lockLifecycleResults(ctx, tx, w.RunID, w.ActivePlanVersion); e != nil {
		return p, e
	}
	p.Metadata, e = verifyMetadata(ctx, tx, w.RunID, id)
	if e != nil {
		return p, e
	}
	if e = validateTaskInputs(ctx, tx, w.RunID, w.ActivePlanVersion, p.Metadata.ProducerTaskID, node); e != nil {
		return p, e
	}
	if e = requireSettledModels(ctx, tx, TaskClaim{RunID: w.RunID, TaskID: p.Metadata.ProducerTaskID}); e != nil {
		return p, e
	}
	var body, hash string
	e = tx.QueryRow(ctx, `SELECT s.body,s.content_sha256 FROM artifact_contexts a JOIN context_snapshots s USING(run_id,snapshot_id) WHERE a.run_id=$1 AND a.artifact_id=$2`, w.RunID, id).Scan(&body, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, ErrStaleInput
	}
	if e != nil {
		return p, e
	}
	if json.Unmarshal([]byte(body), &p.Snapshot) != nil || p.Snapshot.Hash() != hash || p.Snapshot.RunID != w.RunID || p.Snapshot.TaskID != p.Metadata.ProducerTaskID || p.Snapshot.PlanVersion != p.Metadata.SourcePlanVersion {
		return p, ErrStaleInput
	}
	if p.Bytes, e = cas.Read(p.Metadata.Hash, p.Metadata.ByteLength); e != nil {
		return p, e
	}
	total := len(p.Bytes)
	for _, ref := range p.Snapshot.Artifacts {
		m, e := verifyMetadata(ctx, tx, w.RunID, ref.ID)
		if e != nil {
			return p, e
		}
		if m.Hash != ref.Hash || m.SourcePlanVersion != ref.SourcePlanVersion || m.ProducerTaskID != ref.ProducerTaskID || m.ToolVersion != ref.ToolVersion {
			return p, ErrStaleInput
		}
		source, e := cas.Read(m.Hash, m.ByteLength)
		if e != nil {
			return p, e
		}
		total += len(source)
		if total > 4*artifact.MaxBytes {
			return p, ErrPolicy
		}
		p.Sources[ref.ID] = verifier.Source{Metadata: m, Bytes: source}
	}
	return p, nil
}
func verifyMetadata(ctx context.Context, tx pgx.Tx, run, id string) (artifact.Metadata, error) {
	var a artifact.Metadata
	e := tx.QueryRow(ctx, `SELECT artifact_id,run_id,kind,content_sha256,storage_uri,schema_version,producer_task,source_plan_version,byte_length,content_type,tool_schema_version FROM artifacts WHERE run_id=$1 AND artifact_id=$2`, run, id).Scan(&a.ID, &a.RunID, &a.Kind, &a.Hash, &a.StorageURI, &a.SchemaVersion, &a.ProducerTaskID, &a.SourcePlanVersion, &a.ByteLength, &a.ContentType, &a.ToolVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, ErrPolicy
	}
	return a, e
}
