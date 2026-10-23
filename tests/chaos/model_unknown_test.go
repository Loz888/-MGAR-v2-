package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func request() budget.ModelRequest {
	return budget.ModelRequest{LogicalID: "8da5c405-5524-4728-9509-e5c6b3b48438", Provider: "mock", Model: "mock.v1", Tokens: 64, CostMicros: 100000}
}
func modelFixture(t *testing.T) (*store.Store, string, store.OrchestratorClaim) {
	t.Helper()
	ctx := context.Background()
	s := testdb.New(t)
	w, c, e := s.CreateRunClaimed(ctx, "", "synthetic model recovery", 4, "fixture-owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginPlanning(ctx, w.RunID, c); e != nil {
		t.Fatal(e)
	}
	p := planner.Proposal{SchemaVersion: "plan.v1", Rationale: "synthetic model recovery", Nodes: []planner.Node{{Key: "A", Role: "research", Goal: "local fixture", RequiredInputs: []string{}, OutputKind: "finding"}}, Edges: []planner.Edge{}}
	if _, e = s.ProposeAndCommit(ctx, w.RunID, c, p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, w.RunID, c); e != nil {
		t.Fatal(e)
	}
	// Trusted test policy, not model-supplied budget authority.
	if _, e = s.Pool.Exec(ctx, "UPDATE run_budgets SET max_model_calls=1,max_tokens_est=64,max_cost_usd=0.100000 WHERE run_id=$1", w.RunID); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_CHAOS_CAS", filepath.Join(t.TempDir(), "cas"))
	return s, w.RunID, c
}
func claimModel(t *testing.T, s *store.Store, run string, ttl time.Duration) (store.TaskClaim, *artifact.CAS) {
	t.Helper()
	ctx := context.Background()
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "model-worker", Role: "research"}, ttl)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(os.Getenv("MGAR_CHAOS_CAS"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cas.Close() })
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	return c, cas
}
func fixtureHTTP(t *testing.T, path string) []byte {
	t.Helper()
	endpoint := os.Getenv("MGAR_CHAOS_HTTP")
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("loopback fixture required")
	}
	client := http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("no redirects") }}
	defer client.CloseIdleConnections()
	resp, e := client.Get(endpoint + path)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if e != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, e)
	}
	return body
}
func modelChild(t *testing.T, s *store.Store, run, mode string, send, pause func(barrier)) bool {
	ctx := context.Background()
	switch mode {
	case "model-late-reserve", "model-late-finish":
		var c store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_CHAOS_CLAIM")), &c); e != nil {
			t.Fatal(e)
		}
		var e error
		if mode == "model-late-reserve" {
			_, e = s.ReserveModelCall(ctx, c, request())
		} else {
			e = s.FinishModelCall(ctx, c, os.Getenv("MGAR_CHAOS_CALL"), budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 10, CostMicros: 25000, Source: "local_stub"})
		}
		if !errors.Is(e, store.ErrLeaseExpired) {
			t.Fatal("late model transaction committed", e)
		}
		send(barrier{Point: "expired"})
		return true
	case "model-replay":
		if _, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "replayed-worker", Role: "research"}, time.Minute); !errors.Is(e, store.ErrBadState) && !errors.Is(e, store.ErrUnsafeReplay) {
			t.Fatal("unsafe claim", e)
		}
		send(barrier{Point: "replay_denied"})
		return true
	case "model-reserve":
		var c store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_CHAOS_CLAIM")), &c); e != nil {
			t.Fatal(e)
		}
		pause(barrier{Point: "before_reserve"})
		call, e := s.ReserveModelCall(ctx, c, request())
		point := "reserved"
		if errors.Is(e, store.ErrUnsafeReplay) {
			point = "duplicate_denied"
		} else if e != nil {
			t.Fatal(e)
		}
		send(barrier{Point: point, CallID: call.ID})
		return true
	case "model-unknown", "model-verified":
		owner := store.OrchestratorClaim{}
		ttl := time.Second
		if mode == "model-verified" {
			var e error
			owner, e = s.TakeOrchestrator(ctx, run, "crashed-orchestrator", time.Second)
			if e != nil {
				t.Fatal(e)
			}
			ttl = time.Minute
		}
		c, cas := claimModel(t, s, run, ttl)
		call, e := s.ReserveModelCall(ctx, c, request())
		if e != nil {
			t.Fatal(e)
		}
		if mode == "model-unknown" {
			send(barrier{Point: "reserved", Claim: c, CallID: call.ID})
		}
		body := fixtureHTTP(t, "/model")
		if e = s.FinishModelCall(ctx, c, call.ID, budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 10, CostMicros: 25000, Source: "local_stub"}); e != nil {
			t.Fatal(e)
		}
		a, e := s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", body)
		if e != nil {
			t.Fatal(e)
		}
		// Independent synthetic checker fixture; not the P5 Verifier service.
		var verdict struct {
			Passed bool `json:"passed"`
		}
		if e = json.Unmarshal(fixtureHTTP(t, "/verify"), &verdict); e != nil || !verdict.Passed {
			t.Fatal("fixture verification", e)
		}
		if _, e = s.Pool.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'synthetic-chaos.v1',true)", a.ID); e != nil {
			t.Fatal(e)
		}
		pause(barrier{Point: "verified", Claim: c, Owner: owner, CallID: call.ID})
		return true
	}
	return false
}

