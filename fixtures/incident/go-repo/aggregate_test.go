package aggregate

import "testing"

func TestPaidDeduplicatedTotal(t *testing.T) {
	orders := []Order{{101, "paid", 100}, {101, "paid", 100}, {102, "paid", 50}, {103, "pending", 70}}
	got := PaidTotal(orders)
	t.Logf("got=%d", got)
	if got != 150 {
		t.Fatalf("deduplicated paid total: got=%d want=150", got)
	}
}
