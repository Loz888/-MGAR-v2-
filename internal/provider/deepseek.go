// Package provider owns trusted model transport. No exported method can send
// without scheduler admission and a committed PostgreSQL reservation.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

var ErrPolicy = errors.New("provider_configuration_denied")
var ErrUnknown = errors.New("provider_usage_unknown")
var ErrDecision = errors.New("provider_decision_invalid")
var modelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ContextTokens is the verified MODEL context ceiling, not a byte/4 estimate.
// Without InputBound the entire ceiling is reserved. The verified text-only
// mode reserves the reference serializer's UTF-8 byte upper bound instead.
// Output is separately reserved even for providers sharing that ceiling.
type Config struct {
	Price          budget.Pricing  `json:"price"`
	ContextTokens  int64           `json:"context_tokens"`
	OutputTokens   int64           `json:"output_tokens"`
	MaxPromptBytes int             `json:"max_prompt_bytes"`
	EvidenceSHA256 string          `json:"official_evidence_sha256"`
	InputBound     *TextInputBound `json:"input_bound,omitempty"`
}

func (c Config) valid() bool {
	return c.Price.Valid() && c.Price.Provider == "deepseek" && c.Price.InputMicrosPerMillion > 0 && c.Price.OutputMicrosPerMillion > 0 && c.ContextTokens > 0 && c.ContextTokens <= 9_000_000 && c.OutputTokens > 0 && c.OutputTokens <= 8192 && c.MaxPromptBytes > 0 && c.MaxPromptBytes <= 131072 && c.validInputMode()
}

type Client struct {
	config                        Config
	endpoint, key, source, caseID string
	http                          *http.Client
}

// NewDeepSeek accepts only the approved experiment, with externally verified
// official model/pricing evidence. Binds the client to one whitelisted case.
// A config hash is provenance, not independent proof of official correctness;
// the trusted live preflight must verify the referenced evidence first.
func NewDeepSeek(c Config, caseID string, confirm bool) (*Client, error) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	cap := os.Getenv("MGAR_MAX_COST_USD")
	if !c.valid() || c.Price.Model != "flash-1017" || !shaPattern.MatchString(c.EvidenceSHA256) || !confirm || os.Getenv("MGAR_ALLOW_PAID") != "1" || (cap != "2" && cap != "2.00" && cap != "2.000000") || os.Getenv("MGAR_LIVE_CASELIST") != "eval/cases/frozen-live-24.json" || len(key) == 0 || strings.ContainsAny(key, "\r\n") {
		return nil, ErrPolicy
	}
	found := false
	for i := 1; i <= 24; i++ {
		if caseID == fmtCase(i) {
			found = true
		}
	}
	if !found {
		return nil, ErrPolicy
	}
	client := newClient(c, "https://api.deepseek.com/chat/completions", key, "provider")
	client.caseID = caseID
	return client, nil
}
func fmtCase(i int) string { return "live-0" + string(rune('0'+i/10)) + string(rune('0'+i%10)) }

// NewLoopback can ONLY reach literal 127.0.0.1 with a synthetic credential.
// It always records usage_source=local_stub, never provider/live evidence.
func NewLoopback(c Config, endpoint string) (*Client, error) {
	u, e := url.Parse(endpoint)
	if !c.valid() || e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, ErrPolicy
	}
	return newClient(c, endpoint, "loopback-test-only", "local_stub"), nil
}
func newClient(c Config, endpoint, key, source string) *Client {
	if c.InputBound != nil {
		proof := *c.InputBound
		c.InputBound = &proof
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	if source == "local_stub" {
		transport.Proxy = nil
	} // only literal loopback; official uses inherited proxy/CA
	return &Client{config: c, endpoint: endpoint, key: key, source: source, http: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrPolicy }}}
}
func (c *Client) Close() {
	if c != nil {
		c.http.CloseIdleConnections()
	}
}
func (c *Client) Model() string { return c.config.Price.Model }
func (c *Client) Local() bool   { return c.source == "local_stub" }

