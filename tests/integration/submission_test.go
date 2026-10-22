package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func TestSubmissionInitialOwnerCannotBeStolenBeforePlan(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	w, initial, err := s.CreateRunClaimed(ctx, "", "synthetic submission", 4, "submitter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var persisted store.OrchestratorClaim
	var live bool
	if err = s.Pool.QueryRow(ctx, "SELECT owner_id,epoch,revision,lease_until>clock_timestamp() FROM orchestrator_leases WHERE run_id=$1", w.RunID).
		Scan(&persisted.Owner, &persisted.Epoch, &persisted.Revision, &live); err != nil {
		t.Fatal(err)
	}
	if persisted != initial || initial.Owner != "submitter" || initial.Epoch != 1 || initial.Revision != 0 || !live {
		t.Fatalf("returned initial claim was not persisted: returned=%+v persisted=%+v live=%v", initial, persisted, live)
	}
	var max, used, running, events, outboxes int
	if err = s.Pool.QueryRow(ctx, `SELECT max_delegations,used_delegations,running_count,
	 (SELECT count(*) FROM workflow_events WHERE run_id=$1),
	 (SELECT count(*) FROM outbox_events WHERE aggregate_id=$1)
	 FROM run_budgets WHERE run_id=$1`, w.RunID).Scan(&max, &used, &running, &events, &outboxes); err != nil {
		t.Fatal(err)
	}
	if max != 4 || used != 0 || running != 0 || events != 2 || outboxes != 2 {
		t.Fatalf("incomplete atomic submission: max=%d used=%d running=%d events=%d outboxes=%d", max, used, running, events, outboxes)
	}
	if _, err = s.TakeOrchestrator(ctx, w.RunID, "daemon", time.Minute); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("daemon stole the initial live lease: %v", err)
	}
	if _, err = s.BeginPlanning(ctx, w.RunID, store.OrchestratorClaim{Owner: "daemon", Epoch: 1, Revision: 0}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("daemon advanced the submission without its initial lease: %v", err)
	}
	if _, err = s.BeginPlanning(ctx, w.RunID, initial); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProposeAndCommit(ctx, w.RunID, initial, simplePlan()); err != nil {
		t.Fatal(err)
	}
	if planned, err := s.Workflow(ctx, w.RunID); err != nil || planned.State != "running" || planned.ActivePlanVersion != 1 {
		t.Fatalf("initial owner could not publish its first plan: %+v %v", planned, err)
	}
}

func assertNoSubmissionRows(t *testing.T, s *store.Store) {
	t.Helper()
	for _, table := range []string{"workflow_runs", "run_budgets", "orchestrator_leases", "workflow_events", "outbox_events"} {
		var count int
		if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial submission in %s: count=%d error=%v", table, count, err)
		}
	}
}

func TestSubmissionRejectsInvalidInitialClaim(t *testing.T) {
	for _, invalid := range []struct {
		name, owner, goal string
		max               int
		ttl               time.Duration
	}{
		{"empty_owner", "", "synthetic", 4, time.Minute},
		{"blank_owner", " \t", "synthetic", 4, time.Minute},
		{"zero_ttl", "submitter", "synthetic", 4, 0},
		{"negative_ttl", "submitter", "synthetic", 4, -time.Second},
		{"oversized_ttl", "submitter", "synthetic", 4, 5*time.Minute + time.Nanosecond},
		{"empty_goal", "submitter", "", 4, time.Minute},
		{"zero_delegations", "submitter", "synthetic", 0, time.Minute},
		{"oversized_delegations", "submitter", "synthetic", 1025, time.Minute},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			s := testdb.New(t)
			if _, _, err := s.CreateRunClaimed(context.Background(), "", invalid.goal, invalid.max, invalid.owner, invalid.ttl); !errors.Is(err, store.ErrPolicy) {
				t.Fatalf("invalid initial claim accepted: %v", err)
			}
			assertNoSubmissionRows(t, s)
		})
	}
}

func TestSubmissionClaimOutboxFailureRollsBackEverything(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	_, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_initial_claim() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF NEW.event_kind='orchestrator_claimed' THEN RAISE EXCEPTION 'synthetic claim outbox failure'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER injected_claim BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_initial_claim()`)
	if err != nil {
		t.Fatal(err)
	}
	const run = "11111111-1111-4111-8111-111111111111"
	if _, _, err = s.CreateRunClaimed(ctx, run, "synthetic submission", 4, "submitter", time.Minute); err == nil {
		t.Fatal("expected initial-claim outbox failure")
	}
	assertNoSubmissionRows(t, s)
	if _, err = s.Pool.Exec(ctx, "DROP TRIGGER injected_claim ON outbox_events"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateRunClaimed(ctx, run, "retry after complete rollback", 4, "submitter", time.Minute); err != nil {
		t.Fatalf("rolled-back submission permanently consumed its Run ID: %v", err)
	}
}

func TestSubmissionDuplicateIDPreservesInitialClaim(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	w, initial, err := s.CreateRunClaimed(ctx, "", "original synthetic goal", 4, "submitter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateRunClaimed(ctx, w.RunID, "replacement goal", 5, "different-owner", time.Minute); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate submission was accepted: %v", err)
	}
	persisted, err := s.Workflow(ctx, w.RunID)
	if err != nil || persisted.Goal != w.Goal || persisted.Revision != w.Revision {
		t.Fatalf("duplicate submission changed the original Run: %+v %v", persisted, err)
	}
	if claim, err := s.TakeOrchestrator(ctx, w.RunID, initial.Owner, time.Minute); err != nil || claim != initial {
		t.Fatalf("duplicate submission replaced the initial lease: %+v %v", claim, err)
	}
	var events int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", w.RunID).Scan(&events); err != nil || events != 2 {
		t.Fatalf("duplicate submission emitted partial events: %d %v", events, err)
	}
}