func TestUnknownRecoveryRollbackAndFencing(t *testing.T) {
	s, run, owner := modelFixture(t)
	ctx := context.Background()
	c, _ := claimModel(t, s, run, time.Minute)
	if _, e := s.ReserveModelCall(ctx, c, request()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileCalls(ctx, run, "wrong-owner", owner.Epoch, owner.Revision); !errors.Is(e, store.ErrFenced) {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION reject_unknown() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='model_unknown' THEN RAISE EXCEPTION 'synthetic recovery failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_unknown BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_unknown()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileCalls(ctx, run, owner.Owner, owner.Epoch, owner.Revision); e == nil {
		t.Fatal("partial recovery accepted")
	}
	retained(t, ledger(t, s, run), "reserved")
	if _, e := s.Pool.Exec(ctx, "DROP TRIGGER reject_unknown ON outbox_events"); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		r, e := s.ReconcileCalls(ctx, run, owner.Owner, owner.Epoch, owner.Revision)
		if e != nil || r.Unknown != 1 || r.Pending != 0 {
			t.Fatal(r, e)
		}
	}
	retained(t, ledger(t, s, run), "unknown")
	var n int
	if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='model_unknown'", run).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestUnknownLeaseExpiryInsideTransaction(t *testing.T) {
	for _, mode := range []string{"model-late-reserve", "model-late-finish"} {
		t.Run(mode, func(t *testing.T) {
			s, run, _ := modelFixture(t)
			ctx := context.Background()
			c, _ := claimModel(t, s, run, time.Second)
			if mode == "model-late-finish" {
				call, e := s.ReserveModelCall(ctx, c, request())
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("MGAR_CHAOS_CALL", call.ID)
			}
			raw, e := json.Marshal(c)
			if e != nil {
				t.Fatal(e)
			}
			t.Setenv("MGAR_CHAOS_CLAIM", string(raw))
			tx, e := s.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(ctx)
			if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(923423)"); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION model_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind LIKE 'model_%' THEN PERFORM pg_advisory_xact_lock(923423); END IF; RETURN NEW; END $$; CREATE TRIGGER model_barrier BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION model_barrier()`); e != nil {
				t.Fatal(e)
			}
			child := startChaos(t, s, run, mode)
			awaitDB(t, s, "SELECT cardinality(pg_blocking_pids($1))>0", child.pid)
			awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", c.TaskID)
			if e = tx.Rollback(ctx); e != nil {
				t.Fatal(e)
			}
			if b := child.read(t); b.Point != "expired" {
				t.Fatal(b)
			}
			child.finish(t)
			if mode == "model-late-finish" {
				retained(t, ledger(t, s, run), "reserved")
			} else {
				var n, reserved int
				if e = s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM model_calls WHERE run_id=$1),reserved_model_calls FROM run_budgets WHERE run_id=$1", run).Scan(&n, &reserved); e != nil || n != 0 || reserved != 0 {
					t.Fatal(n, reserved, e)
				}
			}
		})
	}
}

