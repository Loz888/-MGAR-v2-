package scheduler

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"time"
)

// Resources combines a per-process semaphore with authoritative DB admission.
// Construct after configuring pools. A later capacity increase may leave an old
// local semaphore conservatively smaller; it can never exceed the DB quota.
type Resources struct {
	Store *store.Store
	local map[string]chan struct{}
}

func NewResources(ctx context.Context, s *store.Store) (*Resources, error) {
	if s == nil {
		return nil, store.ErrPolicy
	}
	policies, e := s.ResourcePolicies(ctx)
	if e != nil {
		return nil, e
	}
	r := &Resources{Store: s, local: map[string]chan struct{}{}}
	for _, p := range policies {
		if p.Capacity < 1 || p.Capacity > 64 {
			return nil, store.ErrPolicy
		}
		r.local[p.Class] = make(chan struct{}, p.Capacity)
	}
	if len(r.local) != 3 {
		return nil, store.ErrPolicy
	}
	return r, nil
}

// Model combines admission, durable budget reservation and trusted transport.
// Transport must report whether its local execution actually stopped; an absent
// usage report is recorded as unknown. This API grants no paid provider access.
func (r *Resources) Model(ctx context.Context, c store.TaskClaim, req budget.ModelRequest, transport func(context.Context) (budget.ModelUsage, bool, error)) (budget.ModelCall, error) {
	var call budget.ModelCall
	if transport == nil {
		return call, store.ErrPolicy
	}
	err := r.Run(ctx, c, "llm", 0, func(ctx context.Context) (bool, error) {
		var e error
		call, e = r.Store.ReserveModelCall(ctx, c, req)
		if e != nil {
			return true, e
		}
		usage, stopped, transportErr := transport(ctx)
		if transportErr != nil || !usage.Valid() {
			usage = budget.ModelUsage{Status: "unknown", Source: "unknown"}
		}
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		e = r.Store.FinishModelCall(settle, c, call.ID, usage)
		return stopped, errors.Join(transportErr, e)
	})
	return call, err
}
