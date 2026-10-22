package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/jackc/pgx/v5"
)

var ErrPlanConflict = errors.New("PLAN_CONFLICT")
var ErrFenced = errors.New("FENCED")
var ErrBadState = errors.New("BAD_STATE")

type OrchestratorClaim struct {
	Owner           string
	Epoch, Revision int64
}

func emit(ctx context.Context, tx pgx.Tx, run, entityType, entity string, revision int64, kind string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var event string
	if err = tx.QueryRow(ctx, "INSERT INTO workflow_events(run_id,entity_type,entity_id,entity_revision,kind,payload) VALUES($1,$2,$3,$4,$5,$6) RETURNING event_id", run, entityType, entity, revision, kind, body).Scan(&event); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO outbox_events(event_id,aggregate_type,aggregate_id,event_kind,payload) VALUES($1,$2,$3,$4,$5)", event, entityType, entity, kind, body)
	return err
}
func lockWorkflow(ctx context.Context, tx pgx.Tx, run string) (state string, version int, revision int64, err error) {
	err = tx.QueryRow(ctx, "SELECT state,active_plan_version,revision FROM workflow_runs WHERE run_id=$1 FOR UPDATE", run).Scan(&state, &version, &revision)
	return
}
func checkOrchestrator(ctx context.Context, tx pgx.Tx, run string, claim OrchestratorClaim) error {
	var owner string
	var epoch, revision int64
	var live bool
	err := tx.QueryRow(ctx, "SELECT owner_id,epoch,revision,lease_until>clock_timestamp() FROM orchestrator_leases WHERE run_id=$1 FOR UPDATE", run).Scan(&owner, &epoch, &revision, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if !live || owner != claim.Owner || epoch != claim.Epoch || revision != claim.Revision {
		return ErrFenced
	}
	return nil
}
func (s *Store) ProposeAndCommit(ctx context.Context, run string, claim OrchestratorClaim, p planner.Proposal) (int, error) {
	if err := planner.Validate(p); err != nil {
		return 0, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	state, version, revision, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return 0, err
	}
	if state != "planning" && state != "running" {
		return 0, ErrBadState
	}
	if version != p.BaseVersion {
		return 0, ErrPlanConflict
	}
	if err = checkOrchestrator(ctx, tx, run, claim); err != nil {
		return 0, err
	}
	next := version + 1
	body, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256(body)
	if _, err = tx.Exec(ctx, "INSERT INTO plans(run_id,plan_version,plan_hash,proposer,schema_version) VALUES($1,$2,$3,$4,$5)", run, next, hex.EncodeToString(hash[:]), claim.Owner, p.SchemaVersion); err != nil {
		return 0, err
	}
	for _, node := range p.Nodes {
		var id string
		if err = tx.QueryRow(ctx, "INSERT INTO task_instances(run_id,node_key,role) VALUES($1,$2,$3) RETURNING task_id", run, node.Key, node.Role).Scan(&id); err != nil {
			return 0, err
		}
		requirements, e := json.Marshal(node)
		if e != nil {
			return 0, e
		}
		if _, err = tx.Exec(ctx, "INSERT INTO plan_nodes(run_id,plan_version,node_key,task_instance_id,role,requirements) VALUES($1,$2,$3,$4,$5,$6)", run, next, node.Key, id, node.Role, requirements); err != nil {
			return 0, err
		}
	}
	for _, edge := range p.Edges {
		if _, err = tx.Exec(ctx, "INSERT INTO plan_edges VALUES($1,$2,$3,$4)", run, next, edge.From, edge.To); err != nil {
			return 0, err
		}
	}
	tag, err := tx.Exec(ctx, "UPDATE workflow_runs SET state='running',active_plan_version=$2,revision=revision+1 WHERE run_id=$1 AND revision=$3", run, next, revision)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, ErrPlanConflict
	}
	if err = emit(ctx, tx, run, "workflow", run, revision+1, "plan_committed", map[string]int{"plan_version": next}); err != nil {
		return 0, err
	}
	return next, tx.Commit(ctx)
}
