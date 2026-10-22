package scheduler

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"time"
)

// RecoverRun reconstructs scheduling from PostgreSQL. It never executes an
// Agent, tool, model call or unverified artifact as a side effect of recovery.
func (s Scheduler) RecoverRun(ctx context.Context, run, owner string) (store.Workflow, error) {
	w, err := s.Store.Workflow(ctx, run)
	if err != nil {
		return w, err
	}
	switch w.State {
	case "succeeded", "failed", "cancelled", "blocked", "interrupted":
		return w, nil
	}
	c, err := s.Store.TakeOrchestrator(ctx, run, owner, 30*time.Second)
	if err != nil {
		return w, err
	}
	if s.AfterOrchestratorClaim != nil {
		s.AfterOrchestratorClaim(run)
	}
	c, err = s.Store.RenewOrchestrator(ctx, run, c, 30*time.Second)
	if err != nil {
		return w, err
	}
	if w.State == "queued" {
		w, err = s.Store.BeginPlanning(ctx, run, c)
		if err != nil {
			return w, err
		}
	}
	if w.ActivePlanVersion == 0 {
		return w, nil
	}
	unsafe, err := s.Store.UnsafeRun(ctx, run)
	if err != nil {
		return w, err
	}
	if unsafe {
		return s.Store.InterruptWorkflow(ctx, run, c, "interrupted", "unknown_or_non_idempotent_side_effect")
	}
	if err = s.Store.RecoverExpired(ctx, run, c); errors.Is(err, store.ErrUnsafeReplay) {
		return s.Store.InterruptWorkflow(ctx, run, c, "interrupted", "unknown_or_non_idempotent_side_effect")
	} else if err != nil {
		return w, err
	}
	if err = s.Store.ApplyVerdicts(ctx, run, c); err != nil {
		return w, err
	}
	if w.State == "running" {
		if _, err = s.GetReady(ctx, run, c); err != nil {
			return w, err
		}
	}
	w, err = s.Store.FinalizeWorkflow(ctx, run, c)
	if err != nil {
		return w, err
	}
	if w.State == "running" {
		return s.Store.BlockIfBudgetExhausted(ctx, run, c)
	}
	return w, nil
}
func (s Scheduler) RecoverAll(ctx context.Context, owner string) error {
	rows, err := s.Store.Pool.Query(ctx, "SELECT run_id FROM workflow_runs WHERE state IN ('queued','planning','running','verifying') ORDER BY created_at,run_id")
	if err != nil {
		return err
	}
	runs := []string{}
	for rows.Next() {
		var run string
		if err = rows.Scan(&run); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, run := range runs {
		if _, err = s.RecoverRun(ctx, run, owner); err != nil && !errors.Is(err, store.ErrFenced) && !errors.Is(err, store.ErrBadState) {
			return err
		}
	}
	return nil
}
