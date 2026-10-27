package demo

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

// Failure details are a closed, redacted reporting protocol. They grant no
// execution/settlement authority and never instruct a Worker to retry.
func writeModelFailure(w http.ResponseWriter, err error, call budget.ModelCall) {
	status, kind, usage := http.StatusConflict, "model_usage_unknown", "unknown"
	switch {
	case errors.Is(err, provider.ErrUnknown), errors.Is(err, store.ErrResourceUnconfirmed):
		// Explicit uncertainty wins over joined semantic/admission labels.
	case errors.Is(err, provider.ErrOutputLimit):
		status, kind, usage = http.StatusUnprocessableEntity, "model_output_limit", "known"
	case errors.Is(err, provider.ErrDecision):
		status, kind, usage = http.StatusUnprocessableEntity, "model_invalid_response", "known"
	case call.ID != "":
		// After reservation, accounting/cleanup errors cannot establish that no
		// request was sent, even if a joined error also mentions budget/policy.
	case errors.Is(err, provider.ErrInputLimit):
		status, kind, usage = http.StatusRequestEntityTooLarge, "model_input_limit", "not_sent"
	case errors.Is(err, store.ErrBudget):
		status, kind, usage = http.StatusPaymentRequired, "budget_exhausted", "not_sent"
	case errors.Is(err, store.ErrResourceDeadline), errors.Is(err, store.ErrResourceFull):
		status, kind, usage = http.StatusLocked, "resource_exhausted", "not_sent"
	case errors.Is(err, provider.ErrPolicy), errors.Is(err, store.ErrPolicy), errors.Is(err, store.ErrFenced), errors.Is(err, store.ErrLeaseExpired), errors.Is(err, store.ErrBadState):
		status, kind, usage = http.StatusForbidden, "policy_denied", "not_sent"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": "model-error.v1", "error": kind, "usage_status": usage, "retryable": false})
}
