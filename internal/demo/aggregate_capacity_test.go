package demo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
)

type vocabTrie struct {
	children map[byte]*vocabTrie
	terminal bool
}

var vocabularyOnce sync.Once
var vocabularyRoot *vocabTrie
var vocabularyError error

// This is a minimum over every segmentation admitted by the published vocab,
// not a claimed implementation of its BPE/presplit algorithm. Ignoring merge
// ranks and split constraints can only make the minimum smaller. If this lower
// bound exceeds max_tokens, the full string cannot fit even ideally.
func minimumReferenceTokens(raw []byte) (int, error) {
	vocabularyOnce.Do(func() {
		data, e := os.ReadFile("../provider/data/deepseek_v4_tokenizer.zip")
		if e != nil || artifact.Digest(data) != provider.OfficialTextInputBound().ArchiveSHA256 {
			vocabularyError = fmt.Errorf("reference tokenizer archive mismatch")
			return
		}
		z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if e != nil {
			vocabularyError = e
			return
		}
		var tokenizer struct {
			Model struct {
				Vocab map[string]int `json:"vocab"`
			} `json:"model"`
			Added []struct {
				Content string `json:"content"`
			} `json:"added_tokens"`
		}
		for _, file := range z.File {
			if file.Name != "deepseek_v4_tokenizer/tokenizer.json" {
				continue
			}
			stream, e := file.Open()
			if e != nil {
				vocabularyError = e
				return
			}
			body, e := io.ReadAll(io.LimitReader(stream, 8<<20))
			_ = stream.Close()
			if e != nil || artifact.Digest(body) != provider.OfficialTextInputBound().TokenizerSHA256 || json.Unmarshal(body, &tokenizer) != nil {
				vocabularyError = fmt.Errorf("reference vocabulary mismatch")
				return
			}
		}
		if len(tokenizer.Model.Vocab) != 128000 {
			vocabularyError = fmt.Errorf("missing vocabulary")
			return
		}
		alphabet := map[rune]byte{}
		next := rune(256)
		for b := 0; b < 256; b++ {
			r := rune(b)
			if !((b >= 33 && b <= 126) || (b >= 161 && b <= 172) || b >= 174) {
				r = next
				next++
			}
			alphabet[r] = byte(b)
		}
		vocabularyRoot = &vocabTrie{}
		add := func(token []byte) {
			if len(token) == 0 {
				return
			}
			node := vocabularyRoot
			for _, b := range token {
				if node.children == nil {
					node.children = map[byte]*vocabTrie{}
				}
				if node.children[b] == nil {
					node.children[b] = &vocabTrie{}
				}
				node = node.children[b]
			}
			node.terminal = true
		}
		for word := range tokenizer.Model.Vocab {
			decoded := []byte{}
			valid := true
			for _, r := range word {
				b, ok := alphabet[r]
				if !ok {
					valid = false
					break
				}
				decoded = append(decoded, b)
			}
			if valid {
				add(decoded)
			}
		}
		for _, added := range tokenizer.Added {
			add([]byte(added.Content))
		}
	})
	if vocabularyError != nil {
		return 0, vocabularyError
	}
	dp := make([]int, len(raw)+1)
	for i := 1; i < len(dp); i++ {
		dp[i] = len(raw) + 1
	}
	for i := 0; i < len(raw); i++ {
		node := vocabularyRoot
		for j := i; j < len(raw); j++ {
			node = node.children[raw[j]]
			if node == nil {
				break
			}
			if node.terminal && dp[i]+1 < dp[j+1] {
				dp[j+1] = dp[i] + 1
			}
		}
	}
	return dp[len(raw)], nil
}

func TestDevelopmentAggregateCapacity(t *testing.T) {
	for _, id := range []string{"development-001", "development-002"} {
		t.Run(id, func(t *testing.T) {
			o := testOptions(t)
			o.Case, o.CostMicros = id, 20833
			o.Model, _ = developmentProvider(t, "B4", "bounded")
			r, e := RunExperiment(context.Background(), o, "B4")
			if e != nil || r.ExecutionStatus != "completed" || r.WorkflowSuccess == nil || !*r.WorkflowSuccess {
				kind := ""
				if report, ok := r.Result.(Report); ok {
					kind = report.ErrorKind
				}
				t.Fatalf("bounded output must close: status=%s kind=%s err=%v", r.ExecutionStatus, kind, e)
			}
			t.Log("local HTTP with published-vocabulary lower-bound quota and real PG/Verifier closes under fixed 1024 output limit")
		})
	}
}

func TestDevelopmentModelFailureClasses(t *testing.T) {
	for _, tc := range []struct{ mode, expected string }{{"aggregate-length", "model_output_limit"}, {"aggregate-invalid", "model_invalid_response"}, {"unknown", "model_usage_unknown"}, {"unaffordable", "budget_exhausted"}} {
		t.Run(tc.mode, func(t *testing.T) {
			o := testOptions(t)
			o.Case, o.CostMicros = "development-002", 20833
			model, sent := developmentProvider(t, "B4", tc.mode)
			o.Model = model
			r, e := RunExperiment(context.Background(), o, "B4")
			if e != nil {
				t.Fatal(e)
			}
			report := r.Result.(Report)
			found := false
			for _, w := range report.Workers {
				if w.ErrorKind != nil && *w.ErrorKind == tc.expected {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s; workers=%+v", tc.expected, report.Workers)
			}
			if tc.mode == "unknown" {
				if report.Budget.UnknownModels != 1 || report.Budget.ReservedCostMicros == 0 || sent.Load() != 1 {
					t.Fatal("unknown not retained")
				}
			} else if report.Budget.ReservedModels != 0 || report.Budget.UnknownModels != 0 || (tc.mode == "unaffordable" && sent.Load() != 0) {
				t.Fatal("known/not-sent failure misaccounted")
			}
		})
	}
}
