package planner

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Edge = dag.Edge
type Node struct {
	Key            string   `json:"node_key"`
	Role           string   `json:"role"`
	Goal           string   `json:"goal"`
	RequiredInputs []string `json:"required_inputs"`
	OutputKind     string   `json:"required_output_kind"`
}
type Proposal struct {
	SchemaVersion string `json:"schema_version"`
	BaseVersion   int    `json:"base_version"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
	Rationale     string `json:"rationale"`
}

var nodeKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func validText(v string, max int) bool {
	return strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= max
}
func Validate(p Proposal) error {
	if p.SchemaVersion != "plan.v1" || p.BaseVersion < 0 || !validText(p.Rationale, 4096) || p.Edges == nil {
		return errors.New("invalid plan envelope")
	}
	roles := map[string]bool{"research": true, "data": true, "action": true, "verify": true, "aggregate": true}
	kinds := map[string]bool{"finding": true, "sql_evidence": true, "patch": true, "verdict": true, "report": true}
	keys := []string{}
	for _, n := range p.Nodes {
		if !nodeKey.MatchString(n.Key) || !roles[n.Role] || !kinds[n.OutputKind] || !validText(n.Goal, 2048) || n.RequiredInputs == nil || len(n.RequiredInputs) > 16 {
			return errors.New("invalid or unauthorized node")
		}
		seen := map[string]bool{}
		for _, input := range n.RequiredInputs {
			if seen[input] {
				return errors.New("duplicate required input")
			}
			seen[input] = true
		}
		keys = append(keys, n.Key)
	}
	_, err := dag.Order(keys, p.Edges)
	return err
}
func uniqueValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate object key")
			}
			seen[s] = true
			if err = uniqueValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = uniqueValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = d.Token()
	return err
}
func Decode(data []byte) (Proposal, error) {
	var p Proposal
	if len(data) > 1<<20 {
		return p, errors.New("plan input too large")
	}
	unique := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(unique); err != nil {
		return p, err
	}
	var extra any
	if err := unique.Decode(&extra); err != io.EOF {
		return p, errors.New("trailing JSON")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return p, err
	}
	for _, key := range []string{"schema_version", "base_version", "nodes", "edges", "rationale"} {
		if v, ok := raw[key]; !ok || bytes.Equal(v, []byte("null")) {
			return p, errors.New("missing required field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	return p, Validate(p)
}
