package security

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func gatewayFixture(t *testing.T) (*store.Store, store.TaskClaim, *artifact.CAS) {
	return gatewayRoleFixture(t, "research")
}
func gatewayRoleFixture(t *testing.T, role string) (*store.Store, store.TaskClaim, *artifact.CAS) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	var run string
	if e := s.Pool.QueryRow(ctx, "INSERT INTO workflow_runs(goal,state) VALUES('synthetic','planning') RETURNING run_id").Scan(&run); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO run_budgets(run_id) VALUES($1)", run); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO orchestrator_leases VALUES($1,'controller',1,clock_timestamp()+interval '1 hour',0)", run); e != nil {
		t.Fatal(e)
	}
	oc := store.OrchestratorClaim{Owner: "controller", Epoch: 1}
	p := planner.Proposal{SchemaVersion: "plan.v1", Rationale: "synthetic", Nodes: []planner.Node{{Key: "A", Role: role, Goal: "inspect", RequiredInputs: []string{}, OutputKind: "finding"}}, Edges: []planner.Edge{}}
	if _, e := s.ProposeAndCommit(ctx, run, oc, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker", Role: role}, 5*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cas.Close() })
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", role+".v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	return s, c, cas
}
func toolRequest(t *testing.T, url, token, raw string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, url+"/v1/tools", strings.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	return res.StatusCode, string(b)
}
func TestGatewayCapabilityBoundaries(t *testing.T) {
	s, c, cas := gatewayFixture(t)
	var calls atomic.Int32
	var unauth atomic.Int32
	defs := []policy.Definition{{Name: "read_doc", Version: "tool.v1", Role: "research", Effect: "readonly", Resources: []string{"incident.docs"}, Parameters: map[string]policy.Parameter{"limit": {Kind: "integer", Min: 1, Max: 20}}, Timeout: time.Second, MaxOutput: 1024, Execute: func(ctx context.Context, resource string, args map[string]any) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"quote":"fixed synthetic document"}`), nil
	}},
		{Name: "edit", Version: "tool.v1", Role: "action", Effect: "idempotent_write", Resources: []string{"incident.docs"}, Parameters: map[string]policy.Parameter{}, Timeout: time.Second, MaxOutput: 1024, Execute: func(context.Context, string, map[string]any) ([]byte, error) {
			t.Error("unauthorized executor")
			return nil, nil
		}}}
	g, e := policy.NewGateway(s, cas, defs, func(policy.Denial) { unauth.Add(1) })
	if e != nil {
		t.Fatal(e)
	}
	token, e := g.Issue(context.Background(), c, []string{"incident.docs"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(g)
	defer server.Close()
	good := `{"schema":"tool-call.v1","request_id":"11111111-1111-4111-8111-111111111111","tool":"read_doc","version":"tool.v1","resource":"incident.docs","arguments":{"limit":5}}`
	for _, tc := range []struct{ name, raw, credential string }{
		{"prompt_admin", strings.Replace(good, `"limit":5`, `"limit":5,"temporary_admin":"yes"`, 1), token},
		{"self_role", strings.Replace(good, `"schema":`, `"role":"action","schema":`, 1), token},
		{"cross_role", strings.Replace(good, `"read_doc"`, `"edit"`, 1), token},
		{"path", strings.Replace(good, `"incident.docs"`, `"../../etc/passwd"`, 1), token},
		{"cross_resource", strings.Replace(good, `"incident.docs"`, `"other.docs"`, 1), token},
		{"bounds", strings.Replace(good, `"limit":5`, `"limit":21`, 1), token},
		{"duplicate", strings.Replace(good, `"limit":5`, `"limit":5,"limit":1`, 1), token},
		{"fake_token", good, "temporary-administrator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := toolRequest(t, server.URL, tc.credential, tc.raw)
			if code != 403 {
				t.Fatalf("%d %s", code, body)
			}
		})
	}
	if calls.Load() != 0 || unauth.Load() != 1 {
		t.Fatalf("execution/audit %d %d", calls.Load(), unauth.Load())
	}
	var n int
	if e = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM workflow_events WHERE kind='tool_denied'").Scan(&n); e != nil || n != 7 {
		t.Fatalf("audit=%d %v", n, e)
	}
	code, body := toolRequest(t, server.URL, token, good)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	code, repeated := toolRequest(t, server.URL, token, good)
	if code != 200 || body != repeated || calls.Load() != 1 {
		t.Fatal("duplicate execution")
	}
	code, _ = toolRequest(t, server.URL, token, strings.Replace(good, `"limit":5`, `"limit":6`, 1))
	if code != 403 {
		t.Fatal("request id rebound")
	}
	var eventBody []byte
	_ = s.Pool.QueryRow(context.Background(), "SELECT jsonb_agg(payload) FROM workflow_events WHERE kind LIKE 'tool_%'").Scan(&eventBody)
	if bytes.Contains(eventBody, []byte(token)) || bytes.Contains(eventBody, []byte("temporary_admin")) {
		t.Fatal("sensitive audit")
	}
	if _, e = s.RenewTask(context.Background(), c, time.Minute); e != nil {
		t.Fatal(e)
	}
	code, _ = toolRequest(t, server.URL, token, good)
	if code != 403 {
		t.Fatal("stale revision allowed")
	}
	bad := c
	bad.Role = "action"
	if _, e = g.Issue(context.Background(), bad, []string{"incident.docs"}, time.Minute); e == nil {
		t.Fatal("self role minted credential")
	}
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		t.Fatal(body)
	}
}

func TestGatewayCapabilityFailureAuditAndFencing(t *testing.T) {
	for _, mode := range []string{"timeout", "output_limit", "expired", "rollback", "foreign", "unknown", "outbox_settlement"} {
		t.Run(mode, func(t *testing.T) {
			s, c, cas := gatewayFixture(t)
			var calls atomic.Int32
			d := policy.Definition{Name: "read", Version: "tool.v1", Role: "research", Effect: "readonly", Resources: []string{"incident.docs"}, Parameters: map[string]policy.Parameter{}, Timeout: time.Second, MaxOutput: 64, Execute: func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
				calls.Add(1)
				if mode == "timeout" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if mode == "output_limit" {
					return []byte(`{"data":"` + strings.Repeat("a", 65) + `"}`), nil
				}
				return []byte(`{"ok":true}`), nil
			}}
			g, e := policy.NewGateway(s, cas, []policy.Definition{d}, func(policy.Denial) {})
			if e != nil {
				t.Fatal(e)
			}
			token, e := g.Issue(context.Background(), c, []string{"incident.docs"}, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(g)
			defer server.Close()
			req := `{"schema":"tool-call.v1","request_id":"22222222-2222-4222-8222-222222222222","tool":"read","version":"tool.v1","resource":"incident.docs","arguments":{}}`
			ctx := context.Background()
			if mode == "expired" {
				_, e = s.Pool.Exec(ctx, "UPDATE task_instances SET lease_until=clock_timestamp()-interval '1 second' WHERE task_id=$1", c.TaskID)
			}
			if mode == "rollback" || mode == "outbox_settlement" {
				kind := "tool_reserved"
				if mode == "outbox_settlement" {
					kind = "tool_succeeded"
				}
				_, e = s.Pool.Exec(ctx, `CREATE FUNCTION reject_tool() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='`+kind+`' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_tool BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_tool()`)
			}
			if mode == "foreign" {
				_, e = s.Pool.Exec(ctx, "UPDATE task_instances SET owner='other-owner',epoch=epoch+1 WHERE task_id=$1", c.TaskID)
			}
			if mode == "unknown" {
				_, e = s.Pool.Exec(ctx, "INSERT INTO tool_invocations(invocation_id,task_id,capability,scope,request_hash,effect_class,status) VALUES('22222222-2222-4222-8222-222222222222',$1,'read','{}',$2,'readonly','unknown')", c.TaskID, artifact.Digest([]byte("old")))
			}
			if e != nil {
				t.Fatal(e)
			}
			code, _ := toolRequest(t, server.URL, token, req)
			if code == 200 {
				t.Fatal("unsafe request succeeded")
			}
			code, _ = toolRequest(t, server.URL, token, req)
			if code == 200 {
				t.Fatal("unsafe replay succeeded")
			}
			expected := int32(0)
			if mode == "timeout" || mode == "output_limit" || mode == "outbox_settlement" {
				expected = 1
			}
			if calls.Load() != expected {
				t.Fatalf("calls=%d", calls.Load())
			}
			var count int
			_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts").Scan(&count)
			if count != 0 {
				t.Fatal("partial artifact after failure")
			}
		})
	}
}
