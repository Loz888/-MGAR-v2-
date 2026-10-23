package policy

import (
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"net/http"
	"time"
)

// Risk is derived from trusted definitions, never model arguments. Only Action
// can request writes, and every non-idempotent effect needs explicit approval.
func Risk(d Definition) (string, error) {
	switch d.Effect {
	case "readonly":
		return "read", nil
	case "idempotent_write":
		if d.Role == "action" {
			return "sandbox_write", nil
		}
	case "non_idempotent":
		if d.Role == "action" {
			return "approval_required", nil
		}
	}
	return "denied", ErrDenied
}
func (g *Gateway) authorizeAction(w http.ResponseWriter, r *http.Request, p grant, req store.ToolRequest) (cached, handled bool, err error) {
	ids, tokens := r.Header.Values("X-MGAR-Approval-ID"), r.Header.Values("X-MGAR-Approval-Token")
	if req.Effect != "non_idempotent" {
		if len(ids) > 0 || len(tokens) > 0 {
			return false, false, ErrDenied
		}
		cached, err = g.store.BeginTool(r.Context(), p.claim, req)
		return cached, false, err
	}
	if len(ids) == 0 && len(tokens) == 0 {
		approval, e := g.store.RequestApproval(r.Context(), p.claim, req, 2*time.Minute)
		if e != nil {
			return false, false, e
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "approval_required", "retryable": false, "approval": approval})
		return false, true, nil
	}
	if len(ids) != 1 || len(tokens) != 1 {
		return false, false, ErrDenied
	}
	cached, err = g.store.BeginApprovedTool(r.Context(), p.claim, req, ids[0], tokens[0])
	return cached, false, err
}
