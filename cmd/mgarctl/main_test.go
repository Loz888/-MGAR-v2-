package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
)

func TestCLIWorkflowAndTerminalGuards(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	s := testdb.New(t)
	var out, errs bytes.Buffer
	call := func(args ...string) int {
		out.Reset()
		errs.Reset()
		return run(context.Background(), args, &out, &errs, s)
	}
	if code := call("submit", "--fixture", "incident", "--provider", "mock"); code != 0 {
		t.Fatalf("submit exit=%d: %s", code, &errs)
	}
	var w store.Workflow
	if err := json.Unmarshal(out.Bytes(), &w); err != nil || w.State != "running" || w.ActivePlanVersion != 1 {
		t.Fatalf("submit response=%s: %v", &out, err)
	}
	id := w.RunID
	if call("status", id) != 0 || !bytes.Contains(out.Bytes(), []byte(id)) {
		t.Fatalf("status: %s", &errs)
	}
	if call("cancel", id) != 0 {
		t.Fatalf("cancel: %s", &errs)
	}
	if err := json.Unmarshal(out.Bytes(), &w); err != nil || w.State != "cancelled" || w.FinishedAt == nil {
		t.Fatalf("cancel response=%s: %v", &out, err)
	}
	frozen := w
	if call("resume", id) == 0 || call("submit", "--run-id", id) == 0 {
		t.Fatal("terminal resume/duplicate submit was accepted")
	}
	w, err := s.Workflow(context.Background(), id)
	if err != nil || w.Revision != frozen.Revision || !w.FinishedAt.Equal(*frozen.FinishedAt) {
		t.Fatalf("terminal was overwritten: %+v %v", w, err)
	}
	var calls int
	if err = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM model_calls").Scan(&calls); err != nil || calls != 0 {
		t.Fatalf("CLI executed models: %d %v", calls, err)
	}
}

func TestCLIHealthAndInvalidCommandsDoNotOpenDatabase(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	t.Setenv("MGAR_TEST_DATABASE_URL", "")
	var out, errs bytes.Buffer
	if run(context.Background(), []string{"health"}, &out, &errs, nil) != 0 {
		t.Fatal(errs.String())
	}
	for _, args := range [][]string{{"submit", "--provider", "real"}, {"submit", "--fixture", "production"}, {"cancel"}, {"unknown"}} {
		errs.Reset()
		if code := run(context.Background(), args, &out, &errs, nil); code != 2 {
			t.Fatalf("invalid args=%v exit=%d error=%s", args, code, &errs)
		}
	}
}