func TestUnknownAdmissionBounds(t *testing.T) {
	for _, mode := range []string{"tokens", "cost", "provider", "logical_id", "negative", "missing_snapshot"} {
		t.Run(mode, func(t *testing.T) {
			s, run, _ := modelFixture(t)
			ctx := context.Background()
			var c store.TaskClaim
			if mode == "missing_snapshot" {
				var e error
				c, e = s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "no-snapshot", Role: "research"}, time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				c, e = s.StartTask(ctx, c)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				c, _ = claimModel(t, s, run, time.Minute)
			}
			r := request()
			want := store.ErrBudget
			switch mode {
			case "tokens":
				if _, e := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_tokens_est=63 WHERE run_id=$1", run); e != nil {
					t.Fatal(e)
				}
			case "cost":
				if _, e := s.Pool.Exec(ctx, "UPDATE run_budgets SET max_cost_usd=0.099999 WHERE run_id=$1", run); e != nil {
					t.Fatal(e)
				}
			case "provider":
				r.Provider = "paid"
				want = store.ErrPolicy
			case "logical_id":
				r.LogicalID = "invalid"
				want = store.ErrPolicy
			case "negative":
				r.CostMicros = -1
				want = store.ErrPolicy
			case "missing_snapshot":
				want = store.ErrStaleInput
			}
			if _, e := s.ReserveModelCall(ctx, c, r); !errors.Is(e, want) {
				t.Fatal(e, want)
			}
			var n, calls, tokens int
			var cost string
			if e := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM model_calls WHERE run_id=$1),reserved_model_calls,reserved_tokens,reserved_cost::text FROM run_budgets WHERE run_id=$1", run).Scan(&n, &calls, &tokens, &cost); e != nil || n != 0 || calls != 0 || tokens != 0 || cost != "0.000000" {
				t.Fatal(n, calls, tokens, cost, e)
			}
		})
	}
}

type ledgerState struct {
	Status                                     string
	Known                                      bool
	Usage                                      string
	Calls, Tokens, SettledCalls, SettledTokens int64
	Cost, SettledCost, Bound                   string
}

func ledger(t *testing.T, s *store.Store, run string) ledgerState {
	t.Helper()
	var v ledgerState
	if e := s.Pool.QueryRow(context.Background(), `SELECT m.status,m.usage_known,m.usage::text,b.reserved_model_calls,b.reserved_tokens,b.reserved_cost::text,b.settled_model_calls,b.settled_tokens,b.settled_cost::text,m.estimate_bound::text FROM model_calls m JOIN run_budgets b USING(run_id) WHERE m.run_id=$1`, run).Scan(&v.Status, &v.Known, &v.Usage, &v.Calls, &v.Tokens, &v.Cost, &v.SettledCalls, &v.SettledTokens, &v.SettledCost, &v.Bound); e != nil {
		t.Fatal(e)
	}
	return v
}
func retained(t *testing.T, v ledgerState, status string) {
	t.Helper()
	if v.Status != status || v.Known || v.Usage != "{}" || v.Calls != 1 || v.Tokens != 64 || v.Cost != "0.100000" || v.Bound != "0.100000" || v.SettledCalls != 0 || v.SettledTokens != 0 || v.SettledCost != "0.000000" {
		t.Fatal("reservation lost or fabricated usage", v)
	}
}

