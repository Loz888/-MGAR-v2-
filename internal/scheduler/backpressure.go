package scheduler

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"time"
)

// Run queues a bounded request, respects DB priority aging, and cancels execution
// when the task loses authority. The callback is trusted and must implement a
// real deadline/stop protocol (Docker, PG cancellation or a bounded transport).
// Returning stopped=false keeps a durable unknown permit rather than overbook.
func (r *Resources) Run(parent context.Context, c store.TaskClaim, class string, priority int, execute func(context.Context) (bool, error)) (err error) {
	local, ok := r.local[class]
	if !ok || execute == nil {
		return store.ErrPolicy
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	ttl := time.Until(deadline)
	if ttl < time.Millisecond {
		return store.ErrResourceDeadline
	}
	ticket, e := r.Store.EnqueueResource(ctx, c, class, priority, ttl)
	if e != nil {
		return e
	}
	stopped := true
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		err = errors.Join(err, r.Store.FinishResource(cleanup, ticket, stopped))
		if !stopped {
			err = errors.Join(err, store.ErrResourceUnconfirmed)
		}
	}()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		held, e := r.Store.AcquireResource(ctx, c, ticket)
		if e != nil {
			return e
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return store.ErrResourceDeadline
		case <-poll.C:
		}
	}
	select {
	case local <- struct{}{}:
		defer func() { <-local }()
	case <-ctx.Done():
		return store.ErrResourceDeadline
	}
	monitorDone := make(chan struct{})
	stopMonitor := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if r.Store.ValidateToolClaim(ctx, c) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(stopMonitor); <-monitorDone }()
	stopped = false
	stopped, err = execute(ctx)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return err
}
