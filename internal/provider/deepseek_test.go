package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/budget"
)

func TestDeepSeekOutputLimitVersusUsageCertainty(t *testing.T) {
	for _, mode := range []string{"length", "stop-at-cap", "invalid-json", "missing-usage", "over-cap-usage"} {
		t.Run(mode, func(t *testing.T) {
			finish, content := "stop", `{"type":"final","output":{}}`
			if mode == "length" || mode == "missing-usage" || mode == "over-cap-usage" {
				finish = "length"
			}
			if mode == "invalid-json" {
				content = `{"broken":`
			}
			output := 1024
			if mode == "over-cap-usage" {
				output++
			}
			wire := map[string]any{"model": "deepseek-flash", "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": content}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": output, "total_tokens": 10 + output}}
			if mode == "missing-usage" {
				delete(wire, "usage")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(wire) }))
			defer server.Close()
			client, e := NewLoopback(testConfig(), server.URL)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			_, u, e := client.send(context.Background(), []byte(`{}`))
			switch mode {
			case "length":
				if !errors.Is(e, ErrOutputLimit) || !u.Known || u.Status != "failed" {
					t.Fatal(u, e)
				}
			case "stop-at-cap":
				if e != nil || !u.Known || u.Status != "succeeded" {
					t.Fatal("exact ceiling is not evidence of truncation", u, e)
				}
			case "invalid-json":
				if !errors.Is(e, ErrDecision) || !u.Known || u.Status != "failed" {
					t.Fatal(u, e)
				}
			default:
				if !errors.Is(e, ErrUnknown) || u.Known {
					t.Fatal("invalid usage cannot become a known length failure", u, e)
				}
			}
		})
	}
}

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
