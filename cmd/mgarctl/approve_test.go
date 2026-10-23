package main

import (
	"bytes"
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

func TestCLIApprovalExactDigestAndNoAutomaticExecution(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	s := testdb.New(t)
	ctx := context.Background()
	w, oc, e := s.CreateRunClaimed(ctx, "", "synthetic approval", 8, "controller", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginPlanning(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	p := incidentPlan()
	p.Nodes[0].Role = "action"
	if _, e = s.ProposeAndCommit(ctx, w.RunID, oc, p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "action", Role: "action"}, time.Minute)
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
	request := store.ToolRequest{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Capability: "synthetic", Version: "tool.v1", Resource: "sandbox", Hash: strings.Repeat("c", 64), Effect: "non_idempotent"}
	a, e := s.RequestApproval(ctx, c, request, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	if code := run(ctx, []string{"approve", a.ID, "--action-digest", strings.Repeat("d", 64)}, &out, &errs, s); code != 1 {
		t.Fatal("wrong digest accepted")
	}
	out.Reset()
	errs.Reset()
	if code := run(ctx, []string{"approve", a.ID, "--action-digest", a.Digest}, &out, &errs, s); code != 0 {
		t.Fatal(code, errs.String())
	}
	var decision store.ApprovalDecision
	if json.Unmarshal(out.Bytes(), &decision) != nil || decision.Approval.State != "approved" || len(decision.Token) != 64 || decision.Approval.Approver == "" {
		t.Fatal("invalid approval response")
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM tool_invocations").Scan(&count); e != nil || count != 0 {
		t.Fatal("CLI executed action")
	}
	out.Reset()
	if code := run(ctx, []string{"approve", a.ID, "--action-digest", a.Digest}, &out, &errs, s); code != 1 || out.Len() != 0 {
		t.Fatal("token reissued")
	}
	for _, args := range [][]string{{"approve"}, {"approve", a.ID, "--action-digest", "bad"}, {"approve", a.ID, "--action-digest", a.Digest, "--approver", "model-admin"}} {
		if run(ctx, args, &out, &errs, nil) != 2 {
			t.Fatal("invalid CLI opened database")
		}
	}
}
