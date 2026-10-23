package handoff

import (
	"encoding/json"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"strings"
	"testing"
)

func mergeSource(id, value string, verified bool) Source {
	return Source{Ref: inputcontext.Ref{ID: id, Hash: strings.Repeat("a", 64), ProducerNode: id, ProducerTaskID: id, SourcePlanVersion: 1, ToolVersion: "fixture.v1"}, Verified: verified, Claims: []Claim{{Key: "paid_orders", Value: json.RawMessage(value)}}}
}
func TestMergeConflictPreservesAllSourcesAndArrivalOrder(t *testing.T) {
	a, b := mergeSource("A", "12", true), mergeSource("B", "13", true)
	first, e := Merge([]Source{a, b}, nil)
	if e != nil || first.CanFinalize || len(first.Conflicts) != 1 || len(first.Conflicts[0].Alternatives) != 2 {
		t.Fatal(first, e)
	}
	second, e := Merge([]Source{b, a}, nil)
	if e != nil || string(inputcontext.Canonical(first)) != string(inputcontext.Canonical(second)) {
		t.Fatal("arrival order wins", second, e)
	}
	b.Claims[0].Value = json.RawMessage("12")
	equal, e := Merge([]Source{a, b}, nil)
	if e != nil || !equal.CanFinalize || len(equal.Facts) != 1 || len(equal.Facts[0].Sources) != 2 {
		t.Fatal(equal, e)
	}
}
func TestMergeVerifiedPriorityAndExplicitMissing(t *testing.T) {
	trusted, untrusted := mergeSource("Z", "12", true), mergeSource("A", "13", false)
	r, e := Merge([]Source{untrusted, trusted}, []Missing{{Node: "optional", Required: false, Reason: "failed"}})
	if e != nil || r.CanFinalize || len(r.Conflicts) != 1 || !r.Conflicts[0].Alternatives[0].Verified || len(r.Missing) != 1 {
		t.Fatal(r, e)
	}
	r, e = Merge([]Source{trusted}, []Missing{{Node: "optional", Required: false, Reason: "failed"}})
	if e != nil || !r.CanFinalize || len(r.Missing) != 1 {
		t.Fatal(r, e)
	}
	r, e = Merge([]Source{trusted}, []Missing{{Node: "required", Required: true, Reason: "failed"}})
	if e != nil || r.CanFinalize {
		t.Fatal(r, e)
	}
	r, e = Merge([]Source{untrusted}, nil)
	if e != nil || r.CanFinalize {
		t.Fatal("unverified claim promoted", r, e)
	}
}
func TestMergeRejectsAmbiguousOrOversizedClaims(t *testing.T) {
	for _, body := range []string{`{"schema_version":"facts.v1","facts":[{"key":"n","value":1,"value":2}]}`, `{"schema_version":"facts.v1","facts":[{"key":"n","value":{"arbitrary":"object"}}]}`, `{"schema_version":"facts.v1","facts":[],"verified":true}`, `{"schema_version":"facts.v1","facts":null}`, `{"schema_version":"facts.v1","facts":[{"key":"n","value":1e9999}]}`} {
		if _, e := DecodeClaims([]byte(body)); e == nil {
			t.Fatal("invalid facts accepted", body)
		}
	}
	if _, e := Merge([]Source{mergeSource("A", "1", true), mergeSource("A", "2", true)}, nil); e == nil {
		t.Fatal("duplicate source identity")
	}
}
