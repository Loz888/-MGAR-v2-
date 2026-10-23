package verifier

import (
	"strings"
	"testing"
)

func TestReviewerAdviceStaysData(t *testing.T) {
	for _, status := range []string{"passed", "failed", "unverifiable"} {
		v := Verdict{Schema: "verdict.v1", ArtifactID: "11111111-1111-4111-8111-111111111111", Version: Version, Status: status, Checks: []Check{{Name: "oracle", Status: status}}}
		review, e := AttachReview(v, []byte(`{"schema":"review.v1","summary":"more checks","additional_checks":[]}`))
		if e != nil || review.Verdict.Status != status {
			t.Fatal(e, review)
		}
		review.Verdict.Checks[0].Status = "mutated"
		if v.Checks[0].Status != status {
			t.Fatal("aliased program verdict")
		}
		for _, raw := range []string{`{"schema":"review.v1","summary":"x","summary":"y","additional_checks":[]}`, `{"schema":"review.v1","summary":"x","additional_checks":null}`, `{"schema":"review.v1","summary":"x","additional_checks":["check_boundary_cases","check_boundary_cases"]}`, `{"schema":"review.v1","summary":"` + strings.Repeat("x", 2049) + `","additional_checks":[]}`} {
			if _, e := AttachReview(v, []byte(raw)); e == nil {
				t.Fatal("invalid advice accepted")
			}
		}
	}
}
