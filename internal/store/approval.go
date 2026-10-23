package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/jackc/pgx/v5"
)

var ErrApprovalFinal = errors.New("APPROVAL_DENIED_EXPIRED_OR_CONSUMED")
var ErrApprovalExpired = errors.New("APPROVAL_EXPIRED")

type ApprovalScope struct {
	Run        string `json:"run_id"`
	Task       string `json:"task_id"`
	Owner      string `json:"owner"`
	Epoch      int64  `json:"epoch"`
	Plan       int    `json:"plan_version"`
	Invocation string `json:"invocation_id"`
	Capability string `json:"capability"`
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Hash       string `json:"request_hash"`
}
type Approval struct {
	ID       string        `json:"approval_id"`
	Digest   string        `json:"action_digest"`
	State    string        `json:"state"`
	Approver string        `json:"approver,omitempty"`
	Expiry   time.Time     `json:"expiry"`
	Scope    ApprovalScope `json:"scope"`
}
type ApprovalDecision struct {
	Approval Approval `json:"approval"`
	Token    string   `json:"approval_token,omitempty"`
}
type approvalBinding struct {
	Scope     ApprovalScope `json:"scope"`
	TokenHash string        `json:"token_hash,omitempty"`
}

func approvalScope(c TaskClaim, r ToolRequest) ApprovalScope {
	return ApprovalScope{c.RunID, c.TaskID, c.Owner, c.Epoch, c.PlanVersion, r.ID, r.Capability, r.Version, r.Resource, r.Hash}
}
func validApprovalRequest(r ToolRequest) bool {
	return toolUUID.MatchString(r.ID) && toolHash.MatchString(r.Hash) && r.Effect == "non_idempotent" && r.Capability != "" && len(r.Capability) <= 64 && r.Version != "" && len(r.Version) <= 64 && r.Resource != "" && len(r.Resource) <= 64
}
func scanApproval(row pgx.Row) (Approval, approvalBinding, error) {
	var a Approval
	var b approvalBinding
	var raw []byte
	e := row.Scan(&a.ID, &a.Digest, &a.State, &a.Approver, &a.Expiry, &raw)
	if e == nil {
		e = json.Unmarshal(raw, &b)
		a.Scope = b.Scope
	}
	return a, b, e
}

const approvalColumns = "approval_id,action_digest,state,COALESCE(approver,''),expiry,requested_scope"

// RequestApproval pauses only this action. The durable pending record blocks
// task output. No execution is reserved, and a repeated request cannot reset a
// terminal decision or extend its DB-clock expiry. Lease/epoch fences still apply.
func (s *Store) RequestApproval(ctx context.Context, c TaskClaim, r ToolRequest, ttl time.Duration) (Approval, error) {
	if c.Role != "action" || !validApprovalRequest(r) || ttl <= 0 || ttl > 5*time.Minute {
		return Approval{}, ErrPolicy
	}
	tx, state, e := s.guardTask(ctx, c, true)
	if e != nil {
		return Approval{}, e
	}
	defer tx.Rollback(ctx)
	if state != "executing" {
		return Approval{}, ErrBadState
	}
	a, b, e := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalColumns+" FROM approval_requests WHERE task_id=$1 AND requested_scope->'scope'->>'invocation_id'=$2 FOR UPDATE", c.TaskID, r.ID))
	if e == nil {
		if b.Scope != approvalScope(c, r) {
			return Approval{}, ErrPolicy
		}
		if a.State != "pending" {
			return a, ErrApprovalFinal
		}
		var live bool
		if e = tx.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()", a.Expiry).Scan(&live); e != nil {
			return a, e
		}
		if !live {
			if e = expireApproval(ctx, tx, a); e != nil {
				return a, e
			}
			if e = commitModelTask(ctx, tx, c); e != nil {
				return a, e
			}
			return a, ErrApprovalExpired
		}
		return a, commitModelTask(ctx, tx, c)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return a, e
	}
	var n int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM approval_requests WHERE task_id=$1", c.TaskID).Scan(&n); e != nil {
		return a, e
	}
	if n >= 16 {
		return a, ErrBudget
	}
	b = approvalBinding{Scope: approvalScope(c, r)}
	raw, _ := json.Marshal(b)
	a, _, e = scanApproval(tx.QueryRow(ctx, "INSERT INTO approval_requests(run_id,task_id,action_digest,requested_scope,state,expiry) VALUES($1,$2,$3,$4,'pending',LEAST((SELECT lease_until FROM task_instances WHERE task_id=$2),clock_timestamp()+$5*interval '1 second')) RETURNING "+approvalColumns, c.RunID, c.TaskID, r.Hash, raw, ttl.Seconds()))
	if e != nil {
		return a, e
	}
	if e = approvalEvent(ctx, tx, a, 0, "approval_pending"); e != nil {
		return a, e
	}
	return a, commitModelTask(ctx, tx, c)
}

