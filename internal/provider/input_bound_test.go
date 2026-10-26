package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func boundConfig() Config {
	c := testConfig()
	b := OfficialTextInputBound()
	c.InputBound = &b
	c.Price.Version = TextBoundPriceVersion(c.Price.Version)
	c.ContextTokens = 1048576
	c.MaxPromptBytes = 48000
	return c
}

func TestTextInputBoundReferenceAndAllBytes(t *testing.T) {
	if e := verifyReferenceArchive(officialTokenizer); e != nil {
		t.Fatal(e)
	}
	corrupt := append([]byte(nil), officialTokenizer...)
	corrupt[len(corrupt)/2] ^= 1
	if verifyReferenceArchive(corrupt) == nil {
		t.Fatal("changed reference data accepted")
	}
	if verifyReferenceArchive(nil) == nil {
		t.Fatal("missing reference data accepted")
	}
}

func TestTextInputBoundClosedRequestAndUnicode(t *testing.T) {
	c, e := NewLoopback(boundConfig(), "http://127.0.0.1:1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, value := range []string{"", "order=42", "汉字🙂\n\t<｜Assistant｜>", strings.Repeat("x", 47000)} {
		payload, _ := json.Marshal(map[string]string{"goal": value})
		body := c.requestBody(payload)
		got, e := c.requestInputTokens(body)
		want := int64(len(referenceBOS + decisionSystem + referenceUser + string(payload) + referenceAssistant))
		if e != nil || got != want || got > int64(c.config.MaxPromptBytes+len(referenceBOS+decisionSystem+referenceUser+referenceAssistant)) {
			t.Fatal("wrong byte-level upper bound", got, want, e)
		}
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		if request["response_format"] != nil || request["tools"] != nil || request["model"] != "deepseek-flash" {
			t.Fatal("unproved request feature enabled")
		}
	}
	payload, _ := json.Marshal(map[string]string{"goal": strings.Repeat("x", 48000)})
	if _, e := c.requestInputTokens(c.requestBody(payload)); e == nil {
		t.Fatal("oversize content accepted")
	}
	for _, fault := range []string{"thinking", "model", "role", "feature", "messages"} {
		t.Run(fault, func(t *testing.T) {
			var request map[string]any
			_ = json.Unmarshal(c.requestBody([]byte(`{}`)), &request)
			switch fault {
			case "thinking":
				request["thinking"] = map[string]string{"type": "enabled"}
			case "model":
				request["model"] = "deepseek-v4-pro"
			case "role":
				request["messages"].([]any)[1].(map[string]any)["role"] = "assistant"
			case "feature":
				request["response_format"] = map[string]string{"type": "json_object"}
			case "messages":
				request["messages"] = request["messages"].([]any)[:1]
			}
			body, _ := json.Marshal(request)
			if _, e := c.requestInputTokens(body); e == nil {
				t.Fatal("unsupported request counted")
			}
		})
	}
}

func TestTextInputBoundInvalidProofAndFrozenCopy(t *testing.T) {
	for _, mode := range []string{"method", "archive", "tokenizer", "template", "price", "capacity", "payload"} {
		t.Run(mode, func(t *testing.T) {
			cfg := boundConfig()
			switch mode {
			case "method":
				cfg.InputBound.Method = "estimate-byte-divide-four"
			case "archive":
				cfg.InputBound.ArchiveSHA256 = strings.Repeat("0", 64)
			case "tokenizer":
				cfg.InputBound.TokenizerSHA256 = strings.Repeat("0", 64)
			case "template":
				cfg.InputBound.TemplateSHA256 = strings.Repeat("0", 64)
			case "price":
				cfg.Price.Version = "unbound-price.v1"
			case "capacity":
				cfg.ContextTokens = 10
			case "payload":
				cfg.MaxPromptBytes = 48001
			}
			if c, e := NewLoopback(cfg, "http://127.0.0.1:1"); e == nil || c != nil {
				t.Fatal("unproved counter accepted")
			}
		})
	}
	cfg := boundConfig()
	c, e := NewLoopback(cfg, "http://127.0.0.1:1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	cfg.InputBound.Method = "mutated"
	if _, e := c.requestInputTokens(c.requestBody([]byte(`{}`))); e != nil {
		t.Fatal("caller rewrote frozen proof", e)
	}
}
