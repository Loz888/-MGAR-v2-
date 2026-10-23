package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"time"
)

var ErrAlreadyExists = errors.New("RUN_ALREADY_EXISTS")

type Workflow struct {
	RunID             string     `json:"run_id"`
	Goal              string     `json:"goal"`
	State             string     `json:"state"`
	ActivePlanVersion int        `json:"active_plan_version"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

const workflowColumns = "run_id,goal,state,active_plan_version,revision,created_at,finished_at"

func scanWorkflow(row pgx.Row) (Workflow, error) {
	var w Workflow
	err := row.Scan(&w.RunID, &w.Goal, &w.State, &w.ActivePlanVersion, &w.Revision, &w.CreatedAt, &w.FinishedAt)
	return w, err
}
func terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}
func (s *Store) Workflow(ctx context.Context, run string) (Workflow, error) {
	return scanWorkflow(s.Pool.QueryRow(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE run_id=$1", run))
}

func validateNewRun(goal string, maxDelegations int) error {
	if strings.TrimSpace(goal) == "" || len(goal) > 4096 || maxDelegations < 1 || maxDelegations > 1024 {
		return ErrPolicy
	}
	return nil
}

func createRunTx(ctx context.Context, tx pgx.Tx, id, goal string, maxDelegations int, initial *OrchestratorClaim, ttl time.Duration) (Workflow, error) {
	w, err := scanWorkflow(tx.QueryRow(ctx, "INSERT INTO workflow_runs(run_id,goal) VALUES(COALESCE(NULLIF($1,'')::uuid,gen_random_uuid()),$2) RETURNING "+workflowColumns, id, goal))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return w, ErrAlreadyExists
		}
		return w, err
	}
	if initial != nil {
		_, err = tx.Exec(ctx, "INSERT INTO orchestrator_leases(run_id,owner_id,epoch,lease_until,revision) VALUES($1,$2,$3,clock_timestamp()+$4*interval '1 second',$5)", w.RunID, initial.Owner, initial.Epoch, ttl.Seconds(), initial.Revision)
		if err != nil {
			return w, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO run_budgets(run_id,max_delegations) VALUES($1,$2)", w.RunID, maxDelegations); err != nil {
		return w, err
	}
	if err = emit(ctx, tx, w.RunID, "workflow", w.RunID, 0, "workflow_queued", map[string]string{"provider": "mock", "mode": "offline_control_plane"}); err != nil {
		return w, err
	}
	if initial != nil {
		if err = emit(ctx, tx, w.RunID, "orchestrator", w.RunID, initial.Revision, "orchestrator_claimed", map[string]any{"owner": initial.Owner, "epoch": initial.Epoch}); err != nil {
			return w, err
		}
	}
	return w, nil
}

func (s *Store) CreateRun(ctx context.Context, id, goal string, maxDelegations int) (Workflow, error) {
	var w Workflow
	if err := validateNewRun(goal, maxDelegations); err != nil {
		return w, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	w, err = createRunTx(ctx, tx, id, goal, maxDelegations, nil, 0)
	if err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}

// CreateRunClaimed publishes a new Run together with the submitting owner's
// initial lease. A daemon can never observe the Run before that lease exists.
func (s *Store) CreateRunClaimed(ctx context.Context, id, goal string, maxDelegations int, owner string, ttl time.Duration) (Workflow, OrchestratorClaim, error) {
	var w Workflow
	var c OrchestratorClaim
	if err := validateNewRun(goal, maxDelegations); err != nil {
		return w, c, err
	}
	if strings.TrimSpace(owner) == "" || ttl <= 0 || ttl > 5*time.Minute {
		return w, c, ErrPolicy
	}
	c = OrchestratorClaim{Owner: owner, Epoch: 1, Revision: 0}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return w, c, err
	}
	defer tx.Rollback(ctx)
	w, err = createRunTx(ctx, tx, id, goal, maxDelegations, &c, ttl)
	if err != nil {
		return w, c, err
	}
	return w, c, commitControl(ctx, tx, w.RunID, c)
}

func (s *Store) controlTx(ctx context.Context, run string, c OrchestratorClaim) (pgx.Tx, Workflow, error) {
	var w Workflow
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, w, err
	}
	fail := func(err error) (pgx.Tx, Workflow, error) { tx.Rollback(ctx); return nil, w, err }
	state, _, _, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return fail(err)
	}
	if terminal(state) {
		return fail(ErrBadState)
	}
	if err = checkOrchestrator(ctx, tx, run, c); err != nil {
		return fail(err)
	}
	w, err = scanWorkflow(tx.QueryRow(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE run_id=$1", run))
	if err != nil {
		return fail(err)
	}
	return tx, w, nil
}

func commitControl(ctx context.Context, tx pgx.Tx, run string, c OrchestratorClaim) error {
	// Locks and verdict reads can outlast the lease checked by controlTx.
	// Reject the entire transaction, including its audit rows, if it expired.
	if err := checkOrchestrator(ctx, tx, run, c); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockLifecycleRows(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Lock results before reading verdicts in another statement. Verdict inserts
// take FK key-share locks on artifacts, so FOR UPDATE serializes the decision
// with pending revocations. Lock budgets and tasks first because finalization
// can cancel remaining tasks and release their running reservations.
func lockLifecycleResults(ctx context.Context, tx pgx.Tx, run string, version int) error {
	var id string
	if err := tx.QueryRow(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&id); err != nil {
		return err
	}
	if err := lockLifecycleRows(ctx, tx, "SELECT task_id FROM task_instances WHERE run_id=$1 ORDER BY task_id FOR UPDATE", run); err != nil {
		return err
	}
	return lockLifecycleRows(ctx, tx, `SELECT a.artifact_id FROM artifacts a
	 JOIN task_instances t ON t.result_artifact_id=a.artifact_id AND t.run_id=a.run_id
	 JOIN plan_nodes n ON n.task_instance_id=t.task_id AND n.run_id=t.run_id
	 WHERE n.run_id=$1 AND n.plan_version=$2 ORDER BY a.artifact_id FOR UPDATE OF a`, run, version)
}

func setWorkflow(ctx context.Context, tx pgx.Tx, w Workflow, target, reason string, c *OrchestratorClaim) (Workflow, error) {
	if c != nil {
		if err := checkOrchestrator(ctx, tx, w.RunID, *c); err != nil {
			return w, err
		}
	}
	updated, err := scanWorkflow(tx.QueryRow(ctx, "UPDATE workflow_runs SET state=$2,revision=revision+1,finished_at=CASE WHEN $2 IN ('succeeded','failed','cancelled') THEN clock_timestamp() ELSE NULL END WHERE run_id=$1 AND revision=$3 RETURNING "+workflowColumns, w.RunID, target, w.Revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrCASConflict
	}
	if err != nil {
		return w, err
	}
	if err = emit(ctx, tx, w.RunID, "workflow", w.RunID, updated.Revision, "workflow_"+target, map[string]string{"reason": reason}); err != nil {
		return w, err
	}
	return updated, nil
}
func (s *Store) BeginPlanning(ctx context.Context, run string, c OrchestratorClaim) (Workflow, error) {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	if w.State != "queued" {
		return w, ErrBadState
	}
	w, err = setWorkflow(ctx, tx, w, "planning", "awaiting_validated_plan", &c)
	if err != nil {
		return w, err
	}
	return w, commitControl(ctx, tx, run, c)
}
func moveTask(ctx context.Context, tx pgx.Tx, run, id string, revision int64, target, reason string) error {
	tag, err := tx.Exec(ctx, "UPDATE task_instances SET state=$2,revision=revision+1 WHERE task_id=$1 AND revision=$3", id, target, revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCASConflict
	}
	return emit(ctx, tx, run, "task", id, revision+1, "task_"+target, map[string]string{"reason": reason})
}
func cancelRemaining(ctx context.Context, tx pgx.Tx, run string) error {
	var ignored string
	if err := tx.QueryRow(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&ignored); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT task_id,revision FROM task_instances WHERE run_id=$1 AND state NOT IN ('succeeded','failed','cancelled') ORDER BY task_id FOR UPDATE", run)
	if err != nil {
		return err
	}
	type item struct {
		id       string
		revision int64
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.revision); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if err = moveTask(ctx, tx, run, it.id, it.revision, "cancelled", "workflow_stopped"); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE run_budgets SET running_count=0 WHERE run_id=$1", run)
	return err
}
func (s *Store) CancelWorkflow(ctx context.Context, run string, expectedRevision int64) (Workflow, error) {
	var w Workflow
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	state, _, revision, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return w, err
	}
	if terminal(state) {
		return w, ErrBadState
	}
	if revision != expectedRevision {
		return w, ErrCASConflict
	}
	w, err = scanWorkflow(tx.QueryRow(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE run_id=$1", run))
	if err != nil {
		return w, err
	}
	if err = cancelRemaining(ctx, tx, run); err != nil {
		return w, err
	}
	w, err = setWorkflow(ctx, tx, w, "cancelled", "explicit_user_request", nil)
	if err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}
func (s *Store) InterruptWorkflow(ctx context.Context, run string, c OrchestratorClaim, state, reason string) (Workflow, error) {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	if state != "blocked" && state != "interrupted" {
		return w, ErrBadState
	}
	w, err = setWorkflow(ctx, tx, w, state, reason, &c)
	if err != nil {
		return w, err
	}
	return w, commitControl(ctx, tx, run, c)
}
func (s *Store) UnsafeRun(ctx context.Context, run string) (bool, error) {
	var unsafe bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE run_id=$1 AND status IN ('reserved','unknown')) OR EXISTS(SELECT 1 FROM tool_invocations v JOIN task_instances t ON t.task_id=v.task_id WHERE t.run_id=$1 AND v.effect_class='non_idempotent' AND v.status IN ('reserved','unknown','succeeded') AND t.state IN ('claimed','executing'))", run).Scan(&unsafe)
	return unsafe, err
}
func (s *Store) ResumeWorkflow(ctx context.Context, run string, c OrchestratorClaim) (Workflow, error) {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	if w.State != "blocked" && w.State != "interrupted" {
		return w, ErrBadState
	}
	var unsafe bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE run_id=$1 AND status IN ('reserved','unknown')) OR EXISTS(SELECT 1 FROM tool_invocations v JOIN task_instances t ON t.task_id=v.task_id WHERE t.run_id=$1 AND v.effect_class='non_idempotent' AND v.status IN ('reserved','unknown','succeeded') AND t.state IN ('claimed','executing'))", run).Scan(&unsafe); err != nil {
		return w, err
	}
	if unsafe {
		return w, ErrUnsafeReplay
	}
	var available bool
	if err = tx.QueryRow(ctx, "SELECT used_delegations<max_delegations OR running_count>0 FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&available); err != nil {
		return w, err
	}
	if !available {
		return w, ErrBudget
	}
	target := "running"
	if w.ActivePlanVersion == 0 {
		target = "planning"
	}
	w, err = setWorkflow(ctx, tx, w, target, "explicit_safe_resume", &c)
	if err != nil {
		return w, err
	}
	return w, commitControl(ctx, tx, run, c)
}
func (s *Store) RelinquishOrchestrator(ctx context.Context, run string, c OrchestratorClaim) error {
	tx, _, err := s.controlTx(ctx, run, c)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp(),revision=revision+1 WHERE run_id=$1 AND owner_id=$2 AND epoch=$3 AND revision=$4 AND lease_until>clock_timestamp()", run, c.Owner, c.Epoch, c.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrFenced
	}
	if err = emit(ctx, tx, run, "orchestrator", run, c.Revision+1, "orchestrator_released", map[string]int64{"epoch": c.Epoch}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ApplyVerdicts(ctx context.Context, run string, c OrchestratorClaim) error {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if w.State != "running" && w.State != "verifying" {
		return ErrBadState
	}
	if err = lockLifecycleResults(ctx, tx, run, w.ActivePlanVersion); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT t.task_id,t.state,t.revision,COALESCE((SELECT CASE WHEN v.passed THEN 1 ELSE -1 END FROM artifact_verdicts v JOIN artifacts a ON a.artifact_id=v.artifact_id WHERE a.artifact_id=t.result_artifact_id AND a.producer_task=t.task_id AND a.run_id=$1 AND (a.source_plan_version=$2 OR (n.reuse_artifact_id=a.artifact_id AND a.source_plan_version<$2)) ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),0) FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id WHERE n.run_id=$1 AND n.plan_version=$2 AND t.state IN ('output_ready','verifying') ORDER BY t.task_id FOR UPDATE OF t", run, w.ActivePlanVersion)
	if err != nil {
		return err
	}
	type item struct {
		id, state string
		revision  int64
		verdict   int
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.state, &it.revision, &it.verdict); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if it.verdict == 0 {
			continue
		}
		if err = checkOrchestrator(ctx, tx, run, c); err != nil {
			return err
		}
		if it.state == "output_ready" {
			if err = moveTask(ctx, tx, run, it.id, it.revision, "verifying", "trusted_verdict_present"); err != nil {
				return err
			}
			it.revision++
		}
		target := "failed"
		if it.verdict == 1 {
			target = "succeeded"
		}
		if err = moveTask(ctx, tx, run, it.id, it.revision, target, "independent_verdict"); err != nil {
			return err
		}
	}
	return commitControl(ctx, tx, run, c)
}
func (s *Store) FinalizeWorkflow(ctx context.Context, run string, c OrchestratorClaim) (Workflow, error) {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	if w.State != "running" && w.State != "verifying" {
		return w, ErrBadState
	}
	if err = lockLifecycleResults(ctx, tx, run, w.ActivePlanVersion); err != nil {
		return w, err
	}
	var total, done, bad int
	err = tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE t.state='succeeded' AND COALESCE((SELECT v.passed FROM artifact_verdicts v JOIN artifacts a ON a.artifact_id=v.artifact_id WHERE a.artifact_id=t.result_artifact_id AND a.run_id=$1 AND a.producer_task=t.task_id AND (a.source_plan_version=$2 OR (n.reuse_artifact_id=a.artifact_id AND a.source_plan_version<$2)) ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false)),count(*) FILTER(WHERE t.state IN ('failed','cancelled','blocked') OR (t.state='succeeded' AND NOT COALESCE((SELECT v.passed FROM artifact_verdicts v JOIN artifacts a ON a.artifact_id=v.artifact_id WHERE a.artifact_id=t.result_artifact_id AND a.run_id=$1 AND a.producer_task=t.task_id AND (a.source_plan_version=$2 OR (n.reuse_artifact_id=a.artifact_id AND a.source_plan_version<$2)) ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false))) FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id WHERE n.run_id=$1 AND n.plan_version=$2", run, w.ActivePlanVersion).Scan(&total, &done, &bad)
	if err != nil {
		return w, err
	}
	if total == 0 {
		return w, ErrBadState
	}
	target := ""
	if bad > 0 {
		target = "failed"
		if err = cancelRemaining(ctx, tx, run); err != nil {
			return w, err
		}
	} else if done == total {
		target = "verifying"
		if w.State == "verifying" {
			target = "succeeded"
		}
	}
	if target != "" {
		w, err = setWorkflow(ctx, tx, w, target, "current_plan_required_nodes_and_verdicts", &c)
		if err != nil {
			return w, err
		}
	}
	return w, commitControl(ctx, tx, run, c)
}
func (s *Store) RecoverExpired(ctx context.Context, run string, c OrchestratorClaim) error {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ignored string
	if err = tx.QueryRow(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&ignored); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT t.task_id,t.state,t.revision,t.attempts>=t.max_attempts,COALESCE(t.lease_until<=clock_timestamp(),false),EXISTS(SELECT 1 FROM plan_nodes n WHERE n.run_id=$1 AND n.plan_version=$2 AND n.task_instance_id=t.task_id) FROM task_instances t WHERE t.run_id=$1 AND t.state NOT IN ('succeeded','failed','cancelled') ORDER BY t.task_id FOR UPDATE", run, w.ActivePlanVersion)
	if err != nil {
		return err
	}
	type item struct {
		id, state                   string
		revision                    int64
		exhausted, expired, current bool
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.state, &it.revision, &it.exhausted, &it.expired, &it.current); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	released := 0
	for _, it := range items {
		target, reason := "", ""
		active := it.state == "claimed" || it.state == "executing"
		if !it.current {
			target = "cancelled"
			reason = "obsolete_plan_instance"
		} else if active && it.expired && it.exhausted {
			target = "failed"
			reason = "attempts_exhausted"
		}
		if target == "" {
			continue
		}
		var unsafe bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE task_id=$1 AND status IN ('reserved','unknown')) OR EXISTS(SELECT 1 FROM tool_invocations WHERE task_id=$1 AND effect_class='non_idempotent' AND status IN ('reserved','unknown','succeeded'))", it.id).Scan(&unsafe)
		if err != nil {
			return err
		}
		if unsafe {
			return ErrUnsafeReplay
		}
		if err = moveTask(ctx, tx, run, it.id, it.revision, target, reason); err != nil {
			return err
		}
		if active {
			released++
		}
	}
	if released > 0 {
		tag, e := tx.Exec(ctx, "UPDATE run_budgets SET running_count=running_count-$2 WHERE run_id=$1 AND running_count>=$2", run, released)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrBudget
		}
	}
	return commitControl(ctx, tx, run, c)
}

func (s *Store) BlockIfBudgetExhausted(ctx context.Context, run string, c OrchestratorClaim) (Workflow, error) {
	tx, w, err := s.controlTx(ctx, run, c)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	if w.State != "running" {
		return w, ErrBadState
	}
	var exhausted bool
	err = tx.QueryRow(ctx, "SELECT used_delegations>=max_delegations AND running_count=0 AND EXISTS(SELECT 1 FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id WHERE n.run_id=$1 AND n.plan_version=$2 AND t.state IN ('pending','ready')) FROM run_budgets WHERE run_id=$1 FOR UPDATE", run, w.ActivePlanVersion).Scan(&exhausted)
	if err != nil {
		return w, err
	}
	if exhausted {
		w, err = setWorkflow(ctx, tx, w, "blocked", "delegation_budget_exhausted", &c)
		if err != nil {
			return w, err
		}
	}
	return w, commitControl(ctx, tx, run, c)
}
