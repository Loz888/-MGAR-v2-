package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrCASConflict = errors.New("CAS_CONFLICT")
var ErrLeaseExpired = errors.New("LEASE_EXPIRED")
var ErrPolicy = errors.New("POLICY_DENIED")
var ErrBudget = errors.New("BUDGET_EXHAUSTED")
var ErrNoReady = errors.New("NO_READY_TASK")
var ErrUnsafeReplay = errors.New("UNSAFE_REPLAY")

type WorkerIdentity struct{ Owner, Role string }
type TaskClaim struct {
	TaskID      string    `json:"task_id"`
	RunID       string    `json:"run_id"`
	Owner       string    `json:"owner"`
	Role        string    `json:"role"`
	Epoch       int64     `json:"epoch"`
	Revision    int64     `json:"revision"`
	PlanVersion int       `json:"plan_version"`
	NodeKey     string    `json:"node_key"`
	LeaseUntil  time.Time `json:"lease_until"`
}

func ValidateWorker(id WorkerIdentity, ttl time.Duration) error {
	roles := map[string]bool{"research": true, "data": true, "action": true, "verify": true, "aggregate": true}
	if id.Owner == "" || len(id.Owner) > 128 || !roles[id.Role] || ttl <= 0 || ttl > 5*time.Minute {
		return ErrPolicy
	}
	return nil
}
func (s *Store) ClaimTask(ctx context.Context, run string, id WorkerIdentity, ttl time.Duration) (TaskClaim, error) {
	return s.claimTask(ctx, run, id, ttl, 0, "", "lease_or_ready")
}
func (s *Store) claimTask(ctx context.Context, run string, id WorkerIdentity, ttl time.Duration, expected int, selected, policy string) (TaskClaim, error) {
	var c TaskClaim
	if err := ValidateWorker(id, ttl); err != nil {
		return c, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	state, version, _, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return c, err
	}
	if expected > 0 && version != expected {
		return c, ErrFenced
	}
	if state != "running" {
		return c, ErrBadState
	}
	var used, max, running, parallel int
	if err = tx.QueryRow(ctx, "SELECT used_delegations,max_delegations,running_count,max_concurrency FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&used, &max, &running, &parallel); err != nil {
		return c, err
	}
	var taskState string
	var attempts, maxAttempts int
	c.RunID = run
	c.Owner = id.Owner
	c.Role = id.Role
	c.PlanVersion = version
	err = tx.QueryRow(ctx, "SELECT t.task_id,n.node_key,t.state,t.epoch,t.revision,t.attempts,t.max_attempts FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id AND n.run_id=t.run_id WHERE n.run_id=$1 AND n.plan_version=$2 AND t.role=$3 AND n.role=t.role AND t.attempts<t.max_attempts AND ($4::text='' OR (t.task_id::text=$4 AND t.state='ready')) AND (t.state='ready' OR (t.state IN ('claimed','executing') AND t.lease_until<=clock_timestamp())) ORDER BY t.task_id LIMIT 1 FOR UPDATE OF t SKIP LOCKED", run, version, id.Role, selected).Scan(&c.TaskID, &c.NodeKey, &taskState, &c.Epoch, &c.Revision, &attempts, &maxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNoReady
	}
	if err != nil {
		return c, err
	}
	if err = validateTaskInputs(ctx, tx, run, version, c.TaskID, c.NodeKey); err != nil {
		return c, err
	}
	first := taskState == "ready"
	if first && (used >= max || running >= parallel) {
		return c, ErrBudget
	}
	if !first {
		var unsafe bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE task_id=$1 AND status IN ('reserved','unknown')) OR EXISTS(SELECT 1 FROM tool_invocations WHERE task_id=$1 AND effect_class='non_idempotent' AND status IN ('reserved','unknown','succeeded'))", c.TaskID).Scan(&unsafe)
		if err != nil {
			return c, err
		}
		if unsafe {
			return c, ErrUnsafeReplay
		}
	}
	if first {
		if _, err = tx.Exec(ctx, "UPDATE run_budgets SET used_delegations=used_delegations+1,running_count=running_count+1 WHERE run_id=$1", run); err != nil {
			return c, err
		}
	}
	err = tx.QueryRow(ctx, "UPDATE task_instances SET state='claimed',owner=$2,epoch=epoch+1,revision=revision+1,attempts=attempts+1,lease_until=clock_timestamp()+$3*interval '1 second' WHERE task_id=$1 RETURNING epoch,revision,lease_until", c.TaskID, id.Owner, ttl.Seconds()).Scan(&c.Epoch, &c.Revision, &c.LeaseUntil)
	if err != nil {
		return c, err
	}
	if err = emit(ctx, tx, run, "task", c.TaskID, c.Revision, "task_claimed", map[string]any{"owner": id.Owner, "epoch": c.Epoch, "plan_version": version, "scheduling_policy": policy}); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s *Store) guardTask(ctx context.Context, c TaskClaim, budget bool) (pgx.Tx, string, error) {
	if e := ValidateWorker(WorkerIdentity{Owner: c.Owner, Role: c.Role}, time.Second); e != nil {
		return nil, "", e
	}
	if c.Epoch < 1 || c.Revision < 0 || c.PlanVersion < 1 {
		return nil, "", ErrFenced
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, "", e
	}
	fail := func(err error) (pgx.Tx, string, error) { tx.Rollback(ctx); return nil, "", err }
	state, version, _, e := lockWorkflow(ctx, tx, c.RunID)
	if e != nil {
		return fail(e)
	}
	if state != "running" {
		return fail(ErrBadState)
	}
	if version != c.PlanVersion {
		return fail(ErrFenced)
	}
	if budget {
		var id string
		if e = tx.QueryRow(ctx, "SELECT run_id FROM run_budgets WHERE run_id=$1 FOR UPDATE", c.RunID).Scan(&id); e != nil {
			return fail(e)
		}
	}
	var owner, role, planRole, taskState, nodeKey string
	var epoch, revision int64
	var live bool
	e = tx.QueryRow(ctx, "SELECT COALESCE(t.owner,''),t.role,n.role,t.state,t.epoch,t.revision,COALESCE(t.lease_until>clock_timestamp(),false),n.node_key FROM task_instances t JOIN plan_nodes n ON n.run_id=t.run_id AND n.task_instance_id=t.task_id AND n.plan_version=$3 WHERE t.run_id=$1 AND t.task_id=$2 FOR UPDATE OF t", c.RunID, c.TaskID, version).Scan(&owner, &role, &planRole, &taskState, &epoch, &revision, &live, &nodeKey)
	if errors.Is(e, pgx.ErrNoRows) {
		return fail(ErrFenced)
	}
	if e != nil {
		return fail(e)
	}
	if role != c.Role || planRole != role {
		return fail(ErrPolicy)
	}
	if owner != c.Owner || epoch != c.Epoch {
		return fail(ErrFenced)
	}
	if !live {
		return fail(ErrLeaseExpired)
	}
	if revision != c.Revision {
		return fail(ErrCASConflict)
	}
	if e = validateTaskInputs(ctx, tx, c.RunID, version, c.TaskID, nodeKey); e != nil {
		return fail(e)
	}
	return tx, taskState, nil
}

