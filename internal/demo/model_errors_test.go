package demo

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

func TestModelFailureEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		reserved    bool
		code        int
		kind, usage string
	}{
		{"length", provider.ErrOutputLimit, true, 422, "model_output_limit", "known"},
		{"invalid", provider.ErrDecision, true, 422, "model_invalid_response", "known"},
		{"input", provider.ErrInputLimit, false, 413, "model_input_limit", "not_sent"},
		{"budget", store.ErrBudget, false, 402, "budget_exhausted", "not_sent"},
		{"resource", store.ErrResourceFull, false, 423, "resource_exhausted", "not_sent"},
		{"policy", store.ErrPolicy, false, 403, "policy_denied", "not_sent"},
		{"unknown", provider.ErrUnknown, true, 409, "model_usage_unknown", "unknown"},
		{"post-reservation-budget", store.ErrBudget, true, 409, "model_usage_unknown", "unknown"},
		{"joined-uncertainty", errors.Join(provider.ErrDecision, store.ErrResourceUnconfirmed), true, 409, "model_usage_unknown", "unknown"},
		{"backend-details", errors.New("Bearer synthetic-leak-marker"), false, 409, "model_usage_unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			call := budget.ModelCall{}
			if tc.reserved {
				call.ID = "already-reserved"
			}
			writeModelFailure(w, tc.err, call)
			var data map[string]any
			if json.Unmarshal(w.Body.Bytes(), &data) != nil || w.Code != tc.code || len(data) != 4 || data["schema_version"] != "model-error.v1" || data["error"] != tc.kind || data["usage_status"] != tc.usage || data["retryable"] != false || strings.Contains(w.Body.String(), "synthetic-leak-marker") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
