package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
)

func testConfig() Config {
	return Config{Price: budget.Pricing{Provider: "deepseek", Model: "flash-1017", Version: "synthetic.v1", InputMicrosPerMillion: 100, OutputMicrosPerMillion: 100}, ContextTokens: 65536, OutputTokens: 1024, MaxPromptBytes: 65536, EvidenceSHA256: strings.Repeat("a", 64)}
}
func TestDeepSeekAuthorizationAndLoopbackConfinement(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "unit-test-never-sent")
	t.Setenv("MGAR_ALLOW_PAID", "1")
	t.Setenv("MGAR_MAX_COST_USD", "2.00")
	t.Setenv("MGAR_LIVE_CASELIST", "eval/cases/frozen-live-24.json")
	for _, mode := range []string{"key", "paid", "budget", "whitelist", "model", "evidence", "confirm", "case", "output", "price"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig()
			confirm := true
			id := "live-001"
			switch mode {
			case "key":
				t.Setenv("DEEPSEEK_API_KEY", "")
			case "paid":
				t.Setenv("MGAR_ALLOW_PAID", "0")
			case "budget":
				t.Setenv("MGAR_MAX_COST_USD", "3")
			case "whitelist":
				t.Setenv("MGAR_LIVE_CASELIST", "")
			case "model":
				cfg.Price.Model = "other"
			case "evidence":
				cfg.EvidenceSHA256 = ""
			case "confirm":
				confirm = false
			case "case":
				id = "live-025"
			case "output":
				cfg.OutputTokens = 8193
			case "price":
				cfg.Price.InputMicrosPerMillion = 0
			}
			if c, e := NewDeepSeek(cfg, id, confirm); e == nil || c != nil {
				t.Fatal("gate accepted", mode)
			}
		})
	}
	// Construction only; no real HTTP request is made by this test.
	c, e := NewDeepSeek(testConfig(), "live-024", true)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if c.Local() || c.Model() != "flash-1017" {
		t.Fatal("wrong identity")
	}
	for _, u := range []string{"https://127.0.0.1:123", "http://localhost:123", "http://api.deepseek.com:80", "http://127.0.0.1:123@evil.invalid", "http://127.0.0.1:123/?key=x", "http://127.0.0.1:123/path"} {
		if c, e := NewLoopback(testConfig(), u); e == nil || c != nil {
			t.Fatal("loopback escape", u)
		}
	}
}
func TestDeepSeekBodyErrorsAreRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("Bearer sensitive-fixture-marker"))
	}))
	defer server.Close()
	c, e := NewLoopback(testConfig(), server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	// Internal transport classification; public API is exercised against real PG.
	_, u, e := c.send(context.Background(), []byte(`{}`))
	if e != ErrUnknown || u.Known || strings.Contains(e.Error(), "sensitive-fixture-marker") {
		t.Fatal(u, e)
	}
}