// validateTaskInputs rechecks the fixed snapshot against the actual current-plan
// dependencies. The caller already holds workflow, optional budget, and task locks.
func validateTaskInputs(ctx context.Context, tx pgx.Tx, run string, version int, task, nodeKey string) error {
	type dependency struct {
		Node, Task, State, Artifact, Kind, Reuse string
	}
	rows, err := tx.Query(ctx, `SELECT n.node_key,p.task_id,p.state,COALESCE(p.result_artifact_id::text,''),n.requirements->>'required_output_kind',COALESCE(n.reuse_artifact_id::text,'')
	 FROM plan_edges e JOIN plan_nodes n ON n.run_id=e.run_id AND n.plan_version=e.plan_version AND n.node_key=e.from_key
	 JOIN task_instances p ON p.run_id=n.run_id AND p.task_id=n.task_instance_id
	 WHERE e.run_id=$1 AND e.plan_version=$2 AND e.to_key=$3 ORDER BY p.result_artifact_id,p.task_id`, run, version, nodeKey)
	if err != nil {
		return err
	}
	parents := []dependency{}
	for rows.Next() {
		var p dependency
		if err = rows.Scan(&p.Node, &p.Task, &p.State, &p.Artifact, &p.Kind, &p.Reuse); err != nil {
			rows.Close()
			return err
		}
		parents = append(parents, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, p := range parents {
		if p.State != "succeeded" || p.Artifact == "" {
			return ErrStaleInput
		}
		// Verdict inserts take a foreign-key key-share lock on this artifact.
		// Lock first, then read the latest verdict in a fresh statement so a
		// verdict committed while this lock was waiting is also observed.
		var locked string
		err = tx.QueryRow(ctx, "SELECT artifact_id FROM artifacts WHERE run_id=$1 AND artifact_id=$2 FOR UPDATE", run, p.Artifact).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleInput
		}
		if err != nil {
			return err
		}
	}
	expected := map[string]inputRef{}
	for _, p := range parents {
		ref := inputRef{ID: p.Artifact, Producer: p.Node}
		var producer, kind string
		var verified bool
		err = tx.QueryRow(ctx, `SELECT a.content_sha256,a.source_plan_version,a.producer_task,a.kind,
		 COALESCE((SELECT v.passed FROM artifact_verdicts v WHERE v.artifact_id=a.artifact_id ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false)
		 FROM artifacts a WHERE a.run_id=$1 AND a.artifact_id=$2`, run, p.Artifact).
			Scan(&ref.Hash, &ref.Version, &producer, &kind, &verified)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleInput
		}
		if err != nil {
			return err
		}
		if !verified || producer != p.Task || (ref.Version != version && !(p.Reuse == ref.ID && ref.Version < version)) || kind != p.Kind {
			return ErrStaleInput
		}
		if _, duplicate := expected[ref.ID]; duplicate {
			return ErrStaleInput
		}
		expected[ref.ID] = ref
	}
	rows, err = tx.Query(ctx, "SELECT artifact_id,content_sha256,producer_node,source_plan_version FROM task_inputs WHERE run_id=$1 AND task_id=$2", run, task)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var have inputRef
		if err = rows.Scan(&have.ID, &have.Hash, &have.Producer, &have.Version); err != nil {
			return err
		}
		want, exists := expected[have.ID]
		if !exists || have != want {
			return ErrStaleInput
		}
		delete(expected, have.ID)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return ErrStaleInput
	}
	return nil
}

