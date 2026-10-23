package store

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

// RecordReview loads the actual immutable verdict; no request may submit its
// own base verdict. This audit write cannot update artifact_verdicts or workflow
// success. Full prose is excluded from the event ledger; checks and digests are
// sufficient to match a separately retained reviewer response.
func (s *Store) RecordReview(ctx context.Context, run string, c OrchestratorClaim, verdictID string, raw []byte) (verifier.Review, error) {
	result := verifier.Review{}
	if !toolUUID.MatchString(verdictID) {
		return result, ErrPolicy
	}
	tx, _, e := s.controlTx(ctx, run, c)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	var body []byte
	var passed bool
	e = tx.QueryRow(ctx, `SELECT e.payload->'verdict',v.passed FROM artifact_verdicts v JOIN artifacts a ON a.artifact_id=v.artifact_id JOIN workflow_events e ON e.entity_id=v.verdict_id AND e.entity_type='verdict' AND e.kind='verdict_recorded' AND e.run_id=a.run_id WHERE v.verdict_id=$1 AND a.run_id=$2`, verdictID, run).Scan(&body, &passed)
	if e != nil {
		return result, ErrPolicy
	}
	var v verifier.Verdict
	if json.Unmarshal(body, &v) != nil || (v.Status == "passed") != passed {
		return result, ErrPolicy
	}
	result, e = verifier.AttachReview(v, raw)
	if e != nil {
		return result, e
	}
	var id string
	if e = tx.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&id); e != nil {
		return result, e
	}
	if e = emit(ctx, tx, run, "review", id, 0, "review_recorded", map[string]any{"verdict_id": verdictID, "artifact_id": v.ArtifactID, "deterministic_status": v.Status, "additional_checks": result.AdditionalChecks, "summary_sha256": result.SummaryHash, "review_sha256": result.ReviewHash}); e != nil {
		return result, e
	}
	return result, commitControl(ctx, tx, run, c)
}
