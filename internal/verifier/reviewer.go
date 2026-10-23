package verifier

import (
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"slices"
)

type Review struct {
	Verdict          Verdict  `json:"deterministic_verdict"`
	AdditionalChecks []string `json:"additional_checks"`
	SummaryHash      string   `json:"summary_sha256"`
	ReviewHash       string   `json:"review_sha256"`
}

// AttachReview consumes data, not authority. The deterministic verdict is copied
// verbatim, including failed/unverifiable states. It never executes suggestions.
func AttachReview(v Verdict, raw []byte) (Review, error) {
	var advice struct {
		Schema  string   `json:"schema"`
		Summary string   `json:"summary"`
		Checks  []string `json:"additional_checks"`
	}
	if len(raw) > 8192 || StrictJSON(raw, &advice) != nil || advice.Schema != "review.v1" || len(advice.Summary) == 0 || len(advice.Summary) > 2048 || advice.Checks == nil || len(advice.Checks) > 8 || !slices.Contains([]string{"passed", "failed", "unverifiable"}, v.Status) || v.Schema != "verdict.v1" || v.Version != Version || !uuid.MatchString(v.ArtifactID) {
		return Review{}, ErrStructure
	}
	seen := map[string]bool{}
	for _, c := range advice.Checks {
		if seen[c] || !slices.Contains([]string{"check_duplicate_orders", "check_input_freshness", "check_boundary_cases", "check_query_scope"}, c) {
			return Review{}, ErrStructure
		}
		seen[c] = true
	}
	v.Checks = slices.Clone(v.Checks)
	return Review{Verdict: v, AdditionalChecks: slices.Clone(advice.Checks), SummaryHash: artifact.Digest([]byte(advice.Summary)), ReviewHash: artifact.Digest(raw)}, nil
}