func TestUnknownModelCrash(t *testing.T) {
	s, run, c := modelFixture(t)
	ctx := context.Background()
	var requests atomic.Int64
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	t.Setenv("MGAR_CHAOS_HTTP", server.URL)
	child := startChaos(t, s, run, "model-unknown")
	b := child.read(t)
	if b.Point != "reserved" || b.CallID == "" {
		t.Fatal(b)
	}
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("request not sent")
	}
	child.kill(t)
	retained(t, ledger(t, s, run), "reserved")
	awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM task_instances WHERE task_id=$1", b.Claim.TaskID)
	if e := s.RelinquishOrchestrator(ctx, run, c); e != nil {
		t.Fatal(e)
	}
	sch := scheduler.Scheduler{Store: s}
	w, e := sch.RecoverRun(ctx, run, "recovery-owner")
	if e != nil || w.State != "interrupted" {
		t.Fatal(w, e)
	}
	retained(t, ledger(t, s, run), "unknown")
	owner, e := s.TakeOrchestrator(ctx, run, "recovery-owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ResumeWorkflow(ctx, run, owner); !errors.Is(e, store.ErrUnsafeReplay) {
		t.Fatal("unknown resume", e)
	}
	replay := startChaos(t, s, run, "model-replay")
	if result := replay.read(t); result.Point != "replay_denied" {
		t.Fatal(result)
	}
	replay.finish(t)
	if _, e = sch.RecoverRun(ctx, run, "recovery-owner"); e != nil {
		t.Fatal(e)
	}
	var calls, artifacts, events int
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_calls WHERE run_id=$1),(SELECT count(*) FROM artifacts WHERE run_id=$1),(SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='model_unknown')`, run).Scan(&calls, &artifacts, &events); e != nil || calls != 1 || artifacts != 0 || events != 1 || requests.Load() != 1 {
		t.Fatal("replayed or fabricated", calls, artifacts, events, requests.Load(), e)
	}
	t.Logf("H11/F-F actual_loopback_requests=%d ledger=%+v", requests.Load(), ledger(t, s, run))
	traceChaos(t, s, run, "H11/F-F", c.Epoch, owner.Epoch)
}

func TestVerifiedRecoveryCrash(t *testing.T) {
	s, run, c := modelFixture(t)
	ctx := context.Background()
	var requests, checks atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/model":
			requests.Add(1)
			_, _ = io.WriteString(w, "synthetic verified result")
		case "/verify":
			checks.Add(1)
			_, _ = io.WriteString(w, `{"passed":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MGAR_CHAOS_HTTP", server.URL)
	if e := s.RelinquishOrchestrator(ctx, run, c); e != nil {
		t.Fatal(e)
	}
	child := startChaos(t, s, run, "model-verified")
	b := child.read(t)
	if b.Point != "verified" || b.Owner.Owner != "crashed-orchestrator" {
		t.Fatal(b)
	}
	child.kill(t)
	awaitDB(t, s, "SELECT lease_until<=clock_timestamp() FROM orchestrator_leases WHERE run_id=$1", run)
	sch := scheduler.Scheduler{Store: s}
	w, e := sch.RecoverRun(ctx, run, "recovery-owner")
	if e != nil || w.State != "verifying" {
		t.Fatal(w, e)
	}
	w, e = sch.RecoverRun(ctx, run, "recovery-owner")
	if e != nil || w.State != "succeeded" {
		t.Fatal(w, e)
	}
	v := ledger(t, s, run)
	if v.Status != "succeeded" || !v.Known || v.Calls != 0 || v.Tokens != 0 || v.Cost != "0.000000" || v.SettledCalls != 1 || v.SettledTokens != 10 || v.SettledCost != "0.025000" {
		t.Fatal(v)
	}
	var calls, artifacts, verdicts int
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_calls WHERE run_id=$1),(SELECT count(*) FROM artifacts WHERE run_id=$1),(SELECT count(*) FROM artifact_verdicts v JOIN artifacts a USING(artifact_id) WHERE a.run_id=$1)`, run).Scan(&calls, &artifacts, &verdicts); e != nil || calls != 1 || artifacts != 1 || verdicts != 1 || requests.Load() != 1 || checks.Load() != 1 {
		t.Fatal(calls, artifacts, verdicts, requests.Load(), checks.Load(), e)
	}
	var newEpoch int64
	if e = s.Pool.QueryRow(ctx, "SELECT epoch FROM orchestrator_leases WHERE run_id=$1", run).Scan(&newEpoch); e != nil || newEpoch <= b.Owner.Epoch {
		t.Fatal(newEpoch, e)
	}
	t.Logf("H12/F-G actual_loopback_requests=%d independent_fixture_checks=%d ledger=%+v", requests.Load(), checks.Load(), v)
	traceChaos(t, s, run, "H12/F-G", b.Owner.Epoch, newEpoch)
}

func TestUnknownOutputCannotPublish(t *testing.T) {
	for _, status := range []string{"reserved", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, run, _ := modelFixture(t)
			c, cas := claimModel(t, s, run, time.Minute)
			ctx := context.Background()
			call, e := s.ReserveModelCall(ctx, c, request())
			if e != nil {
				t.Fatal(e)
			}
			if status == "unknown" {
				if e = s.FinishModelCall(ctx, c, call.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", []byte("unconfirmed model result")); !errors.Is(e, store.ErrUnsafeReplay) {
				t.Fatal("unconfirmed result published", e)
			}
			if e = s.CommitOutput(ctx, c, "23e0c809-6c6a-4d7c-b23e-18b0bdc5cb02"); !errors.Is(e, store.ErrUnsafeReplay) {
				t.Fatal("unchecked output path", e)
			}
			retained(t, ledger(t, s, run), status)
			var n int
			if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE run_id=$1", run).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}

func TestUnknownReservationConcurrentProcesses(t *testing.T) {
	s, run, _ := modelFixture(t)
	c, _ := claimModel(t, s, run, time.Minute)
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_CHAOS_CLAIM", string(raw))
	a := startChaos(t, s, run, "model-reserve")
	b := startChaos(t, s, run, "model-reserve")
	if a.read(t).Point != "before_reserve" || b.read(t).Point != "before_reserve" {
		t.Fatal("reservation barrier")
	}
	for _, child := range []*chaosChild{a, b} {
		if _, e = io.WriteString(child.in, "RESERVE\n"); e != nil {
			t.Fatal(e)
		}
	}
	results := map[string]int{a.read(t).Point: 1}
	results[b.read(t).Point]++
	a.finish(t)
	b.finish(t)
	if results["reserved"] != 1 || results["duplicate_denied"] != 1 {
		t.Fatal(results)
	}
	retained(t, ledger(t, s, run), "reserved")
	var n int
	if e = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM model_calls WHERE run_id=$1", run).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestUnknownAccountingGuards(t *testing.T) {
	for _, mode := range []string{"unknown", "settle", "failed", "pending", "budget", "fencing", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, run, owner := modelFixture(t)
			ctx := context.Background()
			c, _ := claimModel(t, s, run, time.Minute)
			if mode == "rollback" {
				if _, e := s.Pool.Exec(ctx, `CREATE FUNCTION reject_model() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind LIKE 'model_%' THEN RAISE EXCEPTION 'synthetic model outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_model BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_model()`); e != nil {
					t.Fatal(e)
				}
				if _, e := s.ReserveModelCall(ctx, c, request()); e == nil {
					t.Fatal("partial reservation accepted")
				}
				var n, reserved int
				if e := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM model_calls WHERE run_id=$1),reserved_model_calls FROM run_budgets WHERE run_id=$1", run).Scan(&n, &reserved); e != nil || n != 0 || reserved != 0 {
					t.Fatal(n, reserved, e)
				}
				if _, e := s.Pool.Exec(ctx, "DROP TRIGGER reject_model ON outbox_events"); e != nil {
					t.Fatal(e)
				}
			}
			call, e := s.ReserveModelCall(ctx, c, request())
			if e != nil {
				t.Fatal(e)
			}
			retained(t, ledger(t, s, run), "reserved")
			if _, e = s.ReserveModelCall(ctx, c, request()); !errors.Is(e, store.ErrUnsafeReplay) {
				t.Fatal(e)
			}
			invalid := budget.ModelUsage{Status: "succeeded", Known: false, Source: "unknown"}
			if e = s.FinishModelCall(ctx, c, call.ID, invalid); !errors.Is(e, store.ErrPolicy) {
				t.Fatal("fabricated zero success", e)
			}
			if mode == "unknown" {
				if e = s.FinishModelCall(ctx, c, call.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
					t.Fatal(e)
				}
				retained(t, ledger(t, s, run), "unknown")
				newer := request()
				newer.LogicalID = "d87b9ba7-6fd9-41fd-bb06-2ce3cb970388"
				if _, e = s.ReserveModelCall(ctx, c, newer); !errors.Is(e, store.ErrUnsafeReplay) {
					t.Fatal("new logical ID replay", e)
				}
				return
			}
			usage := budget.ModelUsage{Status: "succeeded", Known: true, Tokens: 10, CostMicros: 25000, Source: "local_stub"}
			if mode == "failed" {
				usage.Status = "failed"
			}
			if mode == "pending" {
				if e = s.RelinquishOrchestrator(ctx, run, owner); e != nil {
					t.Fatal(e)
				}
				w, e := (scheduler.Scheduler{Store: s}).RecoverRun(ctx, run, "recovery-owner")
				if e != nil || w.State != "running" {
					t.Fatal(w, e)
				}
				retained(t, ledger(t, s, run), "reserved")
			}
			if mode == "budget" {
				over := usage
				over.Tokens = 65
				if e = s.FinishModelCall(ctx, c, call.ID, over); !errors.Is(e, store.ErrBudget) {
					t.Fatal(e)
				}
				over = usage
				over.CostMicros = 100001
				if e = s.FinishModelCall(ctx, c, call.ID, over); !errors.Is(e, store.ErrBudget) {
					t.Fatal(e)
				}
				retained(t, ledger(t, s, run), "reserved")
			}
			if mode == "fencing" {
				stale := c
				stale.Epoch++
				if e = s.FinishModelCall(ctx, stale, call.ID, usage); !errors.Is(e, store.ErrFenced) {
					t.Fatal(e)
				}
				if _, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID); e != nil {
					t.Fatal(e)
				}
				if e = s.FinishModelCall(ctx, c, call.ID, usage); !errors.Is(e, store.ErrLeaseExpired) {
					t.Fatal(e)
				}
				retained(t, ledger(t, s, run), "reserved")
				return
			}
			if mode == "rollback" {
				if _, e = s.Pool.Exec(ctx, "CREATE TRIGGER reject_model BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_model()"); e != nil {
					t.Fatal(e)
				}
				if e = s.FinishModelCall(ctx, c, call.ID, usage); e == nil {
					t.Fatal("partial settlement accepted")
				}
				retained(t, ledger(t, s, run), "reserved")
				if _, e = s.Pool.Exec(ctx, "DROP TRIGGER reject_model ON outbox_events"); e != nil {
					t.Fatal(e)
				}
			}
			// Legitimate lease renewal changes revision, not reservation ownership.
			c, e = s.RenewTask(ctx, c, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.FinishModelCall(ctx, c, call.ID, usage); e != nil {
				t.Fatal(e)
			}
			before := ledger(t, s, run)
			if e = s.FinishModelCall(ctx, c, call.ID, usage); !errors.Is(e, store.ErrBadState) {
				t.Fatal("double settlement", e)
			}
			if after := ledger(t, s, run); after != before || after.SettledCalls != 1 || !after.Known || after.Status != usage.Status {
				t.Fatal(before, after)
			}
			newer := request()
			newer.LogicalID = "d87b9ba7-6fd9-41fd-bb06-2ce3cb970388"
			if _, e = s.ReserveModelCall(ctx, c, newer); !errors.Is(e, store.ErrBudget) {
				t.Fatal("call budget exceeded", e)
			}
		})
	}
}
