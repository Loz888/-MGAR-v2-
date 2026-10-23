package handoff

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"testing"
)

type recoveryLedger struct {
	result budget.CallRecovery
	err    error
}

func (l recoveryLedger) ReconcileCalls(context.Context, string, string, int64, int64) (budget.CallRecovery, error) {
	return l.result, l.err
}
func TestUnknownRecoveryDecision(t *testing.T) {
	for _, tc := range []struct {
		state budget.CallRecovery
		want  string
	}{
		{budget.CallRecovery{}, "resume"},
		{budget.CallRecovery{Pending: 1}, "wait"},
		{budget.CallRecovery{Unknown: 1, Pending: 1}, "stop_unknown"},
		{budget.CallRecovery{UnsafeTool: true}, "stop_unknown"},
	} {
		got, err := Reconcile(context.Background(), recoveryLedger{result: tc.state}, "run", Authority{Owner: "owner", Epoch: 1})
		if err != nil || got != tc.want {
			t.Fatal(got, err, tc)
		}
	}
	sentinel := errors.New("unavailable")
	if _, err := Reconcile(context.Background(), recoveryLedger{err: sentinel}, "run", Authority{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
