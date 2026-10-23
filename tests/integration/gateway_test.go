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
