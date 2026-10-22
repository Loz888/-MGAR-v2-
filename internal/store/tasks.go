package store

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"github.com/jackc/pgx/v5"
	"sort"
)

type Task struct {
	ID, NodeKey, Role, State string
	Revision                 int64
}
type readyNode struct {
	Task
	Result, OutputKind string
}
type inputRef struct {
	ID, Hash, Producer string
	Version            int
}

var ErrStaleInput = errors.New("STALE_INPUT")

func (s *Store) RefreshReady(ctx context.Context, run string, c OrchestratorClaim) ([]Task, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	state, version, _, err := lockWorkflow(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	if state != "running" {
		return nil, ErrBadState
	}
	if err = checkOrchestrator(ctx, tx, run, c); err != nil {
		return nil, err
	}
	var budgetOK bool
	if err = tx.QueryRow(ctx, "SELECT used_delegations<max_delegations AND running_count<max_concurrency FROM run_budgets WHERE run_id=$1 FOR UPDATE", run).Scan(&budgetOK); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT t.task_id,n.node_key,t.role,t.state,t.revision,COALESCE(t.result_artifact_id::text,''),n.requirements->>'required_output_kind' FROM plan_nodes n JOIN task_instances t ON t.task_id=n.task_instance_id WHERE n.run_id=$1 AND n.plan_version=$2 ORDER BY t.task_id FOR UPDATE OF t", run, version)
	if err != nil {
		return nil, err
	}
	nodes := map[string]*readyNode{}
	keys := []string{}
	for rows.Next() {
		n := &readyNode{}
		if err = rows.Scan(&n.ID, &n.NodeKey, &n.Role, &n.State, &n.Revision, &n.Result, &n.OutputKind); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[n.NodeKey] = n
		keys = append(keys, n.NodeKey)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	edgeRows, err := tx.Query(ctx, "SELECT from_key,to_key FROM plan_edges WHERE run_id=$1 AND plan_version=$2", run, version)
	if err != nil {
		return nil, err
	}
	edges := []dag.Edge{}
	parents := map[string][]string{}
	for edgeRows.Next() {
		var e dag.Edge
		if err = edgeRows.Scan(&e.From, &e.To); err != nil {
			edgeRows.Close()
			return nil, err
		}
		edges = append(edges, e)
		parents[e.To] = append(parents[e.To], e.From)
	}
	edgeRows.Close()
	if err = edgeRows.Err(); err != nil {
		return nil, err
	}
	order, err := dag.Order(keys, edges)
	if err != nil {
		return nil, err
	}
	for _, key := range order {
		n := nodes[key]
		if n.State != "pending" {
			continue
		}
		all := true
		bad := false
		refs := []inputRef{}
		for _, parent := range parents[key] {
			p := nodes[parent]
			if p.State == "failed" || p.State == "cancelled" || p.State == "blocked" {
				bad = true
			}
			if p.State != "succeeded" {
				all = false
				continue
			}
			if p.Result == "" {
				bad = true
				continue
			}
			var ref inputRef
			var verified bool
			var kind string
			ref.Producer = parent
			err = tx.QueryRow(ctx, "SELECT a.artifact_id,a.content_sha256,a.source_plan_version,a.kind,COALESCE((SELECT v.passed FROM artifact_verdicts v WHERE v.artifact_id=a.artifact_id ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),false) FROM artifacts a WHERE a.artifact_id=$1 AND a.run_id=$2 AND a.producer_task=$3", p.Result, run, p.ID).Scan(&ref.ID, &ref.Hash, &ref.Version, &kind, &verified)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if err != nil || !verified || ref.Version != version || kind != p.OutputKind {
				bad = true
				continue
			}
			refs = append(refs, ref)
		}
		target, kind := "", ""
		if bad {
			target = "blocked"
			kind = "dependency_blocked"
		} else if all && budgetOK {
			target = "ready"
			kind = "task_ready"
		}
		if target == "" {
			continue
		}
		if target == "ready" {
			expected := map[string]inputRef{}
			for _, ref := range refs {
				expected[ref.ID] = ref
			}
			existingRows, e := tx.Query(ctx, "SELECT artifact_id,content_sha256,producer_node,source_plan_version FROM task_inputs WHERE task_id=$1", n.ID)
			if e != nil {
				return nil, e
			}
			for existingRows.Next() {
				var have inputRef
				if e = existingRows.Scan(&have.ID, &have.Hash, &have.Producer, &have.Version); e != nil {
					existingRows.Close()
					return nil, e
				}
				want, ok := expected[have.ID]
				if !ok || want != have {
					existingRows.Close()
					return nil, ErrStaleInput
				}
			}
			existingRows.Close()
			if e = existingRows.Err(); e != nil {
				return nil, e
			}
			for _, ref := range refs {
				var existing string
				err = tx.QueryRow(ctx, "SELECT content_sha256 FROM task_inputs WHERE task_id=$1 AND artifact_id=$2", n.ID, ref.ID).Scan(&existing)
				if err == nil && existing != ref.Hash {
					return nil, ErrStaleInput
				}
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if _, err = tx.Exec(ctx, "INSERT INTO task_inputs(task_id,run_id,artifact_id,content_sha256,producer_node,source_plan_version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", n.ID, run, ref.ID, ref.Hash, ref.Producer, ref.Version); err != nil {
					return nil, err
				}
			}
		}
		tag, e := tx.Exec(ctx, "UPDATE task_instances SET state=$2,revision=revision+1 WHERE task_id=$1 AND revision=$3 AND state='pending'", n.ID, target, n.Revision)
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrBadState
		}
		n.State = target
		n.Revision++
		if err = emit(ctx, tx, run, "task", n.ID, n.Revision, kind, map[string]string{"node_key": key, "policy": "same_run_current_plan_verified_direct_dependencies"}); err != nil {
			return nil, err
		}
	}
	ready := []Task{}
	sort.Strings(keys)
	for _, key := range keys {
		if nodes[key].State == "ready" {
			ready = append(ready, nodes[key].Task)
		}
	}
	return ready, tx.Commit(ctx)
}
