package scheduler

import "testing"

func TestFanInRequiredOptionalFailureAndReplan(t *testing.T) {
	required := map[string]bool{"A": true, "B": false}
	outcomes := []DependencyOutcome{{Node: "A", State: "succeeded", Verified: true}, {Node: "B", State: "failed"}}
	d, e := AssessFanIn(required, outcomes, false)
	if e != nil || d.Action != "continue" || len(d.Missing) != 1 || d.Missing[0].Required {
		t.Fatal(d, e)
	}
	required["B"] = true
	d, e = AssessFanIn(required, outcomes, true)
	if e != nil || d.Action != "replan" {
		t.Fatal(d, e)
	}
	d, e = AssessFanIn(required, outcomes, false)
	if e != nil || d.Action != "fail" {
		t.Fatal(d, e)
	}
	outcomes[1].State = "pending"
	d, e = AssessFanIn(required, outcomes, true)
	if e != nil || d.Action != "wait" {
		t.Fatal(d, e)
	}
	outcomes[1] = DependencyOutcome{Node: "B", State: "succeeded", Verified: false}
	d, e = AssessFanIn(required, outcomes, false)
	if e != nil || d.Action != "fail" {
		t.Fatal("unverified dependency", d, e)
	}
}