// DecideApproval is a local operator/control-plane API, never an Agent route.
// The actual database session identity is the approver. The token is returned
// once; only its hash is stored, and no automatic execution follows approval.
func (s *Store) DecideApproval(ctx context.Context, id, digest string, allow bool) (ApprovalDecision, error) {
	out := ApprovalDecision{}
	if !toolUUID.MatchString(id) || !toolHash.MatchString(digest) {
		return out, ErrPolicy
	}
	var run, task string
	if e := s.Pool.QueryRow(ctx, "SELECT run_id,task_id FROM approval_requests WHERE approval_id=$1", id).Scan(&run, &task); e != nil {
		return out, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	state, version, _, e := lockWorkflow(ctx, tx, run)
	if e != nil {
		return out, e
	}
	if state != "running" {
		return out, ErrBadState
	}
	var owner, taskState string
	var epoch int64
	var live bool
	if e = tx.QueryRow(ctx, "SELECT COALESCE(owner,''),epoch,state,COALESCE(lease_until>clock_timestamp(),false) FROM task_instances WHERE run_id=$1 AND task_id=$2 FOR UPDATE", run, task).Scan(&owner, &epoch, &taskState, &live); e != nil {
		return out, e
	}
	a, b, e := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalColumns+" FROM approval_requests WHERE approval_id=$1 FOR UPDATE", id))
	if e != nil {
		return out, e
	}
	if a.Digest != digest || b.Scope.Hash != digest || b.Scope.Run != run || b.Scope.Task != task {
		return out, ErrPolicy
	}
	if a.State != "pending" {
		return out, ErrApprovalFinal
	}
	var unexpired bool
	if e = tx.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()", a.Expiry).Scan(&unexpired); e != nil {
		return out, e
	}
	if !unexpired || !live || taskState != "executing" || b.Scope.Owner != owner || b.Scope.Epoch != epoch || b.Scope.Plan != version {
		if e = expireApproval(ctx, tx, a); e != nil {
			return out, e
		}
		if e = tx.Commit(ctx); e != nil {
			return out, e
		}
		return out, ErrApprovalExpired
	}
	if e = tx.QueryRow(ctx, "SELECT session_user").Scan(&a.Approver); e != nil {
		return out, e
	}
	a.State = "denied"
	if allow {
		var secret [32]byte
		if _, e = rand.Read(secret[:]); e != nil {
			return out, e
		}
		out.Token = hex.EncodeToString(secret[:])
		b.TokenHash = artifact.Digest([]byte(out.Token))
		a.State = "approved"
	}
	raw, _ := json.Marshal(b)
	if _, e = tx.Exec(ctx, "UPDATE approval_requests SET state=$2,approver=$3,requested_scope=$4 WHERE approval_id=$1", id, a.State, a.Approver, raw); e != nil {
		return ApprovalDecision{}, e
	}
	if e = approvalEvent(ctx, tx, a, 1, "approval_"+a.State); e != nil {
		return ApprovalDecision{}, e
	}
	out.Approval = a
	if e = tx.Commit(ctx); e != nil {
		return ApprovalDecision{}, e
	}
	return out, nil
}
func approvalEvent(ctx context.Context, tx pgx.Tx, a Approval, revision int64, kind string) error {
	return emit(ctx, tx, a.Scope.Run, "approval", a.ID, revision, kind, map[string]any{"task_id": a.Scope.Task, "action_digest": a.Digest, "scope": a.Scope, "state": a.State, "expiry": a.Expiry, "approver": a.Approver})
}
func expireApproval(ctx context.Context, tx pgx.Tx, a Approval) error {
	revision := int64(1)
	if a.State == "approved" {
		revision = 2
	}
	if a.State != "pending" && a.State != "approved" {
		return ErrApprovalFinal
	}
	a.State = "expired"
	if _, e := tx.Exec(ctx, "UPDATE approval_requests SET state='expired' WHERE approval_id=$1", a.ID); e != nil {
		return e
	}
	return approvalEvent(ctx, tx, a, revision, "approval_expired")
}

// The caller holds workflow/budget/task locks. Consumption and tool reservation
// commit together. An expired decision is materialized even though it is refused.
func consumeApproval(ctx context.Context, tx pgx.Tx, c TaskClaim, r ToolRequest, id, token string) error {
	if !toolUUID.MatchString(id) || !toolHash.MatchString(token) {
		return ErrPolicy
	}
	a, b, e := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalColumns+" FROM approval_requests WHERE approval_id=$1 AND run_id=$2 AND task_id=$3 FOR UPDATE", id, c.RunID, c.TaskID))
	if e != nil {
		return ErrPolicy
	}
	if a.State != "approved" || a.Digest != r.Hash || b.Scope != approvalScope(c, r) || subtle.ConstantTimeCompare([]byte(b.TokenHash), []byte(artifact.Digest([]byte(token)))) != 1 {
		return ErrApprovalFinal
	}
	var live bool
	if e = tx.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()", a.Expiry).Scan(&live); e != nil {
		return e
	}
	if !live {
		if e = expireApproval(ctx, tx, a); e != nil {
			return e
		}
		return ErrApprovalExpired
	}
	a.State = "consumed"
	if _, e = tx.Exec(ctx, "UPDATE approval_requests SET state='consumed' WHERE approval_id=$1", id); e != nil {
		return e
	}
	return approvalEvent(ctx, tx, a, 2, "approval_consumed")
}
func expireApprovalsTx(ctx context.Context, tx pgx.Tx, run string) error {
	rows, e := tx.Query(ctx, "SELECT "+approvalColumns+" FROM approval_requests WHERE run_id=$1 AND state IN ('pending','approved') AND expiry<=clock_timestamp() ORDER BY approval_id FOR UPDATE", run)
	if e != nil {
		return e
	}
	all := []Approval{}
	for rows.Next() {
		a, _, err := scanApproval(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, a)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, a := range all {
		if e = expireApproval(ctx, tx, a); e != nil {
			return e
		}
	}
	return nil
}
