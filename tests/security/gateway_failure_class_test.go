package security

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/policy"
)

type stoppedReadError bool

func (e stoppedReadError) Error() string          { return "synthetic readonly failure" }
func (e stoppedReadError) ExecutionStopped() bool { return bool(e) }

func TestGatewayReadonlyFailureClassification(t *testing.T) {
	for _, mode := range []string{"stopped-read", "wrapped-stopped-read", "no-proof", "not-stopped", "timeout", "stopped-write", "malformed", "release-error"} {
		t.Run(mode, func(t *testing.T) {
			role, effect := "research", "readonly"
			if mode == "stopped-write" {
				role, effect = "action", "idempotent_write"
			}
			s, c, cas := gatewayRoleFixture(t, role)
			var executed atomic.Int32
			def := policy.Definition{Name: "read", Version: "tool.v1", Role: role, Effect: effect, Resources: []string{"incident.docs"}, Parameters: map[string]policy.Parameter{}, Timeout: 100 * time.Millisecond, MaxOutput: 64, Execute: func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
				executed.Add(1)
				switch mode {
				case "stopped-read", "stopped-write", "release-error":
					return nil, stoppedReadError(true)
				case "wrapped-stopped-read":
					return nil, fmt.Errorf("wrapper: %w", errors.Join(stoppedReadError(true)))
				case "not-stopped":
					return nil, stoppedReadError(false)
				case "timeout":
					<-ctx.Done()
					return nil, stoppedReadError(true)
				case "malformed":
					return []byte(`bad json`), nil
				default:
					return nil, errors.New("no execution evidence")
				}
			}}
			if mode == "release-error" {
				_, e := s.Pool.Exec(context.Background(), `CREATE FUNCTION reject_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='released' THEN RAISE EXCEPTION 'synthetic release failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_release BEFORE UPDATE ON resource_requests FOR EACH ROW EXECUTE FUNCTION reject_release()`)
				if e != nil {
					t.Fatal(e)
				}
			}
			g, e := policy.NewGateway(s, cas, []policy.Definition{def}, func(policy.Denial) {})
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
			code, _ := toolRequest(t, server.URL, token, req)
			expected := "unknown"
			expectedCode := 409
			if mode == "stopped-read" || mode == "wrapped-stopped-read" {
				expected = "failed"
				expectedCode = 422
			}
			var status string
			if e = s.Pool.QueryRow(context.Background(), "SELECT status FROM tool_invocations WHERE task_id=$1", c.TaskID).Scan(&status); e != nil {
				t.Fatal(e)
			}
			if code != expectedCode || status != expected {
				t.Fatalf("status=%s code=%d wanted %s/%d", status, code, expected, expectedCode)
			}
			// Neither a settled failure nor an unknown can be replayed with the same id.
			if code, _ = toolRequest(t, server.URL, token, req); code == 200 || executed.Load() != 1 {
				t.Fatal("failure replayed", code, executed.Load())
			}
			b, e := s.BudgetReport(context.Background(), c.RunID)
			if e != nil || !b.Conserved {
				t.Fatal(b, e)
			}
			if expected == "failed" && (b.UnknownTools != 0 || b.SettledTools != 1 || b.ReservedTools != 0) {
				t.Fatal(b)
			}
			if expected == "unknown" && (b.UnknownTools != 1 || b.ReservedTools != 1) {
				t.Fatal(b)
			}
		})
	}
}
