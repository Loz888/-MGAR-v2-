package planner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplanStrictWireAndSemanticLoopHash(t *testing.T) {
	good := `{"schema_version":"replan.v1","failure_kind":"evidence_rejected","failed_node":"A","proposal":{"schema_version":"plan.v1","base_version":1,"nodes":[{"node_key":"A","role":"research","goal":"check","required_inputs":[],"required_output_kind":"finding"}],"edges":[],"rationale":"fix"}}`
	r, e := DecodeReplan([]byte(good))
	if e != nil {
		t.Fatal(e)
	}
	for _, wire := range []string{
		strings.Replace(good, `"failure_kind":"evidence_rejected"`, `"failure_kind":"policy_denied"`, 1),
		strings.Replace(good, `"schema_version":"replan.v1"`, `"schema_version":"replan.v1","schema_version":"replan.v1"`, 1),
		strings.Replace(good, `"role":"research"`, `"Role":"research"`, 1),
		strings.Replace(good, `"failed_node":"A"`, `"failed_node":null`, 1),
		good + ` {}`, strings.Replace(good, `"failed_node":"A"`, `"failed_node":"A","state":"succeeded"`, 1),
	} {
		if _, e := DecodeReplan([]byte(wire)); e == nil {
			t.Fatal("invalid wire accepted", wire)
		}
	}
	original := ReplanDigest([]byte(good), r, nil, true)
	r.Proposal.BaseVersion = 5
	r.Proposal.Rationale = "same plan rephrased"
	body, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if ReplanDigest(body, r, nil, true) != original {
		t.Fatal("rationale bypasses repeated proposal bound")
	}
}
