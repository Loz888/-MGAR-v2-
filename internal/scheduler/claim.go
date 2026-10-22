package scheduler

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"time"
)

func (s Scheduler) Claim(ctx context.Context, run string, id store.WorkerIdentity, ttl time.Duration) (store.TaskClaim, error) {
	return s.Store.ClaimTask(ctx, run, id, ttl)
}
func (s Scheduler) Renew(ctx context.Context, c store.TaskClaim, ttl time.Duration) (store.TaskClaim, error) {
	return s.Store.RenewTask(ctx, c, ttl)
}
func (s Scheduler) Save(ctx context.Context, c store.TaskClaim, artifact string) error {
	return s.Store.CommitOutput(ctx, c, artifact)
}
