package scheduler

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

type Scheduler struct {
	Store *store.Store
	// Test/event barrier runs only after the durable lease transaction commits.
	AfterOrchestratorClaim func(string)
}

func (s Scheduler) GetReady(ctx context.Context, run string, c store.OrchestratorClaim) ([]store.Task, error) {
	return s.Store.RefreshReady(ctx, run, c)
}
