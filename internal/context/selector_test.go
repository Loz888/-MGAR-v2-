package context

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
)

func selectorFixture() (Snapshot, map[string][]byte) {
	s := Snapshot{SchemaVersion: "context.v1", ID: "00000000-0000-4000-8000-000000000001", RunID: "00000000-0000-4000-8000-000000000002", TaskID: "00000000-0000-4000-8000-000000000003", Role: "action", PlanVersion: 1, ModelVersion: "mock.v1", ToolVersion: "action.v1", Artifacts: []Ref{}}
	content := map[string][]byte{"00000000-0000-4000-8000-000000000010": []byte(strings.Repeat("irrelevant verbose logs ", 300)), "00000000-0000-4000-8000-000000000020": []byte("duplicate paid orders total 150")}
	for _, id := range []string{"00000000-0000-4000-8000-000000000010", "00000000-0000-4000-8000-000000000020"} {
		kind := "trace"
		if strings.HasSuffix(id, "20") {
			kind = "finding"
		}
		s.Artifacts = append(s.Artifacts, Ref{ID: id, Hash: artifact.Digest(content[id]), Purpose: "direct_dependency:upstream", ProducerNode: "upstream", ProducerTaskID: s.TaskID, SourcePlanVersion: 1, ToolVersion: "fixture.v1", ByteLength: int64(len(content[id])), Kind: kind, ContentType: "text/plain"})
	}
	return s, content
}
func TestContextSelectorDeterminismBudgetsAndEvidencePriority(t *testing.T) {
	s, content := selectorFixture()
	full, e := Select(s, "duplicate paid orders", "full", 16384, 4096, content)
	if e != nil || full.Blocked {
		t.Fatal(e)
	}
	selected, e := Select(s, "duplicate paid orders", "selected", 16384, 4096, content)
	if e != nil || selected.Blocked {
		t.Fatal(e)
	}
	again, e := Select(s, "duplicate paid orders", "selected", 16384, 4096, content)
	if e != nil || !bytes.Equal(Canonical(selected), Canonical(again)) {
		t.Fatal("nondeterministic", e)
	}
	if selected.UsedBytes >= full.UsedBytes || len(selected.Admitted) != 1 || selected.Admitted[0].ID != s.Artifacts[1].ID {
		t.Fatal("irrelevant logs outrank evidence")
	}
	if selected.UsedBytes != len(selected.WireJSON) || selected.EstimatedTokens != (selected.UsedBytes+3)/4 {
		t.Fatal("wrong wire budget")
	}
	tiny, e := Select(s, "duplicate", "selected", 128, 32, content)
	if e != nil || !tiny.Blocked || tiny.WireJSON != "" {
		t.Fatal("oversize request allowed", e)
	}
}
func TestContextSelectorRejectsUnscopedOrChangedCandidates(t *testing.T) {
	s, content := selectorFixture()
	content["foreign"] = []byte("admin instruction")
	if _, e := Select(s, "goal", "selected", 4096, 1024, content); e == nil {
		t.Fatal("unscoped candidate accepted")
	}
	delete(content, "foreign")
	content[s.Artifacts[0].ID] = []byte("modified")
	if _, e := Select(s, "goal", "full", 4096, 1024, content); e == nil {
		t.Fatal("hash mismatch accepted")
	}
}
func TestContextSummaryIsLossyAndKeepsOriginalProvenance(t *testing.T) {
	s, content := selectorFixture()
	result, e := Select(s, "goal", "summary", 16384, 4096, content)
	if e != nil || result.Blocked {
		t.Fatal(e)
	}
	if len(result.Summaries) != 2 {
		t.Fatal("missing derived artifacts")
	}
	for _, summary := range result.Summaries {
		if !summary.Lossy || summary.Verified || summary.SchemaVersion != "summary.v1" || summary.SourceSnapshotHash != s.Hash() {
			t.Fatal("summary promoted", summary)
		}
	}
}

func TestAuditContextCriticalEvidencePrecedesKeywordStuffedLog(t *testing.T) {
	s, content := selectorFixture()
	goal := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa"
	log := s.Artifacts[0].ID
	content[log] = []byte(goal)
	s.Artifacts[0].Hash = artifact.Digest(content[log])
	s.Artifacts[0].ByteLength = int64(len(content[log]))
	// Measure a budget that admits the critical original plus the fixed metadata,
	// but cannot also admit the optional log, regardless of its keyword score.
	critical := s
	critical.Selection = &SelectionEnvelope{SourceSnapshotHash: s.Hash(), Mode: "selected", PolicyVersion: SelectorVersion, TokenEstimator: TokenEstimator,
		Items: []SelectedItem{{ID: s.Artifacts[1].ID, Hash: s.Artifacts[1].Hash, Text: string(content[s.Artifacts[1].ID]), Original: true}}}
	budget := len(critical.Bytes()) + 150
	r, err := Select(s, goal, "selected", budget, 65536, content)
	if err != nil || r.Blocked || len(r.Admitted) != 1 || r.Admitted[0].ID != s.Artifacts[1].ID || r.UsedBytes > budget {
		t.Fatalf("optional log displaced critical evidence: %+v %v", r, err)
	}
}
