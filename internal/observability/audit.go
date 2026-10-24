package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type AuditTask struct {
	ID                string `json:"task_id"`
	Role              string `json:"role"`
	State             string `json:"state"`
	Version           int    `json:"plan_version"`
	Epoch, Revision   int64
	Attempts, Retries int
}
type AuditEvent struct {
	ID                             string `json:"event_id"`
	Entity, EntityID, Kind, TaskID string
	Revision                       int64
	PlanVersion                    int
	At                             *time.Time
	TraceMatches                   bool
	ActorDigest                    string
	Epoch                          int64
}
type AuditModel struct {
	ID, TaskID, Status, ModelDigest, Provider string
	Known                                     bool
	Tokens, CostMicros                        *int64
	ReservedTokens, ReservedCostMicros        int64
	InputTokens, OutputTokens                 *int64
}
type AuditHandoff struct {
	EventID, State, ConsumerDigest string
	At                             time.Time
}
type Audit struct {
	Schema            string         `json:"schema_version"`
	RunID             string         `json:"run_id"`
	TraceID           string         `json:"trace_id"`
	SpanState         string         `json:"span_state"`
	CreatedAt         time.Time      `json:"created_at"`
	SnapshotAt        time.Time      `json:"snapshot_at"`
	Tasks             []AuditTask    `json:"tasks"`
	Events            []AuditEvent   `json:"events"`
	Models            []AuditModel   `json:"models"`
	Handoffs          []AuditHandoff `json:"handoffs"`
	Truncated         bool           `json:"truncated"`
	MissingEventTimes int            `json:"missing_event_times"`
}

// ReadAudit never selects raw payloads, authentication material or business text.
func ReadAudit(ctx context.Context, s *store.Store, run string) (Audit, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Audit{}, e
	}
	defer tx.Rollback(ctx)
	a, e := ReadAuditTx(ctx, tx, run)
	if e != nil {
		return Audit{}, e
	}
	return a, tx.Commit(ctx)
}

