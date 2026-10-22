package planner

import (
	"encoding/json"
	"testing"
)

func strictJSONFixture(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	p := sample()
	p.Nodes = append(p.Nodes, Node{Key: "B", Role: "data", Goal: "inspect", RequiredInputs: []string{}, OutputKind: "sql_evidence"})
	p.Edges = []Edge{{From: "A", To: "B"}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	b, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanStrictJSONRejectsAliasesAndNullArrayItems(t *testing.T) {
	cases := map[string]func(map[string]any){
		"top-level case-fold alias":      func(v map[string]any) { v["Nodes"] = v["nodes"] },
		"schema version case-fold alias": func(v map[string]any) { v["SCHEMA_VERSION"] = "plan.v1" },
		"node role case-fold alias": func(v map[string]any) {
			n := v["nodes"].([]any)[0].(map[string]any)
			n["ROLE"] = n["role"]
			delete(n, "role")
		},
		"node extra case-fold field": func(v map[string]any) {
			n := v["nodes"].([]any)[0].(map[string]any)
			n["GOAL"] = n["goal"]
		},
		"edge from case-fold alias": func(v map[string]any) {
			e := v["edges"].([]any)[0].(map[string]any)
			e["FROM"] = e["from"]
			delete(e, "from")
		},
		"edge extra case-fold field": func(v map[string]any) {
			e := v["edges"].([]any)[0].(map[string]any)
			e["TO"] = e["to"]
		},
		"null input item": func(v map[string]any) {
			v["nodes"].([]any)[0].(map[string]any)["required_inputs"] = []any{nil}
		},
		"mixed null input item": func(v map[string]any) {
			v["nodes"].([]any)[0].(map[string]any)["required_inputs"] = []any{"fixture", nil}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strictJSONFixture(t, mutate)); err == nil {
				t.Fatal("schema-forbidden JSON accepted")
			}
		})
	}
}

func TestPlanStrictJSONNestedRequiredAndTypes(t *testing.T) {
	for _, field := range []string{"node_key", "role", "goal", "required_inputs", "required_output_kind"} {
		t.Run("missing node "+field, func(t *testing.T) {
			b := strictJSONFixture(t, func(v map[string]any) { delete(v["nodes"].([]any)[0].(map[string]any), field) })
			if _, err := Decode(b); err == nil {
				t.Fatal("missing node property accepted")
			}
		})
	}
	for _, field := range []string{"from", "to"} {
		t.Run("missing edge "+field, func(t *testing.T) {
			b := strictJSONFixture(t, func(v map[string]any) { delete(v["edges"].([]any)[0].(map[string]any), field) })
			if _, err := Decode(b); err == nil {
				t.Fatal("missing edge property accepted")
			}
		})
	}
	for name, value := range map[string]any{"boolean": true, "number": 1, "object": map[string]any{}, "array": []any{}, "null": nil} {
		t.Run("input item "+name, func(t *testing.T) {
			b := strictJSONFixture(t, func(v map[string]any) { v["nodes"].([]any)[0].(map[string]any)["required_inputs"] = []any{value} })
			if _, err := Decode(b); err == nil {
				t.Fatal("non-string input accepted")
			}
		})
	}
}

func TestPlanStrictJSONPreservesPermittedStringsAndEmptyArrays(t *testing.T) {
	b := strictJSONFixture(t, func(v map[string]any) {
		v["nodes"].([]any)[0].(map[string]any)["required_inputs"] = []any{"", "fixture-α"}
	})
	if _, err := Decode(b); err != nil {
		t.Fatalf("schema-permitted strings rejected: %v", err)
	}
}

func TestTypedPlanEdgeLimit(t *testing.T) {
	p := sample()
	p.Nodes = nil
	for i := 0; i < 24; i++ {
		p.Nodes = append(p.Nodes, Node{Key: string(rune('A' + i)), Role: "research", Goal: "inspect", RequiredInputs: []string{}, OutputKind: "finding"})
	}
	// Three layers keep depth at three and fanout at eight, so only the
	// schema's total edge limit separates these otherwise valid plans.
	for source := 0; source < 12; source++ {
		targetStart := 8
		if source >= 8 {
			targetStart = 16
		}
		for target := targetStart; target < targetStart+8; target++ {
			p.Edges = append(p.Edges, Edge{From: p.Nodes[source].Key, To: p.Nodes[target].Key})
		}
	}
	if len(p.Edges) != 96 {
		t.Fatalf("fixture has %d edges, want 96", len(p.Edges))
	}
	if err := Validate(p); err != nil {
		t.Fatalf("valid 24-node, 96-edge plan rejected: %v", err)
	}
	p.Edges = append(p.Edges, Edge{From: p.Nodes[12].Key, To: p.Nodes[16].Key})
	if err := Validate(p); err == nil {
		t.Fatal("24-node, 97-edge plan accepted")
	}
}
