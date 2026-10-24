package store

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"github.com/jackc/pgx/v5"
	"time"
)

type ScheduleNode struct {
	ID, Key, Role, State string
	ReadyAt              time.Time
	Estimate             time.Duration
}
type ScheduleView struct {
	Version int
	Now     time.Time
	Nodes   []ScheduleNode
	Edges   []dag.Edge
}

// SchedulingView is advisory. ClaimReady rechecks authority and dependencies.
func (s *Store) SchedulingView(ctx context.Context, run string) (ScheduleView, error) {
	v := ScheduleView{Nodes: []ScheduleNode{}, Edges: []dag.Edge{}}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	var state string
	if e = tx.QueryRow(ctx, "SELECT active_plan_version,state,clock_timestamp() FROM workflow_runs WHERE run_id=$1", run).Scan(&v.Version, &state, &v.Now); e != nil {
		return v, e
	}
	if state != "running" {
		return v, ErrBadState
	}
	// Only successful task attempts in this workflow contribute observations.
	rows, e := tx.Query(ctx, `SELECT n.task_instance_id,n.node_key,n.role,t.state,
 COALESCE((SELECT min(o.created_at) FROM workflow_events e JOIN outbox_events o USING(event_id) WHERE e.entity_id=t.task_id AND e.kind='task_ready'),w.created_at),
 COALESCE((SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY d.seconds) FROM (
 SELECT extract(epoch FROM max(o.created_at) FILTER(WHERE e.kind='output_ready')-max(o.created_at) FILTER(WHERE e.kind='task_executing'))::double precision seconds
 FROM task_instances h JOIN workflow_events e ON e.entity_id=h.task_id AND e.run_id=h.run_id JOIN outbox_events o USING(event_id)
 WHERE h.run_id=$1 AND h.role=t.role AND h.state='succeeded' GROUP BY h.task_id
 ) d WHERE d.seconds>0),1)::double precision
 FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id AND t.run_id=n.run_id JOIN workflow_runs w ON w.run_id=n.run_id
 WHERE n.run_id=$1 AND n.plan_version=$2 ORDER BY n.node_key`, run, v.Version)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var n ScheduleNode
		var seconds float64
		if e = rows.Scan(&n.ID, &n.Key, &n.Role, &n.State, &n.ReadyAt, &seconds); e != nil {
			rows.Close()
			return v, e
		}
		seconds = max(.001, min(300, seconds))
		n.Estimate = time.Duration(seconds * float64(time.Second))
		v.Nodes = append(v.Nodes, n)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}
	rows, e = tx.Query(ctx, "SELECT from_key,to_key FROM plan_edges WHERE run_id=$1 AND plan_version=$2 ORDER BY from_key,to_key", run, v.Version)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var edge dag.Edge
		if e = rows.Scan(&edge.From, &edge.To); e != nil {
			rows.Close()
			return v, e
		}
		v.Edges = append(v.Edges, edge)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func (s *Store) ClaimReady(ctx context.Context, run string, id WorkerIdentity, ttl time.Duration, version int, task, policy string) (TaskClaim, error) {
	if version < 1 || !toolUUID.MatchString(task) || (policy != "fifo" && policy != "critical_path") {
		return TaskClaim{}, ErrPolicy
	}
	return s.claimTask(ctx, run, id, ttl, version, task, policy)
}
