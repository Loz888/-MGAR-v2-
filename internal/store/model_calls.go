package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/jackc/pgx/v5"
)

// ReserveModelCall commits the ledger and all upper bounds BEFORE transport.
// Callers must not send on error, including an ambiguous commit error.
func (s *Store) ReserveModelCall(ctx context.Context, c TaskClaim, r budget.ModelRequest) (budget.ModelCall, error) {
	out := budget.ModelCall{}
	if !r.Valid() {
		return out, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return out, ErrBadState
	}
	var snapshot, unsafe bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM context_snapshots WHERE run_id=$1 AND task_id=$2 AND plan_version=$3),
 EXISTS(SELECT 1 FROM model_calls WHERE run_id=$1 AND (logical_call_id=$4 OR (task_id=$2 AND status IN ('reserved','unknown'))))`, c.RunID, c.TaskID, c.PlanVersion, r.LogicalID).Scan(&snapshot, &unsafe); e != nil {
		return out, e
	}
	if !snapshot {
		return out, ErrStaleInput
	}
	if unsafe {
		return out, ErrUnsafeReplay
	}
	tag, e := tx.Exec(ctx, `UPDATE run_budgets SET reserved_model_calls=reserved_model_calls+1,reserved_tokens=reserved_tokens+$2,reserved_cost=reserved_cost+$3::numeric
 WHERE run_id=$1 AND reserved_model_calls+settled_model_calls<max_model_calls AND $2<=max_tokens_est-reserved_tokens-settled_tokens AND $3::numeric<=max_cost_usd-reserved_cost-settled_cost`, c.RunID, r.Tokens, budget.Decimal(r.CostMicros))
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		return out, ErrBudget
	}
	out.Reservation = budget.ModelReservation{Schema: "model_reservation.v1", Owner: c.Owner, Epoch: c.Epoch, Tokens: r.Tokens, CostMicros: r.CostMicros}
	body, e := json.Marshal(out.Reservation)
	if e != nil {
		return out, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO model_calls(logical_call_id,run_id,task_id,provider,model,status,estimate_bound,reservation)
 VALUES($1,$2,$3,$4,$5,'reserved',$6::numeric,$7) RETURNING call_id`, r.LogicalID, c.RunID, c.TaskID, r.Provider, r.Model, budget.Decimal(r.CostMicros), body).Scan(&out.ID)
	if e != nil {
		return budget.ModelCall{}, e
	}
	if e = emit(ctx, tx, c.RunID, "model_call", out.ID, 0, "model_reserved", map[string]any{"task_id": c.TaskID, "request": r, "reservation": out.Reservation}); e != nil {
		return budget.ModelCall{}, e
	}
	if e = commitModelTask(ctx, tx, c); e != nil {
		return budget.ModelCall{}, e
	}
	return out, nil
}

// FinishModelCall accepts only trusted transport accounting. Unknown usage is
// stored as {}, never as an observed zero, and retains the entire reservation.
func (s *Store) FinishModelCall(ctx context.Context, c TaskClaim, id string, u budget.ModelUsage) error {
	if !u.Valid() {
		return ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return ErrBadState
	}
	var status string
	var raw []byte
	e = tx.QueryRow(ctx, "SELECT status,reservation FROM model_calls WHERE call_id=$1 AND run_id=$2 AND task_id=$3 FOR UPDATE", id, c.RunID, c.TaskID).Scan(&status, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrPolicy
	}
	if e != nil {
		return e
	}
	if status != "reserved" {
		return ErrBadState
	}
	var r budget.ModelReservation
	if json.Unmarshal(raw, &r) != nil || !r.Valid() {
		return ErrPolicy
	}
	if r.Owner != c.Owner || r.Epoch != c.Epoch {
		return ErrFenced
	}
	if u.Known {
		if u.Tokens > r.Tokens || u.CostMicros > r.CostMicros {
			return ErrBudget
		}
		tag, e := tx.Exec(ctx, `UPDATE run_budgets SET reserved_model_calls=reserved_model_calls-1,settled_model_calls=settled_model_calls+1,
 reserved_tokens=reserved_tokens-$2,settled_tokens=settled_tokens+$3,reserved_cost=reserved_cost-$4::numeric,settled_cost=settled_cost+$5::numeric
 WHERE run_id=$1 AND reserved_model_calls>=1 AND reserved_tokens>=$2 AND reserved_cost>=$4::numeric`, c.RunID, r.Tokens, u.Tokens, budget.Decimal(r.CostMicros), budget.Decimal(u.CostMicros))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrBudget
		}
		body, e := json.Marshal(u)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE model_calls SET status=$2,usage=$3,usage_known=true,usage_source=$4,settlement=$3 WHERE call_id=$1", id, u.Status, body, u.Source); e != nil {
			return e
		}
	} else {
		if _, e = tx.Exec(ctx, "UPDATE model_calls SET status='unknown',usage='{}',usage_known=false,usage_source='conservative_bound' WHERE call_id=$1", id); e != nil {
			return e
		}
	}
	if e = emit(ctx, tx, c.RunID, "model_call", id, 1, "model_"+u.Status, map[string]any{"usage_known": u.Known, "reason": "transport_result"}); e != nil {
		return e
	}
	return commitModelTask(ctx, tx, c)
}
func commitModelTask(ctx context.Context, tx pgx.Tx, c TaskClaim) error {
	var live bool
	if e := tx.QueryRow(ctx, "SELECT lease_until>clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&live); e != nil {
		return e
	}
	if !live {
		return ErrLeaseExpired
	}
	return tx.Commit(ctx)
}

