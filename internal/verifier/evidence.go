// Package verifier checks evidence against bytes and independently supplied
// requirements. Neither Agent prose nor a Reviewer can authorize a verdict.
package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"unicode/utf8"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
)

const Version = "verifier.v1"
const MaxEvidenceBytes = 65536

var ErrStructure = errors.New("INVALID_EVIDENCE")
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Recipe struct {
	Method        string `json:"method"`
	SourceID      string `json:"source_artifact_id,omitempty"`
	SourceHash    string `json:"source_sha256,omitempty"`
	SourceVersion int    `json:"source_plan_version,omitempty"`
	Start         *int   `json:"start,omitempty"`
	End           *int   `json:"end,omitempty"`
	InputDigest   string `json:"input_digest,omitempty"`
	Dataset       string `json:"dataset_id,omitempty"`
	Command       string `json:"command_id,omitempty"`
}
type Observation struct {
	Hash     string          `json:"sha256,omitempty"`
	Quote    string          `json:"quote,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Tests    *int            `json:"tests,omitempty"`
	RowCount *int            `json:"row_count,omitempty"`
	Rows     json.RawMessage `json:"rows,omitempty"`
}
type Evidence struct {
	Schema          string      `json:"schema_version"`
	Claim           string      `json:"claim"`
	Kind            string      `json:"kind"`
	ArtifactID      string      `json:"artifact_id"`
	Hash            string      `json:"content_sha256"`
	RunID           string      `json:"producer_run_id"`
	TaskID          string      `json:"producer_task_id"`
	PlanVersion     int         `json:"source_plan_version"`
	SnapshotID      string      `json:"input_snapshot_id"`
	ToolVersion     string      `json:"tool_version"`
	ExecutionStatus string      `json:"execution_status"`
	Recipe          Recipe      `json:"replay_recipe"`
	Observed        Observation `json:"observed"`
	Expected        Observation `json:"expected_observation"`
}
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type Verdict struct {
	Schema     string  `json:"schema_version"`
	ArtifactID string  `json:"artifact_id"`
	Status     string  `json:"status"`
	Checks     []Check `json:"checks"`
	Version    string  `json:"verifier_version"`
}
type Source struct {
	Metadata artifact.Metadata
	Bytes    []byte
}
type Proof struct {
	Metadata artifact.Metadata
	Snapshot inputcontext.Snapshot
	Bytes    []byte
	Sources  map[string]Source
}

// Requirement is supplied by the trusted controller, never taken from Evidence.
type Requirement struct {
	Kind, ToolVersion string
	Recipe            Recipe
	Expected          Observation
}
type Replayer interface {
	Replay(context.Context, Recipe) (Observation, error)
}
type Engine struct{ Replayer Replayer }

// StrictJSON rejects duplicate keys, trailing values and unknown fields. The
// recursive token walk bounds depth before decoding the typed wire value.
func StrictJSON(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > MaxEvidenceBytes || !utf8.Valid(raw) {
		return ErrStructure
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return ErrStructure
		}
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrStructure
				}
				seen[name] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return ErrStructure
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return ErrStructure
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrStructure
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return ErrStructure
	}
	return nil
}
func Decode(raw []byte) (Evidence, error) {
	var e Evidence
	if StrictJSON(raw, &e) != nil || e.Schema != "evidence.v1" || len(e.Claim) == 0 || len(e.Claim) > 4096 || !uuid.MatchString(e.ArtifactID) || !uuid.MatchString(e.RunID) || !uuid.MatchString(e.TaskID) || !uuid.MatchString(e.SnapshotID) || !hash.MatchString(e.Hash) || e.PlanVersion < 1 || !inputcontext.ValidVersion(e.ToolVersion) || (e.ExecutionStatus != "succeeded" && e.ExecutionStatus != "failed" && e.ExecutionStatus != "unknown") {
		return e, ErrStructure
	}
	if e.Kind != "file_hash" && e.Kind != "quote" && e.Kind != "log_excerpt" && e.Kind != "test_run" && e.Kind != "sql_result" && e.Kind != "metric" {
		return e, ErrStructure
	}
	// Presence is significant: an absent/null expectation is not an empty proof.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"replay_recipe", "observed", "expected_observation"} {
		if len(fields[key]) == 0 || bytes.Equal(fields[key], []byte("null")) {
			return e, ErrStructure
		}
	}
	if !validObservation(e.Observed) || !validObservation(e.Expected) {
		return e, ErrStructure
	}
	r := e.Recipe
	if r.Method != "cas_hash" && r.Method != "quote" && r.Method != "go_test" && r.Method != "readonly_sql" {
		return e, ErrStructure
	}
	if (r.SourceID != "" && !uuid.MatchString(r.SourceID)) || (r.SourceHash != "" && !hash.MatchString(r.SourceHash)) || r.SourceVersion < 0 || (r.InputDigest != "" && !hash.MatchString(r.InputDigest)) || (r.Command != "" && !inputcontext.ValidVersion(r.Command)) || (r.Dataset != "" && !inputcontext.ValidVersion(r.Dataset)) || (r.Start != nil && *r.Start < 0) || (r.End != nil && *r.End < 1) {
		return e, ErrStructure
	}
	return e, nil
}
func validObservation(o Observation) bool {
	if (o.Hash != "" && !hash.MatchString(o.Hash)) || len(o.Quote) > 4096 || (o.ExitCode != nil && (*o.ExitCode < -256 || *o.ExitCode > 255)) || (o.Tests != nil && (*o.Tests < 0 || *o.Tests > 10000)) || (o.RowCount != nil && (*o.RowCount < 0 || *o.RowCount > 10000)) {
		return false
	}
	if len(o.Rows) > 0 {
		d := json.NewDecoder(bytes.NewReader(o.Rows))
		d.UseNumber()
		var rows [][]any
		if d.Decode(&rows) != nil || rows == nil || len(rows) > 256 {
			return false
		}
		for _, row := range rows {
			if row == nil || len(row) > 32 {
				return false
			}
			for _, v := range row {
				switch x := v.(type) {
				case nil, bool, json.Number:
				case string:
					if len(x) > 4096 {
						return false
					}
				default:
					return false
				}
			}
		}
	}
	return true
}
func (v *Verdict) add(name, status, detail string) {
	v.Checks = append(v.Checks, Check{Name: name, Status: status, Detail: detail})
	if status == "failed" || (status == "unverifiable" && v.Status != "failed") {
		v.Status = status
	}
}
func (engine Engine) Check(ctx context.Context, raw []byte, p Proof, r Requirement) Verdict {
	v := Verdict{Schema: "verdict.v1", ArtifactID: p.Metadata.ID, Status: "passed", Version: Version, Checks: []Check{}}
	e, err := Decode(raw)
	if err != nil {
		v.add("structure", "failed", "typed evidence is incomplete or invalid")
		return v
	}
	v.add("structure", "passed", "strict evidence.v1")
	m := p.Metadata
	if e.ArtifactID != m.ID || e.RunID != m.RunID || e.TaskID != m.ProducerTaskID || e.PlanVersion != m.SourcePlanVersion || e.Hash != m.Hash || e.ToolVersion != m.ToolVersion || artifact.Digest(p.Bytes) != m.Hash || int64(len(p.Bytes)) != m.ByteLength {
		v.add("provenance", "failed", "identity, version, tool or bytes do not match the authorized artifact")
		return v
	}
	if e.SnapshotID != p.Snapshot.ID || p.Snapshot.RunID != m.RunID || p.Snapshot.TaskID != m.ProducerTaskID || p.Snapshot.PlanVersion != m.SourcePlanVersion {
		v.add("snapshot", "failed", "snapshot does not belong to this producer and plan")
		return v
	}
	v.add("provenance", "passed", "authorized metadata and immutable bytes match")
	if e.Kind != r.Kind || e.ToolVersion != r.ToolVersion || !reflect.DeepEqual(e.Recipe, r.Recipe) || !EqualObservation(e.Expected, r.Expected) {
		v.add("requirement", "failed", "claim differs from the trusted verification requirement")
		return v
	}
	if e.ExecutionStatus != "succeeded" {
		status := "failed"
		if e.ExecutionStatus == "unknown" {
			status = "unverifiable"
		}
		v.add("execution", status, "execution is not confirmed successful")
		return v
	}
	switch e.Kind {
	case "file_hash":
		actual := Observation{Hash: artifact.Digest(p.Bytes)}
		if e.Recipe.Method != "cas_hash" || !reflect.DeepEqual(e.Observed, actual) || !reflect.DeepEqual(e.Expected, actual) {
			v.add("file_hash", "failed", "pinned file hash differs")
		} else {
			v.add("file_hash", "passed", "independent byte digest matches")
		}
	case "quote", "log_excerpt":
		source, ok := p.Sources[e.Recipe.SourceID]
		authorized := false
		for _, ref := range p.Snapshot.Artifacts {
			if ref.ID == e.Recipe.SourceID && ref.Hash == e.Recipe.SourceHash && ref.SourcePlanVersion == e.Recipe.SourceVersion {
				authorized = true
			}
		}
		if !ok || !authorized || e.Recipe.Method != "quote" || source.Metadata.RunID != m.RunID || source.Metadata.Hash != e.Recipe.SourceHash || source.Metadata.SourcePlanVersion != e.Recipe.SourceVersion || artifact.Digest(source.Bytes) != source.Metadata.Hash || e.Recipe.Start == nil || e.Recipe.End == nil || *e.Recipe.Start < 0 || *e.Recipe.End <= *e.Recipe.Start || *e.Recipe.End > len(source.Bytes) {
			v.add("reference", "failed", "reference is missing, unauthorized, stale or outside byte bounds")
			return v
		}
		actual := Observation{Quote: string(source.Bytes[*e.Recipe.Start:*e.Recipe.End])}
		if !reflect.DeepEqual(e.Observed, actual) || !reflect.DeepEqual(e.Expected, actual) {
			v.add("quote", "failed", "quote differs from the actual authorized source")
		} else {
			v.add("quote", "passed", "exact source bytes match; prose outside the quotation is not proved")
		}
	case "test_run", "sql_result", "metric":
		// The evidence must describe the actual artifact, not a detached correct
		// observation placed beside a forged result artifact.
		var reported Observation
		if StrictJSON(p.Bytes, &reported) != nil || !validObservation(reported) || !EqualObservation(reported, e.Observed) {
			v.add("result_binding", "failed", "reported observation is not the artifact content")
			return v
		}
		if e.Kind == "test_run" && (e.Expected.ExitCode == nil || *e.Expected.ExitCode != 0 || e.Expected.Tests == nil || *e.Expected.Tests < 1) {
			v.add("test_requirement", "failed", "success requires an explicit zero exit and executed tests")
			return v
		}
		if e.Kind != "test_run" && (e.Expected.RowCount == nil || len(e.Expected.Rows) == 0) {
			v.add("sql_requirement", "failed", "SQL proof requires explicit rows and row count")
			return v
		}
		if !EqualObservation(e.Observed, e.Expected) {
			v.add("observation", "failed", "reported result disagrees with required observation")
			return v
		}
		if engine.Replayer == nil {
			v.add("independent_replay", "unverifiable", "no independent reexecutor is configured")
			return v
		}
		actual, err := engine.Replayer.Replay(ctx, e.Recipe)
		if err != nil {
			v.add("independent_replay", "unverifiable", "independent execution could not establish a result")
		} else if !EqualObservation(actual, e.Expected) {
			v.add("independent_replay", "failed", "actual result differs from the independent requirement")
		} else {
			v.add("independent_replay", "passed", "independently executed observation matches")
		}
	}
	return v
}
func EqualObservation(a, b Observation) bool {
	if len(a.Rows) > 0 || len(b.Rows) > 0 {
		left, e := NormalizeRows(a.Rows)
		if e != nil {
			return false
		}
		right, e := NormalizeRows(b.Rows)
		if e != nil || left != right {
			return false
		}
		a.Rows = nil
		b.Rows = nil
	}
	return reflect.DeepEqual(a, b)
}
