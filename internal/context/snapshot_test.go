package context

import (
	"bytes"
	"testing"
)

func TestContextCanonicalReplayAndConfigIdentity(t *testing.T) {
	a := Snapshot{SchemaVersion: "context.v1", ID: "fixture", TaskID: "task", RunID: "run", Role: "research", PlanVersion: 1, ModelVersion: "mock.v1", ToolVersion: "research.v1", Artifacts: []Ref{}}
	b := a
	if !bytes.Equal(a.Bytes(), b.Bytes()) || a.Hash() != b.Hash() {
		t.Fatal("canonical replay differs")
	}
	b.ToolVersion = "research.v2"
	if a.Hash() == b.Hash() {
		t.Fatal("config not in hash")
	}
	for _, v := range []string{"", "a b", "\n", "模型", "<instruction>"} {
		if ValidVersion(v) {
			t.Fatal("invalid configuration version", v)
		}
	}
}
