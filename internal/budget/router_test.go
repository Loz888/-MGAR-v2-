package budget

import (
	"strings"
	"testing"
)

func routerFixture() Router {
	return Router{AllowedProviders: []string{"mock"}, Catalog: []RouteModel{
		{Price: Pricing{Version: "test.v1", Provider: "mock", Model: "small", InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}, Roles: []string{"research"}, Available: true},
		{Price: Pricing{Version: "test.v1", Provider: "mock", Model: "large", InputMicrosPerMillion: 2_000_000, OutputMicrosPerMillion: 2_000_000}, Roles: []string{"research", "action"}, Available: true},
	}}
}
func routeInput() RouteInput {
	return RouteInput{Role: "research", LogicalID: "11111111-1111-4111-8111-111111111111", InputDigest: strings.Repeat("a", 64), InputTokens: 60, OutputTokens: 40, Preferred: "large", AllowFallback: true}
}
func routeReport() Report {
	return Report{Conserved: true, Limits: Limits{ModelCalls: 3, Tokens: 1000, CostMicros: 150, ConcurrentCalls: 2}}
}
func TestRouterBudgetCapabilityAndExplicitFallback(t *testing.T) {
	r := routerFixture()
	in := routeInput()
	b := routeReport()
	q, e := r.Route(in, b)
	if e != nil || q.Model != "small" || q.Routing.Reason != "explicit_fallback" || !q.Valid() {
		t.Fatal(q, e)
	}
	in.AllowFallback = false
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("silent fallback")
	}
	in.AllowFallback = true
	in.Role = "action"
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("weak model gained capability")
	}
	in.Role = "research"
	r.AllowedProviders = []string{"paid"}
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("unauthorized provider")
	}
	r = routerFixture()
	b.ReservedTokens = 950
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("token quota")
	}
	b = routeReport()
	b.UnknownModels = 1
	b.ReservedModels = 1
	b.ReservedCostMicros = 100
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("unknown reservation ignored")
	}
	b = routeReport()
	b.Conserved = false
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("ledger drift")
	}
	b = routeReport()
	in.Preferred = ""
	q, e = r.Route(in, b)
	if e != nil || q.Routing.Reason != "lowest_authorized_cost" {
		t.Fatal(q, e)
	}
	q.Routing.Selected = "forged"
	if q.Valid() {
		t.Fatal("unbound routing explanation")
	}
}

func TestRouterUnavailableNeedsExplicitFallback(t *testing.T) {
	r := routerFixture()
	r.Catalog[1].Available = false
	in := routeInput()
	b := routeReport()
	b.Limits.CostMicros = 500
	q, e := r.Route(in, b)
	if e != nil || q.Model != "small" || q.Routing.Rejected[0].Reason != "unavailable" {
		t.Fatal(q, e)
	}
	in.AllowFallback = false
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("silent availability fallback")
	}
	r.Catalog[0].Price.Provider = "external"
	in.AllowFallback = true
	if _, e = r.Route(in, b); e == nil {
		t.Fatal("external fallback")
	}
}
