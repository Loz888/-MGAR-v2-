package planner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
)

type ReplanPolicy struct {
	MaxReplans        int   `json:"max_replans"`
	MaxDepth          int   `json:"max_depth"`
	MaxCostMicros     int64 `json:"max_cost_micros"`
	CostPerTaskMicros int64 `json:"cost_per_task_micros"`
}

func (p ReplanPolicy) Valid() bool {
	return p.MaxReplans >= 1 && p.MaxReplans <= 8 && p.MaxDepth >= 1 && p.MaxDepth <= 6 && p.MaxCostMicros >= 1 && p.MaxCostMicros <= 1_000_000_000 && p.CostPerTaskMicros >= 1 && p.CostPerTaskMicros <= 1_000_000_000
}

type ReplanSuggestion struct {
	SchemaVersion string   `json:"schema_version"`
	FailureKind   string   `json:"failure_kind"`
	FailedNode    string   `json:"failed_node"`
	Proposal      Proposal `json:"proposal"`
}

func DecodeReplan(data []byte) (ReplanSuggestion, error) {
	var r ReplanSuggestion
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return r, errors.New("invalid replan size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if e := uniqueValue(d); e != nil {
		return r, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return r, errors.New("trailing JSON")
	}
	object, e := exactObject(data, "schema_version", "failure_kind", "failed_node", "proposal")
	if e != nil {
		return r, e
	}
	for _, key := range []string{"schema_version", "failure_kind", "failed_node"} {
		if e = jsonString(object[key]); e != nil {
			return r, e
		}
	}
	if e = json.Unmarshal(data, &r); e != nil {
		return r, e
	}
	if r.SchemaVersion != "replan.v1" || !nodeKey.MatchString(r.FailedNode) || (r.FailureKind != "evidence_rejected" && r.FailureKind != "permanent") {
		return r, errors.New("invalid failure suggestion")
	}
	r.Proposal, e = Decode(object["proposal"])
	return r, e
}
func CheckReplanBounds(p Proposal, policy ReplanPolicy, recompute int) error {
	if !policy.Valid() || recompute < 0 || int64(recompute)*policy.CostPerTaskMicros > policy.MaxCostMicros {
		return errors.New("replan cost policy")
	}
	keys := []string{}
	for _, n := range p.Nodes {
		keys = append(keys, n.Key)
	}
	order, e := dag.Order(keys, p.Edges)
	if e != nil {
		return e
	}
	depth := map[string]int{}
	for _, key := range order {
		if depth[key] == 0 {
			depth[key] = 1
		}
		if depth[key] > policy.MaxDepth {
			return errors.New("replan depth policy")
		}
		for _, edge := range p.Edges {
			if edge.From == key && depth[edge.To] < depth[key]+1 {
				depth[edge.To] = depth[key] + 1
			}
		}
	}
	return nil
}

// Ignore rationale, base version and array ordering when identifying repeated
// semantic proposals. This hash is a loop bound, never an execution identity.
func ReplanDigest(raw []byte, r ReplanSuggestion, configs map[string]inputcontext.ExecutionConfig, valid bool) string {
	if !valid {
		return artifact.Digest(raw)
	}
	p := r.Proposal
	p.BaseVersion = 0
	p.Rationale = "semantic-proposal"
	p.Nodes = slices.Clone(p.Nodes)
	p.Edges = slices.Clone(p.Edges)
	slices.SortFunc(p.Nodes, func(a, b Node) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	slices.SortFunc(p.Edges, func(a, b Edge) int {
		x, y := a.From+"/"+a.To, b.From+"/"+b.To
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	})
	return artifact.Digest(inputcontext.Canonical(map[string]any{"proposal": p, "configs": configs}))
}
