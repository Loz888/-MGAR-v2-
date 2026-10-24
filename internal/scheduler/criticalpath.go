package scheduler

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"sort"
	"time"
)

type Ranked struct {
	ID, Key          string
	Remaining, Score time.Duration
}

func Rank(nodes []store.ScheduleNode, edges []dag.Edge, role, policy string, now time.Time) ([]Ranked, error) {
	if policy != "fifo" && policy != "critical_path" {
		return nil, errors.New("UNKNOWN_SCHEDULING_POLICY")
	}
	keys := []string{}
	byKey := map[string]store.ScheduleNode{}
	children := map[string][]string{}
	parents := map[string][]string{}
	for _, n := range nodes {
		keys = append(keys, n.Key)
		byKey[n.Key] = n
	}
	order, e := dag.Order(keys, edges)
	if e != nil {
		return nil, e
	}
	for _, edge := range edges {
		children[edge.From] = append(children[edge.From], edge.To)
		parents[edge.To] = append(parents[edge.To], edge.From)
	}
	remaining := map[string]time.Duration{}
	for i := len(order) - 1; i >= 0; i-- {
		key := order[i]
		n := byKey[key]
		if n.State == "succeeded" || n.State == "failed" || n.State == "cancelled" || n.State == "blocked" {
			continue
		}
		own := n.Estimate
		if own <= 0 {
			own = time.Second
		}
		own = min(5*time.Minute, max(time.Millisecond, own))
		var tail time.Duration
		for _, child := range children[key] {
			tail = max(tail, remaining[child])
		}
		remaining[key] = own + tail
	}
	out := []Ranked{}
	for _, n := range nodes {
		if n.State != "ready" || n.Role != role {
			continue
		}
		valid := true
		for _, p := range parents[n.Key] {
			if byKey[p].State != "succeeded" {
				valid = false
			}
		}
		if !valid {
			continue
		}
		wait := max(time.Duration(0), now.Sub(n.ReadyAt))
		out = append(out, Ranked{n.ID, n.Key, remaining[n.Key], remaining[n.Key] + wait})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if policy == "critical_path" && a.Score != b.Score {
			return a.Score > b.Score
		}
		at, bt := byKey[a.Key].ReadyAt, byKey[b.Key].ReadyAt
		if !at.Equal(bt) {
			return at.Before(bt)
		}
		return a.Key < b.Key
	})
	return out, nil
}
func (s Scheduler) ClaimScheduled(ctx context.Context, run string, id store.WorkerIdentity, ttl time.Duration, policy string) (store.TaskClaim, error) {
	v, e := s.Store.SchedulingView(ctx, run)
	if e != nil {
		return store.TaskClaim{}, e
	}
	ranked, e := Rank(v.Nodes, v.Edges, id.Role, policy, v.Now)
	if e != nil {
		return store.TaskClaim{}, e
	}
	if len(ranked) == 0 {
		return store.TaskClaim{}, store.ErrNoReady
	}
	return s.Store.ClaimReady(ctx, run, id, ttl, v.Version, ranked[0].ID, policy)
}