// The approved logical version stays in the budget/snapshot identity. DeepSeek's
// published API name for DeepSeek-V4.1-Flash-1017 is deepseek-flash. The official
// evidence binds this mapping at review time; a moving alias is not a weights pin.
func (c *Client) apiModel() string {
	if c.config.Price.Model == "flash-1017" {
		return "deepseek-flash"
	}
	return c.config.Price.Model
}

// Call keeps network errors and provider bodies out of logs. Each logical call
// has ONE transport attempt; 429/503 and disconnects never trigger auto replay.
func (c *Client) Call(ctx context.Context, resources *scheduler.Resources, claim store.TaskClaim, id string, payload []byte) (decision []byte, call budget.ModelCall, err error) {
	if c == nil || resources == nil || len(payload) > c.config.MaxPromptBytes || !utf8.Valid(payload) || !json.Valid(payload) {
		return nil, call, ErrPolicy
	}
	if c.caseID != "" {
		var identity struct {
			CaseID string `json:"case_id"`
		}
		if json.Unmarshal(payload, &identity) != nil || identity.CaseID != c.caseID {
			return nil, call, ErrPolicy
		}
	}
	body := c.requestBody(payload)
	input, e := c.requestInputTokens(body)
	if e != nil {
		return nil, call, ErrPolicy
	}
	request, e := budget.PricedRequest(id, c.config.Price, input, c.config.OutputTokens, artifact.Digest(body))
	if e != nil {
		return nil, call, ErrPolicy
	}
	var semantic error
	call, err = resources.Model(ctx, claim, request, func(ctx context.Context) (budget.ModelUsage, bool, error) {
		var usage budget.ModelUsage
		decision, usage, semantic = c.send(ctx, body)
		// Known usage still settles a failed decision. Unknown stays reserved.
		if !usage.Known || usage.InputTokens > request.InputTokens || usage.OutputTokens > request.OutputTokens {
			return budget.ModelUsage{Status: "unknown", Source: "unknown"}, true, ErrUnknown
		}
		return usage, true, nil
	})
	if err != nil {
		return nil, call, err
	}
	return decision, call, semantic
}

func (c *Client) send(ctx context.Context, body []byte) ([]byte, budget.ModelUsage, error) {
	unknown := budget.ModelUsage{Status: "unknown", Source: "unknown"}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, unknown, ErrUnknown
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, unknown, ErrUnknown
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || resp.StatusCode != 200 {
		return nil, unknown, ErrUnknown
	}
	// Check duplicate keys, depth and non-JSON before selecting known API fields.
	var object map[string]any
	if verifier.StrictJSON(raw, &object) != nil {
		return nil, unknown, ErrUnknown
	}
	var wire struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Usage == nil || wire.Usage.Input == nil || wire.Usage.Output == nil || wire.Usage.Total == nil || !modelPattern.MatchString(wire.Model) {
		return nil, unknown, ErrUnknown
	}
	in, out, total := *wire.Usage.Input, *wire.Usage.Output, *wire.Usage.Total
	if in < 0 || out < 0 || in > c.config.ContextTokens || out > c.config.OutputTokens || total != in+out {
		return nil, unknown, ErrUnknown
	}
	cost, e := c.config.Price.Cost(in, out)
	if e != nil {
		return nil, unknown, ErrUnknown
	}
	usage := budget.ModelUsage{Status: "failed", Known: true, Source: c.source, InputTokens: in, OutputTokens: out, Tokens: total, CostMicros: cost, PricingVersion: c.config.Price.Version, ResponseModel: wire.Model, ResponseDigest: artifact.Digest(raw)}
	if len(wire.Choices) != 1 || wire.Choices[0].Finish != "stop" {
		return nil, usage, ErrDecision
	}
	decision := []byte(wire.Choices[0].Message.Content)
	var parsed map[string]any
	if len(decision) > 65536 || verifier.StrictJSON(decision, &parsed) != nil || parsed == nil {
		return nil, usage, ErrDecision
	}
	usage.Status = "succeeded"
	return decision, usage, nil
}
