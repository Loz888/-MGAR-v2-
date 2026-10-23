package scheduler

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"sort"
)

type DependencyOutcome struct {
	Node, State string
	Verified    bool
}
type FanInDecision struct {
	Action  string
	Missing []handoff.Missing
}

// Optional describes supplementary evidence only. Persisted DAG edges remain
// required AND dependencies; this advisory function cannot grant a Task Claim.
func AssessFanIn(required map[string]bool, outcomes []DependencyOutcome, allowReplan bool) (FanInDecision, error) {
	d := FanInDecision{Action: "continue", Missing: []handoff.Missing{}}
	if len(required) > 24 || len(outcomes) != len(required) {
		return d, errors.New("fan-in input set")
	}
	seen := map[string]bool{}
	pending, bad := false, false
	for _, o := range outcomes {
		need, ok := required[o.Node]
		if !ok || seen[o.Node] {
			return d, errors.New("unknown/duplicate dependency")
		}
		seen[o.Node] = true
		if o.State == "succeeded" && o.Verified {
			continue
		}
		reason := o.State
		permanent := false
		switch o.State {
		case "failed", "cancelled", "blocked":
			permanent = true
		case "succeeded":
			reason = "unverified"
			permanent = true
		case "pending", "ready", "claimed", "executing", "output_ready", "verifying":
		default:
			return d, errors.New("invalid dependency state")
		}
		d.Missing = append(d.Missing, handoff.Missing{Node: o.Node, Required: need, Reason: reason})
		if need {
			bad = bad || permanent
			pending = pending || !permanent
		}
	}
	if bad {
		d.Action = "fail"
		if allowReplan {
			d.Action = "replan"
		}
	} else if pending {
		d.Action = "wait"
	}
	sort.Slice(d.Missing, func(i, j int) bool { return d.Missing[i].Node < d.Missing[j].Node })
	return d, nil
}

// ResolveFanIn is the conservative no-replan policy: failures propagate and
// terminate honestly; callers may submit a bounded Replan before selecting it.
func (s Scheduler) ResolveFanIn(ctx context.Context, run string, c store.OrchestratorClaim) (store.Workflow, error) {
	if e := s.Store.ApplyVerdicts(ctx, run, c); e != nil {
		return store.Workflow{}, e
	}
	if _, e := s.Store.RefreshReady(ctx, run, c); e != nil {
		return store.Workflow{}, e
	}
	return s.Store.FinalizeWorkflow(ctx, run, c)
}
