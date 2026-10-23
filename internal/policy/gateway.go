package policy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

type Denial struct{ Digest, Reason string }
type grant struct {
	claim     store.TaskClaim
	resources []string
	expiry    time.Time
}
type Gateway struct {
	store       *store.Store
	cas         *artifact.CAS
	definitions map[string]Definition
	mu          sync.Mutex
	grants      map[[32]byte]grant
	unauth      func(Denial)
}

func NewGateway(s *store.Store, cas *artifact.CAS, defs []Definition, unauth func(Denial)) (*Gateway, error) {
	if s == nil || cas == nil || unauth == nil || len(defs) == 0 || len(defs) > 128 {
		return nil, ErrDenied
	}
	g := &Gateway{store: s, cas: cas, definitions: map[string]Definition{}, grants: map[[32]byte]grant{}, unauth: unauth}
	for _, d := range defs {
		f, e := freezeDefinition(d)
		if e != nil {
			return nil, e
		}
		if _, ok := g.definitions[d.Name]; ok {
			return nil, ErrDenied
		}
		if _, e = Risk(f); e != nil {
			return nil, e
		}
		g.definitions[d.Name] = f
	}
	return g, nil
}

// Issue is a controller-only API. There is deliberately no network minting
// route. Restart revokes all credentials; the durable execution ledger survives.
func (g *Gateway) Issue(ctx context.Context, c store.TaskClaim, resources []string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 5*time.Minute || len(resources) == 0 || len(resources) > 64 {
		return "", ErrDenied
	}
	if e := g.store.ValidateToolClaim(ctx, c); e != nil {
		return "", e
	}
	for _, r := range resources {
		allowed := false
		for _, d := range g.definitions {
			if d.Role == c.Role && slices.Contains(d.Resources, r) {
				allowed = true
			}
		}
		if !allowed {
			return "", ErrDenied
		}
	}
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	token := hex.EncodeToString(b[:])
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.grants {
		if !v.expiry.After(now) {
			delete(g.grants, k)
		}
	}
	if len(g.grants) >= 4096 {
		return "", ErrDenied
	}
	g.grants[sha256.Sum256([]byte(token))] = grant{c, slices.Clone(resources), now.Add(ttl)}
	return token, nil
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
	digest := artifact.Digest(raw)
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	g.mu.Lock()
	p, ok := g.grants[sha256.Sum256([]byte(token))]
	g.mu.Unlock()
	deny := func(reason string) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		defer cancel()
		if ok {
			if g.store.RecordToolDenial(ctx, p.claim, digest, reason) != nil {
				http.Error(w, `{"error":"audit_unavailable"}`, 503)
				return
			}
		} else {
			g.unauth(Denial{digest, reason})
		}
		http.Error(w, `{"error":"policy_denied","retryable":false}`, 403)
	}
	if !ok || len(token) != 64 || r.Header.Get("Authorization") != "Bearer "+token || !p.expiry.After(time.Now()) {
		deny("credential")
		return
	}
	var call Call
	if e != nil || len(raw) > 16384 || r.Method != http.MethodPost || r.URL.Path != "/v1/tools" || r.URL.RawQuery != "" || verifier.StrictJSON(raw, &call) != nil || call.Schema != "tool-call.v1" || !uuid.MatchString(call.ID) {
		deny("schema")
		return
	}
	d, found := g.definitions[call.Tool]
	if !found || d.Role != p.claim.Role || d.Version != call.Version || !slices.Contains(d.Resources, call.Resource) || !slices.Contains(p.resources, call.Resource) {
		deny("capability")
		return
	}
	args, e := d.arguments(call.Arguments)
	if e != nil {
		deny("parameters")
		return
	}
	// Canonicalize the request hash independently of JSON whitespace/key order.
	canonical, _ := json.Marshal(struct {
		Tool, Version, Resource string
		Arguments               map[string]any
	}{call.Tool, call.Version, call.Resource, args})
	req := store.ToolRequest{ID: call.ID, Capability: d.Name, Version: d.Version, Resource: call.Resource, Hash: artifact.Digest(canonical), Effect: d.Effect}
	cached, handled, e := g.authorizeAction(w, r, p, req)
	if handled {
		return
	}
	if e != nil {
		deny("reservation")
		return
	}
	if !cached {
		ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
		body, err := d.Execute(ctx, call.Resource, args)
		expired := ctx.Err() != nil
		cancel()
		status := "succeeded"
		if err != nil || expired || len(body) > d.MaxOutput || !json.Valid(body) {
			status = "unknown"
			body = nil
		}
		settle, stop := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		e = g.store.FinishTool(settle, p.claim, req, g.cas, body, status)
		stop()
		if e != nil || status != "succeeded" {
			http.Error(w, `{"error":"tool_unconfirmed","retryable":false}`, 409)
			return
		}
	}
	body, e := g.store.ToolResult(r.Context(), p.claim, req, g.cas)
	if e != nil {
		deny("result")
		return
	}
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
