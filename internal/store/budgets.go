package store

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ConfigureBudget(ctx context.Context, run string, c OrchestratorClaim, l budget.Limits) error {
	if !l.Valid() {
		return ErrPolicy
	}
	tx, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if w.State != "planning" && w.State != "running" && w.State != "queued" {
		return ErrBadState
	}
	tag, e := tx.Exec(ctx, `UPDATE run_budgets SET max_model_calls=$2,max_tool_calls=$3,max_tokens_est=$4,max_cost_usd=$5::numeric,max_call_concurrency=$6,max_concurrency=$7,max_delegations=$8
 WHERE run_id=$1 AND reserved_model_calls+settled_model_calls<=$2 AND reserved_tool_calls+settled_tool_calls<=$3 AND reserved_tokens+settled_tokens<=$4 AND reserved_cost+settled_cost<=$5::numeric AND reserved_model_calls+reserved_tool_calls<=$6 AND running_count<=$7 AND used_delegations<=$8`, run, l.ModelCalls, l.ToolCalls, l.Tokens, budget.Decimal(l.CostMicros), l.ConcurrentCalls, l.TaskConcurrency, l.Delegations)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrBudget
	}
	var id string
	if e = tx.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&id); e != nil {
		return e
	}
	if e = emit(ctx, tx, run, "budget_config", id, 0, "budget_configured", l); e != nil {
		return e
	}
	return commitControl(ctx, tx, run, c)
}

// BudgetReport uses one repeatable-read snapshot. Reads never repair drift.
// Aggregate equality is checked against independently summed ledger entries.
func (s *Store) BudgetReport(ctx context.Context, run string) (budget.Report, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return budget.Report{}, e
	}
	defer tx.Rollback(ctx)
	r, e := BudgetReportTx(ctx, tx, run)
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

// BudgetReportTx reuses the caller's consistent read-only snapshot; never repairs.
func BudgetReportTx(ctx context.Context, tx pgx.Tx, run string) (budget.Report, error) {
	r := budget.Report{RunID: run}
	var e error
	e = tx.QueryRow(ctx, `SELECT max_model_calls,max_tool_calls,max_tokens_est,(max_cost_usd*1000000)::bigint,max_call_concurrency,max_concurrency,max_delegations,
 reserved_model_calls,settled_model_calls,reserved_tool_calls,settled_tool_calls,reserved_tokens,settled_tokens,(reserved_cost*1000000)::bigint,(settled_cost*1000000)::bigint,running_count,used_delegations FROM run_budgets WHERE run_id=$1`, run).Scan(&r.Limits.ModelCalls, &r.Limits.ToolCalls, &r.Limits.Tokens, &r.Limits.CostMicros, &r.Limits.ConcurrentCalls, &r.Limits.TaskConcurrency, &r.Limits.Delegations, &r.ReservedModels, &r.SettledModels, &r.ReservedTools, &r.SettledTools, &r.ReservedTokens, &r.SettledTokens, &r.ReservedCostMicros, &r.SettledCostMicros, &r.RunningTasks, &r.Delegations)
	if e != nil {
		return r, e
	}
	var models, tokens, cost, settledTokens, settledCost, tools int64
	var malformed bool
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='reserved'),count(*) FILTER(WHERE status='unknown'),count(*) FILTER(WHERE status IN ('succeeded','failed')),
 COALESCE(sum((reservation->>'tokens')::bigint) FILTER(WHERE status IN ('reserved','unknown')),0)::bigint,
 COALESCE(sum((estimate_bound*1000000)::bigint) FILTER(WHERE status IN ('reserved','unknown')),0)::bigint,
 COALESCE(sum((usage->>'tokens')::bigint) FILTER(WHERE status IN ('succeeded','failed')),0)::bigint,
 COALESCE(sum((usage->>'cost_micros')::bigint) FILTER(WHERE status IN ('succeeded','failed')),0)::bigint,
 COALESCE(bool_or((status IN ('reserved','unknown') AND (usage_known OR usage<>'{}'::jsonb)) OR (status IN ('succeeded','failed') AND NOT usage_known)),false)
 FROM model_calls WHERE run_id=$1`, run).Scan(&r.PendingModels, &r.UnknownModels, &models, &tokens, &cost, &settledTokens, &settledCost, &malformed)
	if e != nil {
		return r, e
	}
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE v.status='reserved'),count(*) FILTER(WHERE v.status='unknown'),count(*) FILTER(WHERE v.status IN ('succeeded','failed')) FROM tool_invocations v JOIN task_instances t USING(task_id) WHERE t.run_id=$1`, run).Scan(&r.PendingTools, &r.UnknownTools, &tools)
	if e != nil {
		return r, e
	}
	var activeTasks, delegated int64
	if e = tx.QueryRow(ctx, "SELECT count(*) FILTER(WHERE state IN ('claimed','executing')),count(*) FILTER(WHERE attempts>0) FROM task_instances WHERE run_id=$1", run).Scan(&activeTasks, &delegated); e != nil {
		return r, e
	}
	r.Conserved = activeTasks == r.RunningTasks && delegated == r.Delegations && !malformed && r.ReservedModels == r.PendingModels+r.UnknownModels && r.SettledModels == models && r.ReservedTools == r.PendingTools+r.UnknownTools && r.SettledTools == tools && r.ReservedTokens == tokens && r.ReservedCostMicros == cost && r.SettledTokens == settledTokens && r.SettledCostMicros == settledCost
	return r, nil
}
