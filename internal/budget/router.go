package budget

import (
	"errors"
	"slices"
	"sort"
)

var ErrNoRoute = errors.New("NO_AUTHORIZED_AFFORDABLE_MODEL")

type RouteModel struct {
	Price     Pricing
	Roles     []string
	Available bool
}
type Router struct {
	Catalog          []RouteModel
	AllowedProviders []string
}
type RouteInput struct {
	Role, LogicalID, InputDigest, Preferred string
	InputTokens, OutputTokens               int64
	AllowFallback                           bool
}
type RouteRejection struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}
type RouteDecision struct {
	Selected  string           `json:"selected"`
	Role      string           `json:"role"`
	Reason    string           `json:"reason"`
	Preferred string           `json:"preferred,omitempty"`
	Rejected  []RouteRejection `json:"rejected"`
}

func routeRole(role string) bool {
	return slices.Contains([]string{"research", "data", "action", "verify", "aggregate"}, role)
}
func (d RouteDecision) valid(model string) bool {
	if d.Selected != model || !routeRole(d.Role) || (d.Reason != "preferred" && d.Reason != "explicit_fallback" && d.Reason != "lowest_authorized_cost") || len(d.Rejected) > 32 || (d.Preferred != "" && !modelName.MatchString(d.Preferred)) {
		return false
	}
	for _, r := range d.Rejected {
		if !modelName.MatchString(r.Model) || !slices.Contains([]string{"not_authorized", "unavailable", "capability", "budget"}, r.Reason) {
			return false
		}
	}
	return true
}

// Route is a quote, not spending authority. ReserveModelCall must succeed before
// transport; concurrent reservations can invalidate this read-only budget view.
func (r Router) Route(in RouteInput, b Report) (ModelRequest, error) {
	if !routeRole(in.Role) || len(r.Catalog) == 0 || len(r.Catalog) > 32 || !b.Conserved || in.InputTokens < 0 || in.OutputTokens < 0 || in.InputTokens+in.OutputTokens > 10_000_000 || in.InputTokens+in.OutputTokens <= 0 || !logicalID.MatchString(in.LogicalID) || !digest.MatchString(in.InputDigest) || (in.Preferred != "" && !modelName.MatchString(in.Preferred)) {
		return ModelRequest{}, ErrNoRoute
	}
	if b.ReservedModels+b.SettledModels >= b.Limits.ModelCalls || b.ReservedModels+b.ReservedTools >= b.Limits.ConcurrentCalls {
		return ModelRequest{}, ErrNoRoute
	}
	candidates := []ModelRequest{}
	rejected := []RouteRejection{}
	seen := map[string]bool{}
	for _, m := range r.Catalog {
		if !modelName.MatchString(m.Price.Model) || seen[m.Price.Model] {
			return ModelRequest{}, ErrNoRoute
		}
		seen[m.Price.Model] = true
		reason := ""
		cost, e := m.Price.Cost(in.InputTokens, in.OutputTokens)
		switch {
		case !slices.Contains(r.AllowedProviders, m.Price.Provider) || !m.Price.Valid():
			reason = "not_authorized"
		case !m.Available:
			reason = "unavailable"
		case !slices.Contains(m.Roles, in.Role):
			reason = "capability"
		case e != nil || in.InputTokens+in.OutputTokens > b.Limits.Tokens-b.ReservedTokens-b.SettledTokens || cost > b.Limits.CostMicros-b.ReservedCostMicros-b.SettledCostMicros:
			reason = "budget"
		}
		if reason != "" {
			rejected = append(rejected, RouteRejection{m.Price.Model, reason})
			continue
		}
		req, e := PricedRequest(in.LogicalID, m.Price, in.InputTokens, in.OutputTokens, in.InputDigest)
		if e != nil {
			return ModelRequest{}, ErrNoRoute
		}
		candidates = append(candidates, req)
	}
	if len(candidates) == 0 {
		return ModelRequest{}, ErrNoRoute
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CostMicros != candidates[j].CostMicros {
			return candidates[i].CostMicros < candidates[j].CostMicros
		}
		return candidates[i].Model < candidates[j].Model
	})
	selected := candidates[0]
	reason := "lowest_authorized_cost"
	if in.Preferred != "" {
		found := false
		for _, c := range candidates {
			if c.Model == in.Preferred {
				selected = c
				found = true
				break
			}
		}
		if found {
			reason = "preferred"
		} else {
			if !in.AllowFallback {
				return ModelRequest{}, ErrNoRoute
			}
			reason = "explicit_fallback"
		}
	}
	selected.Routing = &RouteDecision{Selected: selected.Model, Role: in.Role, Reason: reason, Preferred: in.Preferred, Rejected: rejected}
	return selected, nil
}
