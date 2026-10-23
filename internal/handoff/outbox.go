// Package handoff delivers durable hints. PostgreSQL state remains authority.
package handoff

import (
	"context"
	"errors"
)

const SchedulerConsumer = "scheduler.v1"

type Event struct {
	ID, RunID, EntityType, EntityID, Kind string
	Revision                              int64
}
type Outbox interface {
	PendingHandoffs(context.Context, string, string, int) ([]Event, error)
	MarkPublished(context.Context, string) error
}
type Dispatcher struct {
	Outbox   Outbox
	Consumer string
	Deliver  func(context.Context, Event) error
}

// Dispatch performs transport outside database transactions. Publication only
// acknowledges transport; missing/received inbox rows remain scan candidates.
func (d Dispatcher) Dispatch(ctx context.Context, run string, limit int) (int, error) {
	if d.Outbox == nil || d.Deliver == nil || d.Consumer != SchedulerConsumer || limit < 1 || limit > 256 {
		return 0, errors.New("invalid handoff dispatcher")
	}
	events, e := d.Outbox.PendingHandoffs(ctx, run, d.Consumer, limit)
	if e != nil {
		return 0, e
	}
	sent := 0
	for _, event := range events {
		if e = d.Deliver(ctx, event); e != nil {
			return sent, e
		}
		if e = d.Outbox.MarkPublished(ctx, event.ID); e != nil {
			return sent, e
		}
		sent++
	}
	return sent, nil
}
