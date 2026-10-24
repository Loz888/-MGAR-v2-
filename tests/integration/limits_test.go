package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceIndependentPoolsAndCancellation(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	for _, class := range []string{"llm", "cpu", "db"} {
		if e := s.ConfigureResource(ctx, run, oc, class, 1, 2); e != nil {
			t.Fatal(e)
		}
	}
	for _, class := range []string{"llm", "cpu", "db"} {
		a, e := s.EnqueueResource(ctx, cs[0], class, 0, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		b, e := s.EnqueueResource(ctx, cs[1], class, 0, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if ok, e := s.AcquireResource(ctx, cs[0], a); e != nil || !ok {
			t.Fatal(ok, e)
		}
		if ok, e := s.AcquireResource(ctx, cs[1], b); e != nil || ok {
			t.Fatal("last permit duplicated", ok, e)
		}
		if e = s.FinishResource(ctx, a, true); e != nil {
			t.Fatal(e)
		}
		if ok, e := s.AcquireResource(ctx, cs[1], b); e != nil || !ok {
			t.Fatal("cancelled permit not recovered", ok, e)
		}
		if e = s.FinishResource(ctx, b, true); e != nil {
			t.Fatal(e)
		}
	}
	var held int
	if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM resource_requests WHERE state IN ('held','unknown')").Scan(&held); e != nil || held != 0 {
		t.Fatal(held, e)
	}
}

func TestResourceCrossProcessLastPermit(t *testing.T) {
	for _, class := range []string{"llm", "cpu", "db"} {
		t.Run(class, func(t *testing.T) {
			s, run, oc, cs, _ := budgetFixture(t, 2)
			ctx := context.Background()
			if e := s.ConfigureResource(ctx, run, oc, class, 1, 8); e != nil {
				t.Fatal(e)
			}
			cs[1] = resourceOtherRun(t, s)
			tickets := []store.ResourceTicket{}
			for _, c := range cs {
				ticket, e := s.EnqueueResource(ctx, c, class, 0, 10*time.Second)
				if e != nil {
					t.Fatal(e)
				}
				tickets = append(tickets, ticket)
			}
			raw, _ := json.Marshal(cs)
			t.Setenv("MGAR_TEST_RESOURCE_CLAIMS", string(raw))
			raw, _ = json.Marshal(tickets)
			t.Setenv("MGAR_TEST_RESOURCE_TICKETS", string(raw))
			raw, _ = json.Marshal([]string{tickets[0].Token, tickets[1].Token})
			t.Setenv("MGAR_TEST_RESOURCE_TOKENS", string(raw))
			outputs := processBatch(t, s, "resource", run, 2)
			w, d := 0, 0
			for _, out := range outputs {
				if strings.Contains(out, "RESULT winner") {
					w++
				}
				if strings.Contains(out, "RESULT denied") {
					d++
				}
			}
			if w != 1 || d != 1 {
				t.Fatal(outputs)
			}
			for _, ticket := range tickets {
				if e := s.FinishResource(ctx, ticket, true); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestResourceQueueLimitsAgingAndFencing(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 2)
	ctx := context.Background()
	if e := s.ConfigureResource(ctx, run, oc, "cpu", 1, 2); e != nil {
		t.Fatal(e)
	}
	low, e := s.EnqueueResource(ctx, cs[0], "cpu", 0, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	high, e := s.EnqueueResource(ctx, cs[1], "cpu", 10, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueResource(ctx, cs[0], "cpu", 0, time.Second); e != store.ErrResourceFull {
		t.Fatal("unbounded queue", e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE resource_requests SET created_at=created_at-interval '2 seconds' WHERE request_id=$1", low.ID); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.AcquireResource(ctx, cs[1], high); e != nil || ok {
		t.Fatal("aging ignored", ok, e)
	}
	if ok, e := s.AcquireResource(ctx, cs[0], low); e != nil || !ok {
		t.Fatal(ok, e)
	}
	forged := low
	forged.Token = strings.Repeat("0", 64)
	if e = s.FinishResource(ctx, forged, true); e == nil {
		t.Fatal("forged release")
	}
	if e = s.FinishResource(ctx, low, false); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.AcquireResource(ctx, cs[1], high); e != nil || ok {
		t.Fatal("unconfirmed capacity reallocated", ok, e)
	}
	if e = s.FinishResource(ctx, low, true); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.AcquireResource(ctx, cs[1], high); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = s.FinishResource(ctx, high, true); e != nil {
		t.Fatal(e)
	}
}
func TestBackpressureConcurrentClassesAndQueueTimeout(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 6)
	ctx := context.Background()
	classes := []string{"llm", "cpu", "db"}
	for _, class := range classes {
		if e := s.ConfigureResource(ctx, run, oc, class, 1, 8); e != nil {
			t.Fatal(e)
		}
	}
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	active := [3]atomic.Int32{}
	started := make(chan string, 6)
	release := make(chan struct{})
	done := make(chan error, 6)
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for i := 0; i < 6; i++ {
		go func(i int) {
			index := i % 3
			done <- resources.Run(requestCtx, cs[i], classes[index], 0, func(ctx context.Context) (bool, error) {
				if active[index].Add(1) != 1 {
					active[index].Add(-1)
					return true, errors.New("class over quota")
				}
				defer active[index].Add(-1)
				started <- classes[index]
				select {
				case <-release:
					return true, nil
				case <-ctx.Done():
					return true, ctx.Err()
				}
			})
		}(i)
	}
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case class := <-started:
			seen[class] = true
		case <-requestCtx.Done():
			t.Fatal("class isolation stalled")
		}
	}
	blockedCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	executed := false
	e = resources.Run(blockedCtx, cs[0], "cpu", 0, func(context.Context) (bool, error) { executed = true; return true, nil })
	stop()
	if e == nil || executed {
		t.Fatal("queue deadline bypassed")
	}
	close(release)
	for i := 0; i < 6; i++ {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	var held, queued int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FILTER(WHERE state IN ('held','unknown')),count(*) FILTER(WHERE state='queued') FROM resource_requests").Scan(&held, &queued); e != nil || held != 0 || queued != 0 {
		t.Fatal("permits leaked", held, queued, e)
	}
}
func TestBackpressureCancelsRevokedTaskAndReleases(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- resources.Run(ctx, cs[0], "db", 0, func(ctx context.Context) (bool, error) { close(started); <-ctx.Done(); return true, ctx.Err() })
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	w, e := s.Workflow(ctx, run)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CancelWorkflow(ctx, run, w.Revision); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("revoked task kept running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel not propagated")
	}
	var n int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM resource_requests WHERE state IN ('held','unknown')").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func resourceOtherRun(t *testing.T, s *store.Store) store.TaskClaim {
	t.Helper()
	ctx := context.Background()
	run, oc := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, oc, simplePlan()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "other-run-worker", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestResourceModelBudgetAndUnknownStop(t *testing.T) {
	s, run, _, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	resources, e := scheduler.NewResources(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	_, e = resources.Model(ctx, cs[0], budgetRequest(0), func(context.Context) (budget.ModelUsage, bool, error) {
		return budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 10, InputTokens: 10, CostMicros: 10, PricingVersion: "synthetic.v1", Source: "local_stub"}, true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	report, e := s.BudgetReport(ctx, run)
	if e != nil || !report.Conserved || report.SettledModels != 1 {
		t.Fatal(report, e)
	}
	if e = resources.Run(ctx, cs[0], "cpu", 0, func(context.Context) (bool, error) { return false, errors.New("executor stop unconfirmed") }); !errors.Is(e, store.ErrResourceUnconfirmed) {
		t.Fatal(e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM resource_requests WHERE class='cpu' AND state='unknown'").Scan(&count); e != nil || count != 1 {
		t.Fatal("unknown prematurely released", count, e)
	}
}
func TestResourceDeadlineFencingAndOutboxRollback(t *testing.T) {
	for _, mode := range []string{"deadline", "epoch", "rollback", "private_token"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, cs, _ := budgetFixture(t, 1)
			ctx := context.Background()
			ticket, e := s.EnqueueResource(ctx, cs[0], "db", 0, time.Second)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "deadline":
				_, e = s.Pool.Exec(ctx, "UPDATE resource_requests SET created_at=clock_timestamp()-interval '2 seconds',deadline=clock_timestamp()-interval '1 second' WHERE request_id=$1", ticket.ID)
			case "epoch":
				_, e = s.Pool.Exec(ctx, "UPDATE task_instances SET epoch=epoch+1 WHERE task_id=$1", cs[0].TaskID)
			case "rollback":
				_, e = s.Pool.Exec(ctx, `CREATE FUNCTION reject_resource() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='resource_held' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_resource BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_resource()`)
			case "private_token":
				raw, _ := json.Marshal(ticket)
				if strings.Contains(string(raw), ticket.Token) {
					t.Fatal("ticket JSON exposed credential")
				}
				ticket.Token = strings.Repeat("0", 64)
			}
			if e != nil {
				t.Fatal(e)
			}
			if ok, e := s.AcquireResource(ctx, cs[0], ticket); e == nil || ok {
				t.Fatal("unsafe admission", ok, e)
			}
			var held int
			if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM resource_requests WHERE state='held'").Scan(&held); e != nil || held != 0 {
				t.Fatal(held, e)
			}
		})
	}
}

func TestResourceControllerConfirmedRecoveryRequiresAuthority(t *testing.T) {
	s, run, oc, cs, _ := budgetFixture(t, 1)
	ctx := context.Background()
	ticket, e := s.EnqueueResource(ctx, cs[0], "cpu", 0, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if ok, e := s.AcquireResource(ctx, cs[0], ticket); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = s.FinishResource(ctx, ticket, false); e != nil {
		t.Fatal(e)
	}
	// This synthetic executor has no subprocess. Its test controller has now
	// independently confirmed stop and records that fact, not a timed lease guess.
	proof := artifact.Digest([]byte("synthetic executor returned; no process exists"))
	bad := oc
	bad.Owner = "model-worker"
	if e = s.ConfirmResourceStopped(ctx, run, bad, ticket.ID, proof); e == nil {
		t.Fatal("worker manufactured stop authority")
	}
	if e = s.ConfirmResourceStopped(ctx, run, oc, ticket.ID, "invalid"); e == nil {
		t.Fatal("missing proof digest")
	}
	if e = s.ConfirmResourceStopped(ctx, run, oc, ticket.ID, proof); e != nil {
		t.Fatal(e)
	}
	var state, recorded string
	if e = s.Pool.QueryRow(ctx, "SELECT state FROM resource_requests WHERE request_id=$1", ticket.ID).Scan(&state); e != nil || state != "released" {
		t.Fatal(state, e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT payload->>'stop_proof_sha256' FROM workflow_events WHERE entity_id=$1 AND kind='resource_released'", ticket.ID).Scan(&recorded); e != nil || recorded != proof {
		t.Fatal("missing cleanup audit", e)
	}
}