// ReadAuditTx only reads. The trusted caller owns the repeatable-read snapshot.
func ReadAuditTx(ctx context.Context, tx pgx.Tx, run string) (Audit, error) {
	a := Audit{Schema: "audit_trace.v1", RunID: run, TraceID: strings.ReplaceAll(run, "-", ""), SpanState: "missing_or_not_collected", Tasks: []AuditTask{}, Events: []AuditEvent{}, Models: []AuditModel{}, Handoffs: []AuditHandoff{}}
	if !uuid.MatchString(run) {
		return Audit{}, ErrTrace
	}
	var e error
	if e = tx.QueryRow(ctx, "SELECT created_at,statement_timestamp() FROM workflow_runs WHERE run_id=$1", run).Scan(&a.CreatedAt, &a.SnapshotAt); e != nil {
		return Audit{}, e
	}
	rows, e := tx.Query(ctx, `SELECT t.task_id,t.role,t.state,COALESCE(max(n.plan_version),0),t.epoch,t.revision,t.attempts,t.retry_count FROM task_instances t LEFT JOIN plan_nodes n ON n.run_id=t.run_id AND n.task_instance_id=t.task_id WHERE t.run_id=$1 GROUP BY t.task_id ORDER BY t.task_id LIMIT 4097`, run)
	if e != nil {
		return Audit{}, e
	}
	for rows.Next() {
		var t AuditTask
		if e = rows.Scan(&t.ID, &t.Role, &t.State, &t.Version, &t.Epoch, &t.Revision, &t.Attempts, &t.Retries); e != nil {
			rows.Close()
			return Audit{}, e
		}
		a.Tasks = append(a.Tasks, t)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return Audit{}, e
	}
	rows, e = tx.Query(ctx, `SELECT e.event_id,e.entity_type,e.entity_id,e.kind,COALESCE(t.task_id::text,''),e.entity_revision,COALESCE((o.payload->>'plan_version')::int,0),o.created_at,e.trace_id=e.run_id,COALESCE(e.payload->>'owner',''),COALESCE((e.payload->>'epoch')::bigint,0)
 FROM workflow_events e LEFT JOIN outbox_events o USING(event_id)
 LEFT JOIN model_calls m ON e.entity_type='model_call' AND m.call_id=e.entity_id AND m.run_id=e.run_id
 LEFT JOIN tool_invocations v ON e.entity_type='tool_invocation' AND v.invocation_id=e.entity_id
 LEFT JOIN artifacts ar ON e.entity_type='artifact' AND ar.artifact_id=e.entity_id AND ar.run_id=e.run_id
 LEFT JOIN artifact_verdicts av ON e.entity_type='verdict' AND av.verdict_id=e.entity_id
 LEFT JOIN artifacts va ON va.artifact_id=av.artifact_id AND va.run_id=e.run_id
 LEFT JOIN task_instances t ON t.run_id=e.run_id AND t.task_id::text=COALESCE(CASE WHEN e.entity_type='task' THEN e.entity_id::text END,m.task_id::text,v.task_id::text,ar.producer_task::text,va.producer_task::text,e.payload->>'task_id')
 WHERE e.run_id=$1 ORDER BY o.created_at NULLS LAST,e.entity_revision,e.event_id LIMIT 4097`, run)
	if e != nil {
		return Audit{}, e
	}
	for rows.Next() {
		var event AuditEvent
		var actor string
		if e = rows.Scan(&event.ID, &event.Entity, &event.EntityID, &event.Kind, &event.TaskID, &event.Revision, &event.PlanVersion, &event.At, &event.TraceMatches, &actor, &event.Epoch); e != nil {
			rows.Close()
			return Audit{}, e
		}
		if actor != "" {
			sum := sha256.Sum256([]byte(actor))
			event.ActorDigest = hex.EncodeToString(sum[:])
		}
		event.Entity = safeEntity(event.Entity)
		event.Kind = safeKind(event.Kind)
		if event.At == nil {
			a.MissingEventTimes++
		}
		a.Events = append(a.Events, event)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return Audit{}, e
	}
	rows, e = tx.Query(ctx, `SELECT call_id,task_id,status,model,usage_known,(usage->>'tokens')::bigint,(usage->>'cost_micros')::bigint,COALESCE((reservation->>'tokens')::bigint,0),COALESCE((reservation->>'cost_micros')::bigint,0),(usage->>'input_tokens')::bigint,(usage->>'output_tokens')::bigint,provider FROM model_calls WHERE run_id=$1 ORDER BY call_id LIMIT 4097`, run)
	if e != nil {
		return Audit{}, e
	}
	for rows.Next() {
		var m AuditModel
		var name string
		if e = rows.Scan(&m.ID, &m.TaskID, &m.Status, &name, &m.Known, &m.Tokens, &m.CostMicros, &m.ReservedTokens, &m.ReservedCostMicros, &m.InputTokens, &m.OutputTokens, &m.Provider); e != nil {
			rows.Close()
			return Audit{}, e
		}
		h := sha256.Sum256([]byte(name))
		m.ModelDigest = hex.EncodeToString(h[:])
		if m.Provider != "mock" {
			m.Provider = "unrecognized"
		}
		if !m.Known {
			m.Tokens = nil
			m.CostMicros = nil
			m.InputTokens = nil
			m.OutputTokens = nil
		}
		a.Models = append(a.Models, m)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return Audit{}, e
	}
	rows, e = tx.Query(ctx, `SELECT i.event_id,i.processing_status,i.first_seen_at,i.consumer_name FROM inbox_receipts i JOIN workflow_events e USING(event_id) WHERE e.run_id=$1 ORDER BY i.first_seen_at,i.event_id LIMIT 4097`, run)
	if e != nil {
		return Audit{}, e
	}
	for rows.Next() {
		var h AuditHandoff
		var consumer string
		if e = rows.Scan(&h.EventID, &h.State, &h.At, &consumer); e != nil {
			rows.Close()
			return Audit{}, e
		}
		digest := sha256.Sum256([]byte(consumer))
		h.ConsumerDigest = hex.EncodeToString(digest[:])
		a.Handoffs = append(a.Handoffs, h)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return Audit{}, e
	}
	if len(a.Events) == 0 {
		a.SpanState = "missing_empty_audit"
	}
	a.Truncated = len(a.Tasks) > 4096 || len(a.Events) > 4096 || len(a.Models) > 4096 || len(a.Handoffs) > 4096
	return a, nil
}
func safeEntity(s string) string {
	switch s {
	case "workflow", "orchestrator", "task", "snapshot", "artifact", "model_call", "tool_invocation", "verdict", "review", "policy_decision", "resource", "resource_config", "budget_config", "approval":
		return s
	}
	return "unrecognized"
}
func safeKind(s string) string {
	// Closed vocabulary: no user-controlled payload string becomes a span name.
	const allowed = " workflow_queued workflow_planning workflow_running workflow_verifying workflow_blocked workflow_interrupted workflow_succeeded workflow_failed workflow_cancelled plan_committed replan_attempt orchestrator_claimed orchestrator_renewed orchestrator_released task_ready task_claimed task_executing task_renewed output_ready task_verifying task_succeeded task_failed task_cancelled task_blocked dependency_blocked model_reserved model_succeeded model_failed model_unknown tool_reserved tool_succeeded tool_failed tool_unknown tool_denied artifact_published context_snapshot_created derived_summary_created verdict_recorded review_recorded merge_rejected resource_queued resource_held resource_released resource_cancelled resource_expired resource_unknown resource_configured budget_configured approval_pending approval_approved approval_denied approval_expired approval_consumed "
	if strings.Contains(allowed, " "+s+" ") && s != "" {
		return s
	}
	return "unrecognized"
}
