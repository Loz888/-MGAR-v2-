package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrResourceFull = errors.New("RESOURCE_QUEUE_FULL")
var ErrResourceDeadline = errors.New("RESOURCE_DEADLINE")
var ErrResourceUnconfirmed = errors.New("RESOURCE_STOP_UNCONFIRMED")

type ResourceTicket struct {
	ID, RunID, TaskID, Class string
	Token                    string `json:"-"`
	Deadline                 time.Time
}
type ResourcePolicy struct {
	Class                string
	Capacity, QueueLimit int
}

func validResource(class string) bool { return class == "llm" || class == "cpu" || class == "db" }
func (s *Store) ResourcePolicies(ctx context.Context) ([]ResourcePolicy, error) {
	rows, e := s.Pool.Query(ctx, "SELECT class,capacity,queue_limit FROM resource_pools ORDER BY class")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ResourcePolicy{}
	for rows.Next() {
		var p ResourcePolicy
		if e = rows.Scan(&p.Class, &p.Capacity, &p.QueueLimit); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) ConfigureResource(ctx context.Context, run string, c OrchestratorClaim, class string, capacity, queueLimit int) error {
	if !validResource(class) || capacity < 1 || capacity > 64 || queueLimit < 1 || queueLimit > 1024 {
		return ErrPolicy
	}
	tx, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if w.State != "running" && w.State != "planning" && w.State != "queued" {
		return ErrBadState
	}
	var old int
	if e = tx.QueryRow(ctx, "SELECT capacity FROM resource_pools WHERE class=$1 FOR UPDATE", class).Scan(&old); e != nil {
		return e
	}
	var active, queued int
	if e = tx.QueryRow(ctx, "SELECT count(*) FILTER(WHERE state IN ('held','unknown')),count(*) FILTER(WHERE state='queued' AND deadline>clock_timestamp()) FROM resource_requests WHERE class=$1", class).Scan(&active, &queued); e != nil {
		return e
	}
	if active > capacity || queued > queueLimit {
		return ErrResourceFull
	}
	if _, e = tx.Exec(ctx, "UPDATE resource_pools SET capacity=$2,queue_limit=$3 WHERE class=$1", class, capacity, queueLimit); e != nil {
		return e
	}
	var id string
	if e = tx.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&id); e != nil {
		return e
	}
	if e = emit(ctx, tx, run, "resource_config", id, 0, "resource_configured", ResourcePolicy{class, capacity, queueLimit}); e != nil {
		return e
	}
	return commitControl(ctx, tx, run, c)
}

const eligibleResource = `r.state='queued' AND r.deadline>statement_timestamp() AND EXISTS(SELECT 1 FROM task_instances t JOIN workflow_runs w ON w.run_id=t.run_id WHERE t.task_id=r.task_id AND t.run_id=r.run_id AND t.owner=r.owner AND t.epoch=r.epoch AND t.state='executing' AND t.lease_until>statement_timestamp() AND w.state='running' AND w.active_plan_version=r.plan_version)`

func (s *Store) EnqueueResource(ctx context.Context, c TaskClaim, class string, priority int, ttl time.Duration) (ResourceTicket, error) {
	out := ResourceTicket{RunID: c.RunID, TaskID: c.TaskID, Class: class}
	if !validResource(class) || priority < 0 || priority > 10 || ttl < time.Millisecond || ttl > 30*time.Second {
		return out, ErrPolicy
	}
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return out, e
	}
	out.Token = hex.EncodeToString(b[:])
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return ResourceTicket{}, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return ResourceTicket{}, ErrBadState
	}
	var limit, queued int
	if e = tx.QueryRow(ctx, "SELECT queue_limit FROM resource_pools WHERE class=$1 FOR UPDATE", class).Scan(&limit); e != nil {
		return ResourceTicket{}, e
	}
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM resource_requests r WHERE r.class=$1 AND "+eligibleResource, class).Scan(&queued); e != nil {
		return ResourceTicket{}, e
	}
	if queued >= limit {
		return ResourceTicket{}, ErrResourceFull
	}
	e = tx.QueryRow(ctx, `INSERT INTO resource_requests(run_id,task_id,owner,epoch,plan_version,class,token_hash,priority,state,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'queued',clock_timestamp()+$9*interval '1 second') RETURNING request_id,deadline`, c.RunID, c.TaskID, c.Owner, c.Epoch, c.PlanVersion, class, artifact.Digest([]byte(out.Token)), priority, ttl.Seconds()).Scan(&out.ID, &out.Deadline)
	if e != nil {
		return ResourceTicket{}, e
	}
	if e = emit(ctx, tx, c.RunID, "resource", out.ID, 0, "resource_queued", map[string]any{"task_id": c.TaskID, "class": class, "priority": priority, "deadline": out.Deadline}); e != nil {
		return ResourceTicket{}, e
	}
	if e = commitModelTask(ctx, tx, c); e != nil {
		return ResourceTicket{}, e
	}
	return out, nil
}
func (s *Store) AcquireResource(ctx context.Context, c TaskClaim, r ResourceTicket) (bool, error) {
	if !toolUUID.MatchString(r.ID) || !toolHash.MatchString(r.Token) || r.RunID != c.RunID || r.TaskID != c.TaskID || !validResource(r.Class) {
		return false, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return false, ErrBadState
	}
	var capacity int
	if e = tx.QueryRow(ctx, "SELECT capacity FROM resource_pools WHERE class=$1 FOR UPDATE", r.Class).Scan(&capacity); e != nil {
		return false, e
	}
	var status string
	var revision int64
	var expired bool
	e = tx.QueryRow(ctx, "SELECT state,revision,deadline<=clock_timestamp() FROM resource_requests WHERE request_id=$1 AND run_id=$2 AND task_id=$3 AND class=$4 AND token_hash=$5 AND owner=$6 AND epoch=$7 AND plan_version=$8 FOR UPDATE", r.ID, c.RunID, c.TaskID, r.Class, artifact.Digest([]byte(r.Token)), c.Owner, c.Epoch, c.PlanVersion).Scan(&status, &revision, &expired)
	if e != nil {
		return false, ErrPolicy
	}
	if status != "queued" {
		return false, ErrBadState
	}
	if expired {
		if e = moveResource(ctx, tx, r, revision, "expired"); e != nil {
			return false, e
		}
		if e = commitModelTask(ctx, tx, c); e != nil {
			return false, e
		}
		return false, ErrResourceDeadline
	}
	var held int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM resource_requests WHERE class=$1 AND state IN ('held','unknown')", r.Class).Scan(&held); e != nil {
		return false, e
	}
	if held >= capacity {
		return false, commitModelTask(ctx, tx, c)
	}
	var head string
	e = tx.QueryRow(ctx, "SELECT r.request_id FROM resource_requests r WHERE r.class=$1 AND "+eligibleResource+" ORDER BY r.priority+floor(extract(epoch FROM statement_timestamp()-r.created_at)*10) DESC,r.created_at,r.request_id LIMIT 1", r.Class).Scan(&head)
	if e != nil {
		return false, e
	}
	if head != r.ID {
		return false, commitModelTask(ctx, tx, c)
	}
	if e = moveResource(ctx, tx, r, revision, "held"); e != nil {
		return false, e
	}
	return true, commitModelTask(ctx, tx, c)
}
func moveResource(ctx context.Context, tx pgx.Tx, r ResourceTicket, revision int64, state string) error {
	if _, e := tx.Exec(ctx, "UPDATE resource_requests SET state=$2,revision=revision+1 WHERE request_id=$1", r.ID, state); e != nil {
		return e
	}
	return emit(ctx, tx, r.RunID, "resource", r.ID, revision+1, "resource_"+state, map[string]string{"task_id": r.TaskID, "class": r.Class, "state": state})
}

