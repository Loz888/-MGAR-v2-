package store

import (
	"context"
	"time"
)

// OrchestratorCheckpoint binds one reviewed, unstarted allocation to its original
// lease. This is not a general resume/force operation.
type OrchestratorCheckpoint struct {
	Goal            string
	Owner           string
	Epoch, Revision int64
	LeaseUntil      time.Time
}

// TakeOrchestratorOnce performs the reviewed epoch 1 -> 2 CAS. It cannot renew,
// create a missing lease, or acquire epoch 3 after a crash. The ordinary lease
// API is unchanged. The caller must first establish its external zero-send proof.
func (s *Store) TakeOrchestratorOnce(ctx context.Context, run string, previous OrchestratorCheckpoint, owner string, ttl time.Duration) (OrchestratorClaim, error) {
	c := OrchestratorClaim{Owner: owner, Epoch: 2, Revision: 1}
	if previous.Goal == "" || previous.Owner == "" || previous.Epoch != 1 || previous.Revision != 0 || previous.LeaseUntil.IsZero() || owner == "" || ttl <= 0 || ttl > 5*time.Minute {
		return c, ErrPolicy
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	state, version, revision, e := lockWorkflow(ctx, tx, run)
	if e != nil {
		return c, e
	}
	if state != "queued" || version != 0 || revision != 0 {
		return c, ErrBadState
	}
	var goal string
	if e = tx.QueryRow(ctx, "SELECT goal FROM workflow_runs WHERE run_id=$1", run).Scan(&goal); e != nil || goal != previous.Goal {
		return c, ErrFenced
	}
	// Recheck the authoritative marker's zero usage under the same row lock.
	b, e := BudgetReportTx(ctx, tx, run)
	if e != nil {
		return c, e
	}
	if !b.Conserved || b.ReservedModels+b.SettledModels+b.ReservedTools+b.SettledTools+b.ReservedTokens+b.SettledTokens+b.ReservedCostMicros+b.SettledCostMicros+b.RunningTasks+int64(b.Delegations) != 0 || b.Limits.ModelCalls != 0 || b.Limits.Tokens != 0 || b.Limits.CostMicros != 0 {
		return c, ErrUnsafeReplay
	}
	tag, e := tx.Exec(ctx, `UPDATE orchestrator_leases SET owner_id=$2,epoch=2,revision=1,lease_until=clock_timestamp()+$3*interval '1 second'
 WHERE run_id=$1 AND owner_id=$4 AND epoch=1 AND revision=0 AND lease_until=$5 AND lease_until<=clock_timestamp()`, run, owner, ttl.Seconds(), previous.Owner, previous.LeaseUntil)
	if e != nil {
		return c, e
	}
	if tag.RowsAffected() != 1 {
		return c, ErrFenced
	}
	if e = emit(ctx, tx, run, "orchestrator", run, 1, "orchestrator_claimed", map[string]any{"owner": owner, "epoch": 2, "recovery": "approved_zero_send_once"}); e != nil {
		return c, e
	}
	return c, commitControl(ctx, tx, run, c)
}
