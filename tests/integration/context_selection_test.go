package integration

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"strings"
	"testing"
)

func newSelectionReplica(root string, cas *artifact.CAS, refs []inputcontext.Ref) (*artifact.CAS, error) {
	replica, e := artifact.New(root)
	if e != nil {
		return nil, e
	}
	for _, ref := range refs {
		body, e := cas.Read(ref.Hash, ref.ByteLength)
		if e != nil {
			replica.Close()
			return nil, e
		}
		if _, e = replica.Put(body); e != nil {
			replica.Close()
			return nil, e
		}
	}
	return replica, nil
}
func TestContextSummaryCASLineageAndCrossProcessIdempotence(t *testing.T) {
	s, cas, c, a := contextFixture(t)
	ctx := context.Background()
	snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	chosen, e := s.PrepareContext(ctx, c, cas, snap.ID, "duplicate paid orders", "summary", 16384, 4096)
	if e != nil || chosen.Blocked || len(chosen.Summaries) != 1 {
		t.Fatal(chosen, e)
	}
	summary := chosen.Summaries[0]
	var hash, id string
	var size int64
	if e = s.Pool.QueryRow(ctx, "SELECT a.content_sha256,a.byte_length,x.snapshot_id FROM artifacts a JOIN artifact_contexts x USING(artifact_id) WHERE a.artifact_id=$1 AND a.kind='summary' AND a.producer_task=$2", summary.ID, c.TaskID).Scan(&hash, &size, &id); e != nil || id != snap.ID {
		t.Fatal(e)
	}
	body, e := cas.Read(hash, size)
	if e != nil {
		t.Fatal(e)
	}
	var actual map[string]any
	if e = json.Unmarshal(body, &actual); e != nil {
		t.Fatal(e)
	}
	if actual["lossy"] != true || actual["verified"] != false || actual["source_artifact_id"] != a.ID || actual["source_content_sha256"] != a.Hash {
		t.Fatal(actual)
	}
	claim, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MGAR_TEST_CLAIM", string(claim))
	root := t.TempDir() + "/replica"
	replica, e := newSelectionReplica(root, cas, snap.Artifacts)
	if e != nil {
		t.Fatal(e)
	}
	defer replica.Close()
	t.Setenv("MGAR_TEST_CAS_ROOT", root)
	for _, out := range processBatch(t, s, "selection", c.RunID, 2) {
		if !strings.Contains(out, summary.ID) {
			t.Fatal(out)
		}
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE producer_task=$1 AND kind='summary'", c.TaskID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE entity_id=$1 AND kind='derived_summary_created'", summary.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	var state string
	var revision int64
	if e = s.Pool.QueryRow(ctx, "SELECT state,revision FROM task_instances WHERE task_id=$1", c.TaskID).Scan(&state, &revision); e != nil || state != "claimed" || revision != c.Revision {
		t.Fatal(state, revision, e)
	}
}
