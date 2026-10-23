package policy

import "testing"

func TestApprovalRiskIsControllerDefined(t *testing.T) {
	for _, tc := range []struct{ role, effect, want string }{{"research", "readonly", "read"}, {"research", "idempotent_write", "denied"}, {"data", "non_idempotent", "denied"}, {"action", "idempotent_write", "sandbox_write"}, {"action", "non_idempotent", "approval_required"}, {"action", "arbitrary", "denied"}} {
		got, e := Risk(Definition{Role: tc.role, Effect: tc.effect})
		if got != tc.want || (e != nil) != (tc.want == "denied") {
			t.Fatal(tc, got, e)
		}
	}
}