func (s *Store) StartTask(ctx context.Context, c TaskClaim) (TaskClaim, error) {
	tx, state, err := s.guardTask(ctx, c, false)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	if state != "claimed" {
		return c, ErrBadState
	}
	err = tx.QueryRow(ctx, "UPDATE task_instances SET state='executing',revision=revision+1 WHERE task_id=$1 AND lease_until>clock_timestamp() RETURNING revision", c.TaskID).Scan(&c.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrLeaseExpired
	}
	if err != nil {
		return c, err
	}
	if err = emit(ctx, tx, c.RunID, "task", c.TaskID, c.Revision, "task_executing", map[string]int64{"epoch": c.Epoch}); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s *Store) RenewTask(ctx context.Context, c TaskClaim, ttl time.Duration) (TaskClaim, error) {
	if err := ValidateWorker(WorkerIdentity{Owner: c.Owner, Role: c.Role}, ttl); err != nil {
		return c, err
	}
	tx, state, err := s.guardTask(ctx, c, false)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	if state != "claimed" && state != "executing" {
		return c, ErrBadState
	}
	err = tx.QueryRow(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()+$2*interval '1 second',revision=revision+1 WHERE task_id=$1 AND lease_until>clock_timestamp() RETURNING revision,lease_until", c.TaskID, ttl.Seconds()).Scan(&c.Revision, &c.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrLeaseExpired
	}
	if err != nil {
		return c, err
	}
	if err = emit(ctx, tx, c.RunID, "task", c.TaskID, c.Revision, "task_renewed", map[string]int64{"epoch": c.Epoch}); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s *Store) CommitOutput(ctx context.Context, c TaskClaim, artifact string) error {
	tx, state, err := s.guardTask(ctx, c, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return ErrBadState
	}
	if err = requireSettledModels(ctx, tx, c); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM artifacts a JOIN plan_nodes n ON n.task_instance_id=a.producer_task AND n.run_id=a.run_id AND n.plan_version=a.source_plan_version WHERE a.artifact_id=$1 AND a.run_id=$2 AND a.producer_task=$3 AND a.source_plan_version=$4 AND a.kind=n.requirements->>'required_output_kind')", artifact, c.RunID, c.TaskID, c.PlanVersion).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrPolicy
	}
	tag, err := tx.Exec(ctx, "UPDATE task_instances SET state='output_ready',result_artifact_id=$2,revision=revision+1 WHERE task_id=$1 AND lease_until>clock_timestamp()", c.TaskID, artifact)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseExpired
	}
	tag, err = tx.Exec(ctx, "UPDATE run_budgets SET running_count=running_count-1 WHERE run_id=$1 AND running_count>0", c.RunID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrBudget
	}
	if err = emit(ctx, tx, c.RunID, "task", c.TaskID, c.Revision+1, "output_ready", map[string]string{"artifact_id": artifact}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) TakeOrchestrator(ctx context.Context, run, owner string, ttl time.Duration) (OrchestratorClaim, error) {
	c := OrchestratorClaim{Owner: owner}
	if owner == "" || ttl <= 0 || ttl > 5*time.Minute {
		return c, ErrPolicy
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	state, _, _, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return c, err
	}
	if state == "succeeded" || state == "failed" || state == "cancelled" {
		return c, ErrBadState
	}
	var previous string
	var live bool
	err = tx.QueryRow(ctx, "SELECT owner_id,epoch,revision,lease_until>clock_timestamp() FROM orchestrator_leases WHERE run_id=$1 FOR UPDATE", run).Scan(&previous, &c.Epoch, &c.Revision, &live)
	if err == nil && live {
		if previous != owner {
			return c, ErrFenced
		}
		return c, tx.Commit(ctx)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		c.Epoch = 1
		c.Revision = 0
		_, err = tx.Exec(ctx, "INSERT INTO orchestrator_leases VALUES($1,$2,$3,clock_timestamp()+$4*interval '1 second',$5)", run, owner, c.Epoch, ttl.Seconds(), c.Revision)
	} else if err == nil {
		c.Epoch++
		c.Revision++
		_, err = tx.Exec(ctx, "UPDATE orchestrator_leases SET owner_id=$2,epoch=$3,revision=$4,lease_until=clock_timestamp()+$5*interval '1 second' WHERE run_id=$1", run, owner, c.Epoch, c.Revision, ttl.Seconds())
	}
	if err != nil {
		return c, err
	}
	if err = emit(ctx, tx, run, "orchestrator", run, c.Revision, "orchestrator_claimed", map[string]any{"owner": owner, "epoch": c.Epoch}); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s *Store) RenewOrchestrator(ctx context.Context, run string, c OrchestratorClaim, ttl time.Duration) (OrchestratorClaim, error) {
	if ttl <= 0 || ttl > 5*time.Minute {
		return c, ErrPolicy
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	state, _, _, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return c, err
	}
	if state == "succeeded" || state == "failed" || state == "cancelled" {
		return c, ErrBadState
	}
	if err = checkOrchestrator(ctx, tx, run, c); err != nil {
		return c, err
	}
	err = tx.QueryRow(ctx, "UPDATE orchestrator_leases SET lease_until=clock_timestamp()+$2*interval '1 second',revision=revision+1 WHERE run_id=$1 AND lease_until>clock_timestamp() RETURNING revision", run, ttl.Seconds()).Scan(&c.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrFenced
	}
	if err != nil {
		return c, err
	}
	if err = emit(ctx, tx, run, "orchestrator", run, c.Revision, "orchestrator_renewed", map[string]int64{"epoch": c.Epoch}); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
