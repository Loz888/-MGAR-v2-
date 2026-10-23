package handoff

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
)

type Authority struct {
	Owner           string
	Epoch, Revision int64
}
type CallLedger interface {
	ReconcileCalls(context.Context, string, string, int64, int64) (budget.CallRecovery, error)
}

// Reconcile has no transport dependency: an unknown request is never replayed.
func Reconcile(ctx context.Context, ledger CallLedger, run string, c Authority) (string, error) {
	r, e := ledger.ReconcileCalls(ctx, run, c.Owner, c.Epoch, c.Revision)
	if e != nil {
		return "", e
	}
	if r.Unknown > 0 || r.UnsafeTool {
		return "stop_unknown", nil
	}
	if r.Pending > 0 {
		return "wait", nil
	}
	return "resume", nil
}
