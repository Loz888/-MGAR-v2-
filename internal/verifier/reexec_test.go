package verifier

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReexecOracleNormalizesNumbersAndMultiplicity(t *testing.T) {
	a := json.RawMessage(`[[1,2.00],[2,3],[1,2]]`)
	b := json.RawMessage(`[[1.0,2],[1,2e0],[2,3.000]]`)
	x, e := NormalizeRows(a)
	if e != nil {
		t.Fatal(e)
	}
	y, e := NormalizeRows(b)
	if e != nil || x != y {
		t.Fatal(x, y, e)
	}
	z, e := NormalizeRows(json.RawMessage(`[[1,2],[2,3]]`))
	if e != nil || z == x {
		t.Fatal("duplicate row was discarded", e)
	}
	if _, e = NormalizeRows(json.RawMessage(`[[1e999999999]]`)); e == nil {
		t.Fatal("unbounded exponent accepted")
	}
}

func TestReexecOracleRejectsIncompleteTestTranscripts(t *testing.T) {
	job := TestInput{Package: "fixture", Tests: []string{"TestRequired"}}
	for _, raw := range []string{
		`{"Action":"start","Package":"fixture"}`,
		"{\"Action\":\"start\",\"Package\":\"fixture\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}",
		"{\"Action\":\"start\",\"Package\":\"fixture\"}\n{\"Action\":\"run\",\"Package\":\"fixture\",\"Test\":\"TestRequired\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestRequired\"}",
	} {
		if _, e := testObservation(job, TestExecution{Output: []byte(raw), ExitCode: 0}); e == nil {
			t.Fatal("exit zero without independent test completion accepted")
		}
	}
}
func TestReexecOracleRejectsSourceLinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "source.go")
	if e := os.WriteFile(outside, []byte("outside source"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "linked.go")
	if e := os.Symlink(outside, link); e != nil {
		t.Fatal(e)
	}
	if _, e := SourceDigest(root); e == nil {
		t.Fatal("symlink accepted")
	}
	if e := os.Remove(link); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(outside, link); e != nil {
		t.Fatal(e)
	}
	if _, e := SourceDigest(root); e == nil {
		t.Fatal("host hardlink accepted")
	}
}
func TestReexecOracleNoLiveFallback(t *testing.T) {
	r := Reexecutor{}
	if _, e := r.Replay(context.Background(), Recipe{Method: "readonly_sql", Dataset: "missing", Command: "query", InputDigest: "unfixed"}); e == nil {
		t.Fatal("unfixed inputs accepted")
	}
}
