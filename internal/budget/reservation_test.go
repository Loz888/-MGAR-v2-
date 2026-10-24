package budget

import (
	"strings"
	"testing"
)

func TestBudgetPricingIntegerBounds(t *testing.T) {
	p := Pricing{Version: "synthetic.v1", Provider: "mock", Model: "small.v1", InputMicrosPerMillion: 1, OutputMicrosPerMillion: 2}
	cost, e := p.Cost(1, 1)
	if e != nil || cost != 1 {
		t.Fatal(cost, e)
	}
	for _, value := range []int64{-1, 10_000_001, 1 << 62} {
		if _, e = p.Cost(value, 1); e == nil {
			t.Fatal("unbounded quote")
		}
	}
	r, e := PricedRequest("11111111-1111-4111-8111-111111111111", p, 10, 5, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	r.CostMicros++
	if r.Valid() {
		t.Fatal("price forged")
	}
	p.Provider = "paid"
	if _, e = p.Cost(1, 1); e == nil {
		t.Fatal("unauthorized provider")
	}
}
