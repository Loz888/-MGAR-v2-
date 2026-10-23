package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func approvalCall(t *testing.T, url, token, raw, id, approvalToken string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/tools", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	if id != "" {
		req.Header.Set("X-MGAR-Approval-ID", id)
		req.Header.Set("X-MGAR-Approval-Token", approvalToken)
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
func TestApprovalExactActionOneUse(t *testing.T) {
	s, c, cas := gatewayRoleFixture(t, "action")
	var executions atomic.Int32
	d := policy.Definition{Name: "synthetic_action", Version: "tool.v1", Role: "action", Effect: "non_idempotent", Resources: []string{"incident.sandbox"}, Parameters: map[string]policy.Parameter{"command": {Kind: "string", Enum: []string{"check", "changed"}}}, Timeout: time.Second, MaxOutput: 1024, Execute: func(context.Context, string, map[string]any) ([]byte, error) {
		executions.Add(1)
		return []byte(`{"synthetic":true}`), nil
	}}
	g, e := policy.NewGateway(s, cas, []policy.Definition{d}, func(policy.Denial) {})
	if e != nil {
		t.Fatal(e)
	}
	token, e := g.Issue(context.Background(), c, []string{"incident.sandbox"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(g)
	defer server.Close()
	raw := `{"schema":"tool-call.v1","request_id":"66666666-6666-4666-8666-666666666666","tool":"synthetic_action","version":"tool.v1","resource":"incident.sandbox","arguments":{"command":"check"}}`
	code, b := approvalCall(t, server.URL, token, raw, "", "")
	if code != 409 || executions.Load() != 0 {
		t.Fatalf("pending=%d %s", code, b)
	}
	var pending struct {
		Approval store.Approval `json:"approval"`
	}
	if json.Unmarshal(b, &pending) != nil || pending.Approval.State != "pending" {
		t.Fatal(string(b))
	}
	if _, e = s.PublishArtifact(context.Background(), c, cas, "finding", "text/plain", "action.v1", []byte("bypass approval")); e == nil {
		t.Fatal("pending approval became output")
	}
	approved, e := s.DecideApproval(context.Background(), pending.Approval.ID, pending.Approval.Digest, true)
	if e != nil || approved.Token == "" {
		t.Fatal(e)
	}
	changed := strings.Replace(raw, `"command":"check"`, `"command":"changed"`, 1)
	if code, _ = approvalCall(t, server.URL, token, changed, pending.Approval.ID, approved.Token); code != 403 {
		t.Fatal("changed action accepted")
	}
	if code, b = approvalCall(t, server.URL, token, raw, pending.Approval.ID, approved.Token); code != 200 || executions.Load() != 1 {
		t.Fatalf("approved=%d %s", code, b)
	}
	if code, _ = approvalCall(t, server.URL, token, raw, pending.Approval.ID, approved.Token); code != 403 || executions.Load() != 1 {
		t.Fatal("approval replay")
	}
	var state string
	var audit []byte
	if e = s.Pool.QueryRow(context.Background(), "SELECT state FROM approval_requests WHERE approval_id=$1", pending.Approval.ID).Scan(&state); e != nil || state != "consumed" {
		t.Fatal(state, e)
	}
	if e = s.Pool.QueryRow(context.Background(), "SELECT jsonb_agg(payload) FROM workflow_events WHERE kind LIKE 'approval_%'").Scan(&audit); e != nil || strings.Contains(string(audit), approved.Token) {
		t.Fatal("token audit leak")
	}
}
func TestReviewerCannotOverrideProgramFailure(t *testing.T) {
	v := verifier.Verdict{Schema: "verdict.v1", ArtifactID: "11111111-1111-4111-8111-111111111111", Version: verifier.Version, Status: "failed", Checks: []verifier.Check{{Name: "actual_rows", Status: "failed", Detail: "independent oracle mismatch"}}}
	result, e := verifier.AttachReview(v, []byte(`{"schema":"review.v1","summary":"inspect duplicate inputs","additional_checks":["check_duplicate_orders"]}`))
	if e != nil || result.Verdict.Status != "failed" {
		t.Fatal(e, result)
	}
	for _, raw := range []string{`{"schema":"review.v1","summary":"override","additional_checks":[],"status":"passed"}`, `{"schema":"review.v1","summary":"override","additional_checks":["force_pass"]}`} {
		if _, e = verifier.AttachReview(v, []byte(raw)); e == nil {
			t.Fatal("reviewer escalation")
		}
	}
}

func fixedApprovalRequest() store.ToolRequest {
	return store.ToolRequest{ID: "77777777-7777-4777-8777-777777777777", Capability: "synthetic_action", Version: "tool.v1", Resource: "incident.sandbox", Hash: strings.Repeat("a", 64), Effect: "non_idempotent"}
}
func TestApprovalTerminalScopeAndRollback(t *testing.T) {
	for _, mode := range []string{"denied", "expired_decision", "expired_consume", "wrong_token", "wrong_scope", "wrong_digest", "wrong_invocation", "stale_epoch", "outbox_rollback", "tool_unknown", "restart", "renewal"} {
		t.Run(mode, func(t *testing.T) {
			s, c, cas := gatewayRoleFixture(t, "action")
			ctx := context.Background()
			r := fixedApprovalRequest()
			a, e := s.RequestApproval(ctx, c, r, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "expired_decision" {
				_, e = s.Pool.Exec(ctx, "UPDATE approval_requests SET expiry=clock_timestamp()-interval '1 second' WHERE approval_id=$1", a.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.DecideApproval(ctx, a.ID, a.Digest, true); e != store.ErrApprovalExpired {
					t.Fatal(e)
				}
				var state string
				_ = s.Pool.QueryRow(ctx, "SELECT state FROM approval_requests WHERE approval_id=$1", a.ID).Scan(&state)
				if state != "expired" {
					t.Fatal(state)
				}
				return
			}
			if mode == "wrong_digest" {
				if _, e = s.DecideApproval(ctx, a.ID, strings.Repeat("b", 64), true); e == nil {
					t.Fatal("wrong digest approved")
				}
				return
			}
			decision, e := s.DecideApproval(ctx, a.ID, a.Digest, mode != "denied")
			if e != nil {
				t.Fatal(e)
			}
			if mode == "denied" {
				if decision.Token != "" {
					t.Fatal("denial emitted token")
				}
				if _, e = s.RequestApproval(ctx, c, r, time.Minute); e != store.ErrApprovalFinal {
					t.Fatal("denial reset", e)
				}
				if _, e = s.DecideApproval(ctx, a.ID, a.Digest, true); e != store.ErrApprovalFinal {
					t.Fatal("denial reopened", e)
				}
				return
			}
			token := decision.Token
			wantState := "approved"
			switch mode {
			case "expired_consume":
				_, e = s.Pool.Exec(ctx, "UPDATE approval_requests SET expiry=clock_timestamp()-interval '1 second' WHERE approval_id=$1", a.ID)
				wantState = "expired"
			case "wrong_token":
				token = strings.Repeat("b", 64)
			case "wrong_scope":
				r.Resource = "other.sandbox"
			case "wrong_invocation":
				r.ID = "88888888-8888-4888-8888-888888888888"
			case "stale_epoch":
				_, e = s.Pool.Exec(ctx, "UPDATE task_instances SET epoch=epoch+1 WHERE task_id=$1", c.TaskID)
			case "outbox_rollback":
				_, e = s.Pool.Exec(ctx, `CREATE FUNCTION reject_approval_tool() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='tool_reserved' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_approval_tool BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_approval_tool()`)
			case "renewal":
				c, e = s.RenewTask(ctx, c, time.Minute)
			}
			if e != nil {
				t.Fatal(e)
			}
			// A fresh Store object has no approval/token cache; authorization is durable.
			writer := s
			if mode == "restart" {
				writer = &store.Store{Pool: s.Pool}
			}
			_, e = writer.BeginApprovedTool(ctx, c, r, a.ID, token)
			if mode == "tool_unknown" || mode == "restart" || mode == "renewal" {
				if e != nil {
					t.Fatal(e)
				}
				wantState = "consumed"
				if mode == "tool_unknown" {
					if e = s.FinishTool(ctx, c, r, cas, nil, "unknown"); e != nil {
						t.Fatal(e)
					}
					if _, e = s.BeginApprovedTool(ctx, c, r, a.ID, token); e == nil {
						t.Fatal("unknown action retried")
					}
					if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "action.v1", []byte("unknown")); e == nil {
						t.Fatal("unknown action published")
					}
				}
			} else if e == nil {
				t.Fatal("invalid approval consumed")
			}
			var state string
			if e = s.Pool.QueryRow(ctx, "SELECT state FROM approval_requests WHERE approval_id=$1", a.ID).Scan(&state); e != nil || state != wantState {
				t.Fatal(state, e)
			}
			if mode == "outbox_rollback" {
				var count int
				_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM tool_invocations").Scan(&count)
				if count != 0 {
					t.Fatal("partial reservation")
				}
			}
		})
	}
}
func TestApprovalRecoveryExpiresPending(t *testing.T) {
	s, c, _ := gatewayRoleFixture(t, "action")
	ctx := context.Background()
	a, e := s.RequestApproval(ctx, c, fixedApprovalRequest(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE approval_requests SET expiry=clock_timestamp()-interval '1 second' WHERE approval_id=$1", a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReconcileCalls(ctx, c.RunID, "controller", 1, 0); e != nil {
		t.Fatal(e)
	}
	var state string
	_ = s.Pool.QueryRow(ctx, "SELECT state FROM approval_requests WHERE approval_id=$1", a.ID).Scan(&state)
	if state != "expired" {
		t.Fatal(state)
	}
}

func TestApprovalWorkerCredentialCannotSelfApprove(t *testing.T) {
	s, c, _ := gatewayRoleFixture(t, "action")
	ctx := context.Background()
	a, e := s.RequestApproval(ctx, c, fixedApprovalRequest(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	secret := make([]byte, 24)
	if _, e = rand.Read(secret); e != nil {
		t.Fatal(e)
	}
	password := hex.EncodeToString(secret)
	role := s.Pool.Config().ConnConfig.RuntimeParams["search_path"] + "_worker"
	ident := pgx.Identifier{role}.Sanitize()
	if _, e = s.Pool.Exec(ctx, "CREATE ROLE "+ident+" LOGIN PASSWORD '"+password+"' IN ROLE mgar_worker"); e != nil {
		t.Fatal("could not create owned synthetic worker")
	}
	t.Cleanup(func() {
		if _, e := s.Pool.Exec(context.Background(), "DROP ROLE "+ident); e != nil {
			t.Error("owned login cleanup failed")
		}
	})
	cfg, e := pgx.ParseConfig(testdb.DSN(t))
	if e != nil {
		t.Fatal("test config")
	}
	cfg.User = role
	cfg.Password = password
	cfg.RuntimeParams["search_path"] = s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal("restricted worker connect")
	}
	defer conn.Close(ctx)
	for _, query := range []string{"UPDATE approval_requests SET state='approved' WHERE approval_id=$1", "SELECT requested_scope FROM approval_requests WHERE approval_id=$1"} {
		if _, e = conn.Exec(ctx, query, a.ID); e == nil {
			t.Fatal("worker bypassed approval authority")
		}
	}
	if _, e = conn.Exec(ctx, "SET ROLE mgar_reader"); e == nil {
		t.Fatal("worker escalated identity")
	}
}
