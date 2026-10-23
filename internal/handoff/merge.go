package handoff

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
)

type Claim struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}
type Source struct {
	Ref      inputcontext.Ref
	Verified bool
	Claims   []Claim
}
type Citation struct {
	inputcontext.Ref
	Verified bool `json:"verified"`
}
type Fact struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	Verified bool            `json:"verified"`
	Sources  []Citation      `json:"sources"`
}
type Conflict struct {
	Key          string `json:"key"`
	Alternatives []Fact `json:"alternatives"`
}
type Missing struct {
	Node     string `json:"node"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}
type MergeResult struct {
	SchemaVersion string     `json:"schema_version"`
	Facts         []Fact     `json:"facts"`
	Conflicts     []Conflict `json:"conflicts"`
	Missing       []Missing  `json:"missing"`
	CanFinalize   bool       `json:"can_finalize"`
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("facts JSON depth")
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter == '{' {
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	} else if delimiter == '[' {
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	} else {
		return errors.New("invalid delimiter")
	}
	_, e = d.Token()
	return e
}
func normalizeClaim(c Claim) (Claim, error) {
	if strings.TrimSpace(c.Key) == "" || len(c.Key) > 128 || !utf8.ValidString(c.Key) || len(c.Value) > 4096 {
		return c, errors.New("invalid fact")
	}
	var value any
	if e := json.Unmarshal(c.Value, &value); e != nil {
		return c, e
	}
	switch v := value.(type) {
	case string:
		if len(v) > 2048 {
			return c, errors.New("fact string limit")
		}
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return c, errors.New("nonfinite fact")
		}
	case bool:
	default:
		return c, errors.New("fact must be a non-null scalar")
	}
	// Keep exact JSON numbers (including large integers), compact scalar whitespace.
	var b bytes.Buffer
	if e := json.Compact(&b, c.Value); e != nil {
		return c, e
	}
	c.Value = bytes.Clone(b.Bytes())
	return c, nil
}
func DecodeClaims(body []byte) ([]Claim, error) {
	if len(body) > 256*1024 || !utf8.Valid(body) {
		return nil, errors.New("facts size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	if e := uniqueJSON(d, 0); e != nil {
		return nil, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing facts JSON")
	}
	var raw map[string]json.RawMessage
	if e := json.Unmarshal(body, &raw); e != nil || len(raw) != 2 || raw["schema_version"] == nil || raw["facts"] == nil {
		return nil, errors.New("facts envelope")
	}
	var envelope struct {
		Schema string            `json:"schema_version"`
		Facts  []json.RawMessage `json:"facts"`
	}
	if e := json.Unmarshal(body, &envelope); e != nil || envelope.Schema != "facts.v1" || envelope.Facts == nil || len(envelope.Facts) > 64 {
		return nil, errors.New("facts schema or bound")
	}
	claims := []Claim{}
	seen := map[string]bool{}
	for _, data := range envelope.Facts {
		var fields map[string]json.RawMessage
		if e := json.Unmarshal(data, &fields); e != nil || len(fields) != 2 || fields["key"] == nil || fields["value"] == nil {
			return nil, errors.New("fact fields")
		}
		var c Claim
		if e := json.Unmarshal(data, &c); e != nil {
			return nil, e
		}
		c, e := normalizeClaim(c)
		if e != nil {
			return nil, e
		}
		if seen[c.Key] {
			return nil, errors.New("duplicate fact key")
		}
		seen[c.Key] = true
		claims = append(claims, c)
	}
	return claims, nil
}

// Verified is supplied by the trusted caller, never parsed from Artifact text.
func Merge(sources []Source, missing []Missing) (MergeResult, error) {
	r := MergeResult{SchemaVersion: "merge_result.v1", Facts: []Fact{}, Conflicts: []Conflict{}, Missing: append([]Missing{}, missing...), CanFinalize: true}
	if len(sources) > 24 || len(missing) > 24 {
		return r, errors.New("merge bound")
	}
	seen := map[string]bool{}
	groups := map[string]map[string]*Fact{}
	for _, source := range sources {
		ref := source.Ref
		if ref.ID == "" || seen[ref.ID] || len(ref.Hash) != 64 || ref.ProducerNode == "" || ref.ProducerTaskID == "" || ref.SourcePlanVersion < 1 || len(source.Claims) > 64 {
			return r, errors.New("invalid source")
		}
		seen[ref.ID] = true
		claimKeys := map[string]bool{}
		for _, c := range source.Claims {
			c, e := normalizeClaim(c)
			if e != nil {
				return r, e
			}
			if claimKeys[c.Key] {
				return r, errors.New("duplicate source fact")
			}
			claimKeys[c.Key] = true
			if groups[c.Key] == nil {
				groups[c.Key] = map[string]*Fact{}
			}
			value := string(c.Value)
			fact := groups[c.Key][value]
			if fact == nil {
				fact = &Fact{Key: c.Key, Value: c.Value, Sources: []Citation{}}
				groups[c.Key][value] = fact
			}
			fact.Verified = fact.Verified || source.Verified
			fact.Sources = append(fact.Sources, Citation{Ref: ref, Verified: source.Verified})
		}
	}
	if len(groups) > 256 {
		return r, errors.New("merged fact bound")
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		alternatives := []Fact{}
		for _, f := range groups[key] {
			sort.Slice(f.Sources, func(i, j int) bool {
				if f.Sources[i].Verified != f.Sources[j].Verified {
					return f.Sources[i].Verified
				}
				return f.Sources[i].ID < f.Sources[j].ID
			})
			alternatives = append(alternatives, *f)
		}
		sort.Slice(alternatives, func(i, j int) bool {
			if alternatives[i].Verified != alternatives[j].Verified {
				return alternatives[i].Verified
			}
			return string(alternatives[i].Value) < string(alternatives[j].Value)
		})
		if len(alternatives) > 1 {
			r.Conflicts = append(r.Conflicts, Conflict{Key: key, Alternatives: alternatives})
			r.CanFinalize = false
		} else {
			r.Facts = append(r.Facts, alternatives[0])
			r.CanFinalize = r.CanFinalize && alternatives[0].Verified
		}
	}
	missingSeen := map[string]bool{}
	for _, m := range r.Missing {
		if m.Node == "" || len(m.Node) > 64 || m.Reason == "" || len(m.Reason) > 128 || missingSeen[m.Node] {
			return r, errors.New("invalid missing source")
		}
		missingSeen[m.Node] = true
		if m.Required {
			r.CanFinalize = false
		}
	}
	sort.Slice(r.Missing, func(i, j int) bool { return r.Missing[i].Node < r.Missing[j].Node })
	if len(r.Facts) == 0 {
		r.CanFinalize = false
	}
	return r, nil
}
