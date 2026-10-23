package integration

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGatewayCrossProcessReservation(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, oc := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, oc, simplePlan()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "tool-worker", Role: "research"}, time.Minute)
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
	raw, _ := json.Marshal(c)
	t.Setenv("MGAR_TEST_CLAIM", string(raw))
	outputs := processBatch(t, s, "tool", run, 2)
	wins, denied := 0, 0
	for _, s := range outputs {
		if strings.Contains(s, "RESULT winner") {
			wins++
		}
		if strings.Contains(s, "RESULT denied") {
			denied++
		}
	}
	if wins != 1 || denied != 1 {
		t.Fatal(outputs)
	}
	if _, e = s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "research.v1", []byte("unconfirmed tool")); e == nil {
		t.Fatal("unsettled tool became task output")
	}
}

func TestApprovalCrossProcessConsumption(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	run, oc := planFixture(t, s)
	p := simplePlan()
	p.Nodes[0].Role = "action"
	if _, e := s.ProposeAndCommit(ctx, run, oc, p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "action-worker", Role: "action"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	r := store.ToolRequest{ID: "99999999-9999-4999-8999-999999999999", Capability: "synthetic", Version: "tool.v1", Resource: "sandbox", Hash: artifact.Digest([]byte("fixed")), Effect: "non_idempotent"}
	a, e := s.RequestApproval(ctx, c, r, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	decision, e := s.DecideApproval(ctx, a.ID, a.Digest, true)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	t.Setenv("MGAR_TEST_CLAIM", string(raw))
	raw, _ = json.Marshal(r)
	t.Setenv("MGAR_TEST_TOOL_REQUEST", string(raw))
	t.Setenv("MGAR_TEST_APPROVAL_ID", a.ID)
	t.Setenv("MGAR_TEST_APPROVAL_TOKEN", decision.Token)
	outputs := processBatch(t, s, "approval", run, 2)
	wins, denied := 0, 0
	for _, s := range outputs {
		if strings.Contains(s, "RESULT winner") {
			wins++
		}
		if strings.Contains(s, "RESULT denied") {
			denied++
		}
	}
	if wins != 1 || denied != 1 {
		t.Fatal(outputs)
	}
	var reservations, consumptions int
	if e = s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM tool_invocations),(SELECT count(*) FROM workflow_events WHERE kind='approval_consumed')").Scan(&reservations, &consumptions); e != nil || reservations != 1 || consumptions != 1 {
		t.Fatal(reservations, consumptions, e)
	}
}
