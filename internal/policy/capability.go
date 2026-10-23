// Package policy is the trusted authorization boundary. Model text never issues
// credentials. Definitions and handlers are installed by the controller only.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

var ErrDenied = errors.New("policy_denied")
var name = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Call struct {
	Schema    string          `json:"schema"`
	ID        string          `json:"request_id"`
	Tool      string          `json:"tool"`
	Version   string          `json:"version"`
	Resource  string          `json:"resource"`
	Arguments json.RawMessage `json:"arguments"`
}

// All parameters are required and bounded; string parameters must be enums.
// No free-form SQL, shell, path or prompt enters a handler through this API.
type Parameter struct {
	Kind     string
	Min, Max int64
	Enum     []string
}
type Definition struct {
	Name, Version, Role, Effect string
	Resources                   []string
	Parameters                  map[string]Parameter
	Timeout                     time.Duration
	MaxOutput                   int
	Execute                     func(context.Context, string, map[string]any) ([]byte, error)
}

func (d Definition) arguments(raw []byte) (map[string]any, error) {
	var args map[string]json.RawMessage
	if verifier.StrictJSON(raw, &args) != nil || args == nil || len(args) != len(d.Parameters) {
		return nil, ErrDenied
	}
	out := map[string]any{}
	for key, p := range d.Parameters {
		b, ok := args[key]
		if !ok || string(b) == "null" {
			return nil, ErrDenied
		}
		switch p.Kind {
		case "integer":
			var v int64
			if json.Unmarshal(b, &v) != nil || v < p.Min || v > p.Max {
				return nil, ErrDenied
			}
			out[key] = v
		case "string":
			var v string
			if json.Unmarshal(b, &v) != nil || !slices.Contains(p.Enum, v) {
				return nil, ErrDenied
			}
			out[key] = v
		case "boolean":
			var v bool
			if string(b) == "null" || json.Unmarshal(b, &v) != nil {
				return nil, ErrDenied
			}
			out[key] = v
		default:
			return nil, ErrDenied
		}
	}
	return out, nil
}
func freezeDefinition(d Definition) (Definition, error) {
	if !name.MatchString(d.Name) || !name.MatchString(d.Version) || !slices.Contains([]string{"research", "data", "action", "verify", "aggregate"}, d.Role) || !slices.Contains([]string{"readonly", "idempotent_write", "non_idempotent"}, d.Effect) || len(d.Resources) == 0 || len(d.Resources) > 64 || d.Execute == nil || d.Timeout <= 0 || d.Timeout > 30*time.Second || d.MaxOutput < 1 || d.MaxOutput > 65536 || len(d.Parameters) > 16 {
		return d, ErrDenied
	}
	d.Resources = slices.Clone(d.Resources)
	for _, r := range d.Resources {
		if !name.MatchString(r) {
			return d, ErrDenied
		}
	}
	params := map[string]Parameter{}
	for k, v := range d.Parameters {
		if !name.MatchString(k) || !slices.Contains([]string{"integer", "string", "boolean"}, v.Kind) || v.Min > v.Max || (v.Kind == "string" && (len(v.Enum) == 0 || len(v.Enum) > 64)) {
			return d, ErrDenied
		}
		for _, s := range v.Enum {
			if len(s) > 256 {
				return d, ErrDenied
			}
		}
		v.Enum = slices.Clone(v.Enum)
		params[k] = v
	}
	d.Parameters = params
	return d, nil
}
