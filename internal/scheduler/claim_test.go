package scheduler_test

import (
	"context"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"testing"
	"time"
)

func TestClaimPolicyBeforeDatabaseAccess(t *testing.T) {
	for _, id := range []store.WorkerIdentity{{Owner: "w", Role: "admin"}, {Owner: "", Role: "research"}} {
		_, e := (scheduler.Scheduler{}).Claim(context.Background(), "unused", id, time.Minute)
		if !errors.Is(e, store.ErrPolicy) {
			t.Fatalf("unsafe request not denied before DB access: %v", e)
		}
	}
}
