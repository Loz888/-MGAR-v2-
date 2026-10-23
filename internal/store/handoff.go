package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/jackc/pgx/v5"
)

// Scan by per-consumer receipt, including published events whose notification
// was lost. SKIP LOCKED is unnecessary: duplicate transport is intentional.
func (s *Store) PendingHandoffs(ctx context.Context, run, consumer string, limit int) ([]handoff.Event, error) {
	if consumer != handoff.SchedulerConsumer || limit < 1 || limit > 256 {
		return nil, ErrPolicy
	}
	rows, e := s.Pool.Query(ctx, `SELECT o.event_id,w.run_id,w.entity_type,w.entity_id,w.kind,w.entity_revision
 FROM outbox_events o JOIN workflow_events w USING(event_id)
 WHERE w.run_id=$1 AND NOT EXISTS(SELECT 1 FROM inbox_receipts i WHERE i.consumer_name=$2 AND i.event_id=o.event_id AND i.processing_status='processed')
 ORDER BY o.created_at,o.event_id LIMIT $3`, run, consumer, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	events := []handoff.Event{}
	for rows.Next() {
		var v handoff.Event
		if e = rows.Scan(&v.ID, &v.RunID, &v.EntityType, &v.EntityID, &v.Kind, &v.Revision); e != nil {
			return nil, e
		}
		events = append(events, v)
	}
	return events, rows.Err()
}
func (s *Store) MarkPublished(ctx context.Context, id string) error {
	tag, e := s.Pool.Exec(ctx, "UPDATE outbox_events SET published_at=COALESCE(published_at,clock_timestamp()) WHERE event_id=$1", id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrPolicy
	}
	return nil
}

// ConsumeHandoff accepts an ID, never sender-provided state or authorization.
// Workflow locking serializes consumers with plan changes and other consumers;
// ready/input writes and receipt processed are committed under the same lease.
func (s *Store) ConsumeHandoff(ctx context.Context, run string, c OrchestratorClaim, consumer, id string) (handoff.Receipt, error) {
	result := handoff.Receipt{}
	if consumer != handoff.SchedulerConsumer {
		return result, ErrPolicy
	}
	tx, w, e := s.controlTx(ctx, run, c)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	var entity, entityID, body string
	var revision int64
	e = tx.QueryRow(ctx, `SELECT w.entity_type,w.entity_id,w.entity_revision,o.payload::text
 FROM outbox_events o JOIN workflow_events w USING(event_id) WHERE o.event_id=$1 AND w.run_id=$2`, id, run).Scan(&entity, &entityID, &revision, &body)
	if errors.Is(e, pgx.ErrNoRows) {
		return result, ErrPolicy
	}
	if e != nil {
		return result, e
	}
	var status string
	e = tx.QueryRow(ctx, "SELECT processing_status FROM inbox_receipts WHERE consumer_name=$1 AND event_id=$2", consumer, id).Scan(&status)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}
	if status == "processed" {
		result = handoff.Receipt{Processed: true, Duplicate: true, Reason: "already_processed"}
		return result, commitControl(ctx, tx, run, c)
	}
	var envelope struct {
		Schema      string `json:"schema_version"`
		RunID       string `json:"run_id"`
		PlanVersion int    `json:"plan_version"`
		Revision    int64  `json:"entity_revision"`
	}
	if e = json.Unmarshal([]byte(body), &envelope); e != nil {
		return result, ErrPolicy
	}
	current := false
	if envelope.Schema == "handoff.v1" && envelope.RunID == run && envelope.PlanVersion == w.ActivePlanVersion && envelope.Revision == revision {
		switch entity {
		case "workflow":
			current = entityID == run && revision == w.Revision
		case "task":
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_instances t JOIN plan_nodes n ON n.task_instance_id=t.task_id
    WHERE n.run_id=$1 AND n.plan_version=$2 AND t.task_id=$3 AND t.revision=$4)`, run, w.ActivePlanVersion, entityID, revision).Scan(&current)
			if e != nil {
				return result, e
			}
		}
	}
	result = handoff.Receipt{Processed: true, Reason: "stale"}
	if current && w.State == "running" {
		if _, e = s.refreshReadyTx(ctx, tx, run, c); e != nil {
			return handoff.Receipt{}, e
		}
		result.Applied = true
		result.Reason = "authoritative_state_checked"
	}
	if _, e = tx.Exec(ctx, "INSERT INTO inbox_receipts(consumer_name,event_id,processing_status) VALUES($1,$2,'received') ON CONFLICT DO NOTHING", consumer, id); e != nil {
		return handoff.Receipt{}, e
	}
	if _, e = tx.Exec(ctx, "UPDATE inbox_receipts SET processing_status='processed' WHERE consumer_name=$1 AND event_id=$2", consumer, id); e != nil {
		return handoff.Receipt{}, e
	}
	if e = commitControl(ctx, tx, run, c); e != nil {
		return handoff.Receipt{}, e
	}
	return result, nil
}
