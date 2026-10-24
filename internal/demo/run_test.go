package demo

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/google/uuid"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_TEST_DATABASE_URL", testdb.DSN(t))
	return Options{Root: root, Case: "offline-002", Fault: "none"}
}
func TestRuntimeRealProcessesAndDurableVerification(t *testing.T) {
	o := testOptions(t)
	for _, fault := range []string{"none", "forge-data"} {
		t.Run(fault, func(t *testing.T) {
			o.Fault = fault
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			r, e := Run(ctx, o)
			if e != nil {
				t.Fatal(e)
			}
			want := "succeeded"
			if fault != "none" {
				want = "failed"
			}
			if r.State != want || !r.Budget.Conserved || !r.SourceUnchanged || r.Parallel < 2 || r.TraceState != "complete_projection" {
				t.Fatalf("state=%s error=%s budget=%+v parallel=%d", r.State, r.ErrorKind, r.Budget, r.Parallel)
			}
			if r.Budget.SettledModels < 5 || r.Budget.SettledTools < 2 || r.Budget.ReservedModels != 0 || r.Budget.ReservedTools != 0 {
				t.Fatal("pre-call model/tool ledger not settled", r.Budget)
			}
			if fault != "none" {
				failed := false
				for _, a := range r.Artifacts {
					if a.Verdict.Status == "failed" {
						failed = true
					}
				}
				if !failed {
					t.Fatal("missing independent rejection")
				}
			}
		})
	}
}
func TestRuntimeReaderCannotWriteOrReadControlState(t *testing.T) {
	o := testOptions(t)
	ctx := context.Background()
	f, e := newFixture(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := f.close(); e != nil {
			t.Fatal(e)
		}
	}()
	conn, e := f.reader.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	tx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var readonly string
	if e = tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readonly); e != nil || readonly != "on" {
		t.Fatal("reader is writable")
	}
	_, e = tx.Exec(ctx, "DELETE FROM "+pgx.Identifier{f.schema, "orders"}.Sanitize())
	var pgErr *pgconn.PgError
	if !errors.As(e, &pgErr) || pgErr.Code != "25006" {
		t.Fatal("write was not denied")
	}
	_ = tx.Rollback(ctx)
	_, e = conn.Exec(ctx, "SELECT * FROM "+pgx.Identifier{f.schema, "workflow_runs"}.Sanitize())
	if !errors.As(e, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("control state readable")
	}
	var elevated bool
	e = conn.QueryRow(ctx, "SELECT rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname=current_user").Scan(&elevated)
	if e != nil || elevated {
		t.Fatal("reader elevated")
	}
}

func TestIndependentReplayReservesBeforeExecution(t *testing.T) {
	o := testOptions(t)
	ctx := context.Background()
	f, e := newFixture(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := f.close(); e != nil {
			t.Fatal(e)
		}
	}()
	run := uuid.NewString()
	_, owner, e := f.s.CreateRunClaimed(ctx, run, "test independent verifier quota", 8, "controller", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.BeginPlanning(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	if e = f.s.ConfigureBudget(ctx, run, owner, budget.Limits{TaskConcurrency: 4, ConcurrentCalls: 4, Delegations: 8}); e != nil {
		t.Fatal(e)
	}
	p := proposal("analysis")
	p.BaseVersion = 0
	if _, e = f.s.ProposeAndCommit(ctx, run, owner, p); e != nil {
		t.Fatal(e)
	}
	if e = f.startVerification(ctx, run, owner); e != nil {
		t.Fatal(e)
	}
	replay := admittedReplay{f: f, inner: verifier.Reexecutor{SQL: map[string]verifier.SQLJob{"paid-metrics.v1": f.sql}}}
	_, e = replay.Replay(ctx, verifier.Recipe{Method: "readonly_sql", Command: "paid-metrics.v1", Dataset: "orders.v1", InputDigest: f.sql.InputDigest})
	if !errors.Is(e, store.ErrBudget) || f.verification.calls != 0 {
		t.Fatal("unreserved independent query executed", e)
	}
	b, e := f.s.BudgetReport(ctx, run)
	if e != nil || !b.Conserved || b.PendingTools != 0 || b.SettledTools != 0 {
		t.Fatal("verification rejection changed quota", e)
	}
}
func TestModelHTTPRejectsIdentityScopeSchemaAndToken(t *testing.T) {
	c := store.TaskClaim{RunID: uuid.NewString(), TaskID: uuid.NewString(), PlanVersion: 2}
	token := strings.Repeat("a", 64)
	base := mockRequest{RunID: c.RunID, TaskID: c.TaskID, PlanVersion: 2, LogicalID: uuid.NewString(), Turn: 1, CaseID: "offline-001", Goal: "fixed", Snapshot: "{}", Tools: "[]", Observations: []mockObservation{}}
	for _, mode := range []string{"valid", "token", "run", "task", "version", "goal", "snapshot", "turn", "query", "method", "extra", "duplicate", "oversized", "tools"} {
		t.Run(mode, func(t *testing.T) {
			value := base
			r := httptest.NewRequest("POST", "http://127.0.0.1/v1/models", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			switch mode {
			case "token":
				r.Header.Set("Authorization", "Bearer forged")
			case "run":
				value.RunID = uuid.NewString()
			case "task":
				value.TaskID = uuid.NewString()
			case "version":
				value.PlanVersion++
			case "goal":
				value.Goal = "replace"
			case "snapshot":
				value.Snapshot = "forged"
			case "turn":
				value.Turn = 9
			case "query":
				r.URL.RawQuery = "role=action"
			case "method":
				r.Method = "GET"
			case "tools":
				value.Tools = "invalid"
			}
			raw := inputcontext.Canonical(value)
			switch mode {
			case "extra":
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				m["model"] = "paid"
				raw = inputcontext.Canonical(m)
			case "duplicate":
				raw = append([]byte(`{"run_id":"forged",`), raw[1:]...)
			case "oversized":
				raw = make([]byte, 65537)
			}
			_, e := parseModelRequest(r, raw, token, c, "offline-001", "fixed", "{}", 1)
			if (e == nil) != (mode == "valid") {
				t.Fatal("model boundary", mode, e)
			}
		})
	}
}
