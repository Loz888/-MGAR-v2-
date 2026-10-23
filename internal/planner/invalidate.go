package planner

import (
	"errors"
	"slices"
	"sort"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
)

type Invalidation struct {
	Reuse     map[string]string `json:"reuse"`
	Recompute map[string]string `json:"recompute"`
}

func Invalidate(p Proposal, configs map[string]inputcontext.ExecutionConfig, history map[string]inputcontext.Lineage) (Invalidation, error) {
	d := Invalidation{Reuse: map[string]string{}, Recompute: map[string]string{}}
	if e := Validate(p); e != nil {
		return d, e
	}
	if len(configs) != len(p.Nodes) {
		return d, errors.New("execution configuration set mismatch")
	}
	nodes := map[string]Node{}
	parents := map[string][]string{}
	keys := []string{}
	for _, n := range p.Nodes {
		nodes[n.Key] = n
		keys = append(keys, n.Key)
		if !configs[n.Key].Valid() {
			return d, errors.New("invalid execution configuration")
		}
	}
	for _, edge := range p.Edges {
		parents[edge.To] = append(parents[edge.To], edge.From)
	}
	order, e := dag.Order(keys, p.Edges)
	if e != nil {
		return d, e
	}
	for _, key := range order {
		n := nodes[key]
		old, exists := history[key]
		reason := ""
		sort.Strings(parents[key])
		old.Parents = slices.Clone(old.Parents)
		sort.Strings(old.Parents)
		if !exists || !old.Valid || old.Artifact.ID == "" {
			reason = "no_valid_provenance"
		} else if old.RequirementsHash != artifact.Digest(inputcontext.Canonical(n)) {
			reason = "requirements_changed"
		} else if old.Config != configs[key] {
			reason = "configuration_changed"
		} else if !slices.Equal(old.Parents, parents[key]) {
			reason = "dependencies_changed"
		}
		if reason == "" {
			actual := map[string]inputcontext.Ref{}
			for _, ref := range old.Inputs {
				actual[ref.ProducerNode] = ref
			}
			if len(actual) != len(parents[key]) || len(old.Inputs) != len(parents[key]) {
				reason = "input_lineage_mismatch"
			}
			for _, parent := range parents[key] {
				if d.Reuse[parent] == "" {
					reason = "upstream_changed"
					break
				}
				ref, ok := actual[parent]
				want := history[parent].Artifact
				if !ok || ref.ID != want.ID || ref.Hash != want.Hash || ref.ProducerTaskID != want.ProducerTaskID || ref.SourcePlanVersion != want.SourcePlanVersion || ref.ToolVersion != want.ToolVersion {
					reason = "input_lineage_mismatch"
					break
				}
			}
		}
		if reason != "" {
			d.Recompute[key] = reason
		} else {
			d.Reuse[key] = old.Artifact.ID
		}
	}
	return d, nil
}
