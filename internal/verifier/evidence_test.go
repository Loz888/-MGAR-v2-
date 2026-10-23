package verifier

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"strings"
	"testing"
)

func fixtureEvidence() (Evidence, Proof, Requirement) {
	body := []byte("independent bytes")
	m := artifact.Metadata{ID: "b76b4412-397b-4c51-9ba8-6b50f7d2f271", RunID: "b76b4412-397b-4c51-9ba8-6b50f7d2f272", ProducerTaskID: "b76b4412-397b-4c51-9ba8-6b50f7d2f273", Hash: artifact.Digest(body), SourcePlanVersion: 1, ToolVersion: "fixture.v1", ByteLength: int64(len(body))}
	snap := inputcontext.Snapshot{ID: "b76b4412-397b-4c51-9ba8-6b50f7d2f274", RunID: m.RunID, TaskID: m.ProducerTaskID, PlanVersion: 1, Artifacts: []inputcontext.Ref{}}
	expected := Observation{Hash: m.Hash}
	recipe := Recipe{Method: "cas_hash"}
	e := Evidence{Schema: "evidence.v1", Claim: "content hash matches pinned bytes", Kind: "file_hash", ArtifactID: m.ID, Hash: m.Hash, RunID: m.RunID, TaskID: m.ProducerTaskID, PlanVersion: 1, SnapshotID: snap.ID, ToolVersion: m.ToolVersion, ExecutionStatus: "succeeded", Recipe: recipe, Observed: expected, Expected: expected}
	return e, Proof{Metadata: m, Snapshot: snap, Bytes: body}, Requirement{Kind: e.Kind, ToolVersion: e.ToolVersion, Recipe: recipe, Expected: expected}
}

func TestVerifierEvidenceBoundaries(t *testing.T) {
	for _, kind := range []string{"missing_identity", "wrong_snapshot", "stale_plan", "tool_version", "reviewer_yes", "duplicate_key", "trailing", "unknown_usage", "fake_test_success", "no_independent_test", "missing_reference"} {
		t.Run(kind, func(t *testing.T) {
			e, p, r := fixtureEvidence()
			want := "failed"
			switch kind {
			case "missing_identity":
				e.RunID = ""
			case "wrong_snapshot":
				e.SnapshotID = e.TaskID
			case "stale_plan":
				e.PlanVersion++
			case "tool_version":
				e.ToolVersion = "untrusted.v2"
			case "unknown_usage":
				e.ExecutionStatus = "unknown"
				want = "unverifiable"
			case "fake_test_success", "no_independent_test":
				zero, five := 0, 5
				e.Kind = "test_run"
				e.Claim = "5/5 tests passed"
				e.Recipe = Recipe{Method: "go_test", Command: "fixture.v1", InputDigest: p.Metadata.Hash}
				e.Observed = Observation{ExitCode: &zero, Tests: &five}
				e.Expected = e.Observed
				r = Requirement{Kind: e.Kind, ToolVersion: e.ToolVersion, Recipe: e.Recipe, Expected: e.Expected}
				if kind == "fake_test_success" {
					one := 1
					e.Observed.ExitCode = &one
				} else {
					want = "unverifiable"
				}
				p.Bytes, _ = json.Marshal(e.Observed)
				p.Metadata.Hash = artifact.Digest(p.Bytes)
				p.Metadata.ByteLength = int64(len(p.Bytes))
				e.Hash = p.Metadata.Hash
			case "missing_reference":
				start, end := 0, 2
				e.Kind = "quote"
				e.Recipe = Recipe{Method: "quote", SourceID: e.TaskID, SourceHash: e.Hash, SourceVersion: 1, Start: &start, End: &end}
				e.Observed = Observation{Quote: "in"}
				e.Expected = e.Observed
				r = Requirement{Kind: e.Kind, ToolVersion: e.ToolVersion, Recipe: e.Recipe, Expected: e.Expected}
			}
			raw, _ := json.Marshal(e)
			switch kind {
			case "reviewer_yes":
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"reviewer_passed":true}`)
			case "duplicate_key":
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"claim":"yes"}`)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			}
			if v := (Engine{}).Check(context.Background(), raw, p, r); v.Status != want {
				t.Fatal(v)
			}
		})
	}
}
func TestVerifierEvidenceQuoteBytes(t *testing.T) {
	e, p, r := fixtureEvidence()
	source := p.Metadata
	source.ID = e.TaskID
	source.ProducerTaskID = e.SnapshotID
	p.Sources = map[string]Source{source.ID: {Metadata: source, Bytes: p.Bytes}}
	p.Snapshot.Artifacts = []inputcontext.Ref{{ID: source.ID, Hash: source.Hash, SourcePlanVersion: 1}}
	start, end := 0, 11
	e.Kind = "quote"
	e.Recipe = Recipe{Method: "quote", SourceID: source.ID, SourceHash: source.Hash, SourceVersion: 1, Start: &start, End: &end}
	e.Observed = Observation{Quote: "independent"}
	e.Expected = e.Observed
	r = Requirement{Kind: e.Kind, ToolVersion: e.ToolVersion, Recipe: e.Recipe, Expected: e.Expected}
	raw, _ := json.Marshal(e)
	if v := (Engine{}).Check(context.Background(), raw, p, r); v.Status != "passed" {
		t.Fatal(v)
	}
	p.Sources[source.ID] = Source{Metadata: source, Bytes: []byte("forged source")}
	if v := (Engine{}).Check(context.Background(), raw, p, r); v.Status != "failed" {
		t.Fatal(v)
	}
}
func TestVerifierEvidenceDoesNotTrustClaim(t *testing.T) {
	e, p, r := fixtureEvidence()
	raw, _ := json.Marshal(e)
	v := (Engine{}).Check(context.Background(), raw, p, r)
	if v.Status != "passed" {
		t.Fatal(v)
	}
	e.Hash = artifact.Digest([]byte("forged"))
	raw, _ = json.Marshal(e)
	if v = (Engine{}).Check(context.Background(), raw, p, r); v.Status != "failed" {
		t.Fatal(v)
	}
}
