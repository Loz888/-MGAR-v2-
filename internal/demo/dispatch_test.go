package demo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/policy"
)

func TestLiveRoleDispatchContracts(t *testing.T) {
	cases := []struct{ node, tool, kind string }{{"planner", "", "plan"}, {"research", "research.read", "tool_observation"}, {"data", "data.query", "tool_observation"}, {"action", "candidate.read", "tool_observation"}, {"aggregate", "aggregate.merge", "tool_observation"}, {"aggregate", "candidate.read", "candidate_report"}, {"single", "data.query", "candidate_report"}}
	for _, tc := range cases {
		t.Run(tc.node+"/"+tc.tool, func(t *testing.T) {
			var pPlan = proposal("incident")
			p := &pPlan
			if tc.node != "planner" {
				p = nil
			}
			defs := []policy.Definition{}
			if tc.tool != "" {
				defs = append(defs, policy.Definition{Name: tc.tool})
			}
			d, e := liveDispatch(tc.node, "incident", 1, p, defs)
			if e != nil {
				t.Fatal(e)
			}
			if d["schema_version"] != "demo.live-dispatch.v3" || d["final_kind"] != tc.kind || d["task_goal"] == "" || d["output_schema"] == nil {
				t.Fatal(d)
			}
			if _, mixed := d["baseline_final"]; mixed {
				t.Fatal("ambiguous shared final contract")
			}
			raw, _ := json.Marshal(d)
			if tc.node == "planner" && (d["required_plan"] == nil || strings.Contains(string(raw), "candidate_report")) {
				t.Fatal("planner mixed with business answer", string(raw))
			}
			if tc.node == "action" && d["final_kind"] == "candidate_report" {
				t.Fatal("action cannot finish with aggregate answer")
			}
			if tc.kind == "candidate_report" && !strings.Contains(string(raw), `"const":"answer"`) {
				t.Fatal("business success decision missing", string(raw))
			}
		})
	}
	for _, v := range []struct {
		node, business string
		version        int
	}{{"other", "incident", 1}, {"single", "other", 1}, {"single", "analysis", 0}} {
		if _, e := liveDispatch(v.node, v.business, v.version, nil, nil); e == nil {
			t.Fatal("unsupported role dispatch allowed")
		}
	}
}
