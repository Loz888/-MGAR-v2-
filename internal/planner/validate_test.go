package planner

import (
	"encoding/json"
	"testing"
)

func sample() Proposal {
	return Proposal{SchemaVersion: "plan.v1", Rationale: "synthetic plan", Nodes: []Node{{Key: "A", Role: "research", Goal: "inspect synthetic input", RequiredInputs: []string{}, OutputKind: "finding"}}, Edges: []Edge{}}
}
func TestPlanSchemaStrict(t *testing.T) {
	p := sample()
	b, _ := json.Marshal(p)
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`{"schema_version":"plan.v1","nodes":[],"edges":[],"rationale":"x"}`, string(b) + ` {}`, `{"schema_version":"plan.v1","schema_version":"bad"}`} {
		if _, err := Decode([]byte(text)); err == nil {
			t.Fatal("invalid or ambiguous schema accepted")
		}
	}
	p.Nodes[0].Role = "admin"
	if Validate(p) == nil {
		t.Fatal("unauthorized role accepted")
	}
}
func TestDAGInvalidAndBounds(t *testing.T) {
	for _, edges := range [][]Edge{{{From: "A", To: "A"}}, {{From: "A", To: "missing"}}, {{From: "A", To: "B"}, {From: "B", To: "A"}}, {{From: "A", To: "B"}, {From: "B", To: "C"}, {From: "C", To: "A"}}} {
		p := sample()
		p.Nodes = append(p.Nodes, Node{Key: "B", Role: "data", Goal: "data", RequiredInputs: []string{}, OutputKind: "sql_evidence"}, Node{Key: "C", Role: "aggregate", Goal: "report", RequiredInputs: []string{}, OutputKind: "report"})
		p.Edges = edges
		if Validate(p) == nil {
			t.Fatalf("bad graph accepted: %v", edges)
		}
	}
	for _, kind := range []string{"nodes", "depth", "fanout"} {
		t.Run(kind, func(t *testing.T) {
			p := sample()
			n := 7
			if kind == "nodes" {
				n = 25
			}
			if kind == "fanout" {
				n = 10
			}
			p.Nodes = nil
			for i := 0; i < n; i++ {
				key := string(rune('A' + i))
				p.Nodes = append(p.Nodes, Node{Key: key, Role: "research", Goal: "inspect", RequiredInputs: []string{}, OutputKind: "finding"})
				if i > 0 {
					from := string(rune('A' + i - 1))
					if kind == "fanout" {
						from = "A"
					}
					p.Edges = append(p.Edges, Edge{From: from, To: key})
				}
			}
			if Validate(p) == nil {
				t.Fatal("DAG bound ignored")
			}
		})
	}
}