// The workflow lock held by the caller serializes this check with reservation
// and settlement. An uncertain model result cannot become task output.
func requireSettledModels(ctx context.Context, tx pgx.Tx, c TaskClaim) error {
	var unsafe bool
	if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE run_id=$1 AND task_id=$2 AND status IN ('reserved','unknown'))", c.RunID, c.TaskID).Scan(&unsafe); e != nil {
		return e
	}
	if unsafe {
		return ErrUnsafeReplay
	}
	return nil
}

// ReconcileCalls uses the existing ledger and lock order, without transport or
// schema changes. A live reservation waits; an abandoned one becomes unknown.
func (s *Store) ReconcileCalls(ctx context.Context, run, owner string, epoch, revision int64) (budget.CallRecovery, error) {
	result := budget.CallRecovery{}
	c := OrchestratorClaim{Owner: owner, Epoch: epoch, Revision: revision}
	tx, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	var locked string
	if e = tx.QueryRow(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&locked); e != nil {
		return result, e
	}
	if e = lockLifecycleRows(ctx, tx, "SELECT task_id FROM task_instances WHERE run_id=$1 ORDER BY task_id FOR UPDATE", run); e != nil {
		return result, e
	}
	rows, e := tx.Query(ctx, `SELECT m.call_id,m.status,m.reservation,COALESCE(t.owner,''),t.epoch,
 t.state IN ('claimed','executing') AND COALESCE(t.lease_until>clock_timestamp(),false) AND EXISTS(SELECT 1 FROM plan_nodes n WHERE n.run_id=$1 AND n.plan_version=$2 AND n.task_instance_id=t.task_id)
 FROM model_calls m JOIN task_instances t ON t.task_id=m.task_id AND t.run_id=m.run_id WHERE m.run_id=$1 AND m.status IN ('reserved','unknown') ORDER BY m.call_id FOR UPDATE OF m`, run, w.ActivePlanVersion)
	if e != nil {
		return result, e
	}
	type call struct {
		id, status, owner string
		raw               []byte
		epoch             int64
		live              bool
	}
	calls := []call{}
	for rows.Next() {
		var it call
		if e = rows.Scan(&it.id, &it.status, &it.raw, &it.owner, &it.epoch, &it.live); e != nil {
			rows.Close()
			return result, e
		}
		calls = append(calls, it)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return result, e
	}
	for _, it := range calls {
		if it.status == "unknown" {
			result.Unknown++
			continue
		}
		var r budget.ModelReservation
		valid := json.Unmarshal(it.raw, &r) == nil && r.Valid() && r.Owner == it.owner && r.Epoch == it.epoch
		if it.live && valid {
			result.Pending++
			continue
		}
		if _, e = tx.Exec(ctx, "UPDATE model_calls SET status='unknown',usage='{}',usage_known=false,usage_source='conservative_bound' WHERE call_id=$1", it.id); e != nil {
			return result, e
		}
		if e = emit(ctx, tx, run, "model_call", it.id, 1, "model_unknown", map[string]any{"usage_known": false, "reason": "abandoned_or_unproven_reservation"}); e != nil {
			return result, e
		}
		result.Unknown++
	}
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tool_invocations v JOIN task_instances t ON t.task_id=v.task_id WHERE t.run_id=$1 AND v.effect_class='non_idempotent' AND v.status IN ('reserved','unknown','succeeded') AND t.state IN ('claimed','executing'))`, run).Scan(&result.UnsafeTool); e != nil {
		return result, e
	}
	if e = commitControl(ctx, tx, run, c); e != nil {
		return budget.CallRecovery{}, e
	}
	return result, nil
}