// FinishResource is called by the trusted host after observing executor stop.
// It permits release after task cancellation/lease expiry but requires the
// private ticket credential. Unconfirmed stop retains capacity as unknown.
func (s *Store) FinishResource(ctx context.Context, r ResourceTicket, stopped bool) error {
	if !toolUUID.MatchString(r.ID) || !toolHash.MatchString(r.Token) || !validResource(r.Class) {
		return ErrPolicy
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, _, _, e = lockWorkflow(ctx, tx, r.RunID); e != nil {
		return e
	}
	var capacity int
	if e = tx.QueryRow(ctx, "SELECT capacity FROM resource_pools WHERE class=$1 FOR UPDATE", r.Class).Scan(&capacity); e != nil {
		return e
	}
	var state string
	var rev int64
	e = tx.QueryRow(ctx, "SELECT state,revision FROM resource_requests WHERE request_id=$1 AND run_id=$2 AND task_id=$3 AND class=$4 AND token_hash=$5 FOR UPDATE", r.ID, r.RunID, r.TaskID, r.Class, artifact.Digest([]byte(r.Token))).Scan(&state, &rev)
	if e != nil {
		return ErrPolicy
	}
	target := "cancelled"
	switch state {
	case "released", "cancelled", "expired":
		return tx.Commit(ctx)
	case "queued":
	case "held", "unknown":
		if !stopped {
			if state == "unknown" {
				return tx.Commit(ctx)
			}
			target = "unknown"
		} else {
			target = "released"
		}
	default:
		return ErrBadState
	}
	if e = moveResource(ctx, tx, r, rev, target); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// ConfirmResourceStopped is an explicit controller attestation after independent
// executor cleanup. It is not a timer-based reclaim and performs no cleanup
// itself. The evidence digest is audited; an Agent cannot provide this authority.
// It also permits recovery when a crashed host lost its private ticket token.
func (s *Store) ConfirmResourceStopped(ctx context.Context, run string, c OrchestratorClaim, id, proofDigest string) error {
	if !toolUUID.MatchString(id) || !toolHash.MatchString(proofDigest) {
		return ErrPolicy
	}
	tx, _, e := s.controlTx(ctx, run, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	r := ResourceTicket{ID: id, RunID: run}
	if e = tx.QueryRow(ctx, "SELECT class,task_id FROM resource_requests WHERE request_id=$1 AND run_id=$2", id, run).Scan(&r.Class, &r.TaskID); e != nil {
		return ErrPolicy
	}
	var capacity int
	if e = tx.QueryRow(ctx, "SELECT capacity FROM resource_pools WHERE class=$1 FOR UPDATE", r.Class).Scan(&capacity); e != nil {
		return e
	}
	var state string
	var revision int64
	if e = tx.QueryRow(ctx, "SELECT state,revision FROM resource_requests WHERE request_id=$1 FOR UPDATE", id).Scan(&state, &revision); e != nil {
		return e
	}
	if state != "held" && state != "unknown" {
		return ErrBadState
	}
	if _, e = tx.Exec(ctx, "UPDATE resource_requests SET state='released',revision=revision+1 WHERE request_id=$1", id); e != nil {
		return e
	}
	if e = emit(ctx, tx, run, "resource", id, revision+1, "resource_released", map[string]string{"task_id": r.TaskID, "class": r.Class, "state": "released", "stop_proof_sha256": proofDigest, "authority": "controller_attestation"}); e != nil {
		return e
	}
	return commitControl(ctx, tx, run, c)
}
