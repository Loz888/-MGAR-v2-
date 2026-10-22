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
	if p.SchemaVersion != "plan.v1" || p.BaseVersion < 0 || !validText(p.Rationale, 4096) || p.Edges == nil || len(p.Edges) > 96 {
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

// exactObject checks wire names before encoding/json can case-fold struct fields.
// Every property in the current plan/node/edge schemas is required.
func exactObject(data json.RawMessage, fields ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil || len(object) != len(fields) {
		return nil, errors.New("missing or unknown object property")
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("missing or null required property")
		}
	}
	return object, nil
}

func jsonArray(data json.RawMessage, maximum int) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	if values == nil || len(values) > maximum {
		return nil, errors.New("invalid array or array size")
	}
	return values, nil
}

func jsonString(data json.RawMessage) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '"' {
		return errors.New("expected JSON string")
	}
	var value string
	return json.Unmarshal(data, &value)
}

// validateJSONShape preserves distinctions lost by struct decoding: absent vs
// null, exact field spelling, and null vs empty-string array elements.
func validateJSONShape(data []byte) error {
	object, err := exactObject(data, "schema_version", "base_version", "nodes", "edges", "rationale")
	if err != nil {
		return err
	}
	for _, field := range []string{"schema_version", "rationale"} {
		if err = jsonString(object[field]); err != nil {
			return err
		}
	}
	var baseVersion int
	if err = json.Unmarshal(object["base_version"], &baseVersion); err != nil {
		return err
	}
	nodes, err := jsonArray(object["nodes"], 24)
	if err != nil {
		return err
	}
	for _, raw := range nodes {
		node, err := exactObject(raw, "node_key", "role", "goal", "required_inputs", "required_output_kind")
		if err != nil {
			return err
		}
		for _, field := range []string{"node_key", "role", "goal", "required_output_kind"} {
			if err = jsonString(node[field]); err != nil {
				return err
			}
		}
		inputs, err := jsonArray(node["required_inputs"], 16)
		if err != nil {
			return err
		}
		for _, input := range inputs {
			if err = jsonString(input); err != nil {
				return err
			}
		}
	}
	edges, err := jsonArray(object["edges"], 96)
	if err != nil {
		return err
	}
	for _, raw := range edges {
		edge, err := exactObject(raw, "from", "to")
		if err != nil {
			return err
		}
		for _, field := range []string{"from", "to"} {
			if err = jsonString(edge[field]); err != nil {
				return err
			}
		}
	}
	return nil
}

func Decode(data []byte) (Proposal, error) {
	var p Proposal
	if len(data) > 1<<20 {
		return p, errors.New("plan input too large")
	}
	if !utf8.Valid(data) {
		return p, errors.New("plan must be valid UTF-8 JSON")
	}
	unique := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(unique); err != nil {
		return p, err
	}
	var extra any
	if err := unique.Decode(&extra); err != io.EOF {
		return p, errors.New("trailing JSON")
	}
	if err := validateJSONShape(data); err != nil {
		return p, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	return p, Validate(p)
}
