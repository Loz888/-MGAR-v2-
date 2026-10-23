package planner

import (
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"testing"
)

func TestInvalidateChangedBranchAndReuseIndependentArtifact(t *testing.T) {
	p := sample()
	p.Nodes = append(p.Nodes, Node{Key: "B", Role: "data", Goal: "unchanged", RequiredInputs: []string{}, OutputKind: "sql_evidence"}, Node{Key: "C", Role: "action", Goal: "fix", RequiredInputs: []string{"A"}, OutputKind: "patch"})
	p.Edges = []Edge{{From: "A", To: "C"}}
	cfg := map[string]inputcontext.ExecutionConfig{}
	history := map[string]inputcontext.Lineage{}
	for _, n := range p.Nodes {
		c := inputcontext.ExecutionConfig{ModelVersion: "mock.v1", PolicyVersion: n.Role + ".v1", OutputToolVersion: n.Role + ".v1"}
		cfg[n.Key] = c
		history[n.Key] = inputcontext.Lineage{RequirementsHash: artifact.Digest(inputcontext.Canonical(n)), Config: c, Valid: true, Artifact: artifact.Metadata{ID: n.Key, Hash: n.Key}, Parents: []string{}, Inputs: []inputcontext.Ref{}}
	}
	c := history["C"]
	c.Parents = []string{"A"}
	c.Inputs = []inputcontext.Ref{{ProducerNode: "A", ID: "A", Hash: "A", ToolVersion: cfg["A"].OutputToolVersion}}
	history["C"] = c
	p.Nodes[0].Goal = "changed upstream evidence"
	result, e := Invalidate(p, cfg, history)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Reuse) != 1 || result.Reuse["B"] != "B" || result.Recompute["A"] == "" || result.Recompute["C"] != "upstream_changed" {
		t.Fatal(result)
	}
}
func TestReuseRejectsConfigChangesAndInvalidProvenance(t *testing.T) {
	p := sample()
	n := p.Nodes[0]
	cfg := inputcontext.ExecutionConfig{ModelVersion: "mock.v1", PolicyVersion: "research.v1", OutputToolVersion: "research.v1"}
	h := inputcontext.Lineage{RequirementsHash: artifact.Digest(inputcontext.Canonical(n)), Config: cfg, Valid: true, Artifact: artifact.Metadata{ID: "artifact", Hash: "hash"}, Parents: []string{}, Inputs: []inputcontext.Ref{}}
	changed := cfg
	changed.PolicyVersion = "research.v2"
	for _, tc := range []struct {
		history inputcontext.Lineage
		config  inputcontext.ExecutionConfig
	}{{h, changed}, {inputcontext.Lineage{}, cfg}} {
		d, e := Invalidate(p, map[string]inputcontext.ExecutionConfig{n.Key: tc.config}, map[string]inputcontext.Lineage{n.Key: tc.history})
		if e != nil || len(d.Reuse) != 0 || len(d.Recompute) != 1 {
			t.Fatal(d, e)
		}
	}
}
