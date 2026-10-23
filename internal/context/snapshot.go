// Package context defines immutable, authorized execution inputs.
package context

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

type Ref struct {
	ID                string `json:"artifact_id"`
	Hash              string `json:"content_sha256"`
	Purpose           string `json:"purpose"`
	ProducerNode      string `json:"producer_node"`
	ProducerTaskID    string `json:"producer_task_id"`
	SourcePlanVersion int    `json:"source_plan_version"`
	ToolVersion       string `json:"tool_schema_version"`
	ByteLength        int64  `json:"byte_length"`
	Kind              string `json:"kind"`
	ContentType       string `json:"content_type"`
}
type Snapshot struct {
	SchemaVersion string             `json:"schema_version"`
	ID            string             `json:"snapshot_id"`
	RunID         string             `json:"run_id"`
	TaskID        string             `json:"task_id"`
	Role          string             `json:"role"`
	PlanVersion   int                `json:"plan_version"`
	ModelVersion  string             `json:"model_config_version"`
	ToolVersion   string             `json:"tool_policy_version"`
	Artifacts     []Ref              `json:"artifacts"`
	Selection     *SelectionEnvelope `json:"context_selection,omitempty"`
}

// Canonical produces sorted JSON keys. Snapshot strings are ASCII identifiers;
// this matches the existing Python SDK canonical encoding for this protocol.
func Canonical(v any) []byte {
	raw, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e = d.Decode(&value); e != nil {
		panic(e)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if e = encoder.Encode(value); e != nil {
		panic(e)
	}
	// Match Python ensure_ascii=False while preserving literal backslash-u text.
	encoded := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	canonical := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if bytes.HasPrefix(encoded[i:], []byte(`\u2028`)) {
				canonical = append(canonical, []byte("\u2028")...)
				i += 6
				continue
			}
			if bytes.HasPrefix(encoded[i:], []byte(`\u2029`)) {
				canonical = append(canonical, []byte("\u2029")...)
				i += 6
				continue
			}
			canonical = append(canonical, encoded[i], encoded[i+1])
			i += 2
			continue
		}
		canonical = append(canonical, encoded[i])
		i++
	}
	return canonical
}
func (s Snapshot) Bytes() []byte { return Canonical(s) }
func (s Snapshot) Hash() string  { return artifact.Digest(s.Bytes()) }
