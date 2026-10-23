package context

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
)

var ErrSelection = errors.New("CONTEXT_SELECTION_INVALID")

const SelectorVersion = "artifact-selector.v1"
const TokenEstimator = "utf8_bytes_div4.v1"

type SelectedItem struct {
	ID                 string `json:"artifact_id"`
	Hash               string `json:"content_sha256"`
	Text               string `json:"text"`
	Original           bool   `json:"original"`
	Lossy              bool   `json:"lossy"`
	Verified           bool   `json:"verified"`
	DerivedArtifactURI string `json:"derived_artifact_uri,omitempty"`
}
type SelectionEnvelope struct {
	SourceSnapshotHash string         `json:"source_snapshot_sha256"`
	Mode               string         `json:"mode"`
	PolicyVersion      string         `json:"policy_version"`
	TokenEstimator     string         `json:"token_estimator"`
	Items              []SelectedItem `json:"items"`
}
type DerivedSummary struct {
	ID                 string `json:"artifact_id"`
	SchemaVersion      string `json:"schema_version"`
	SourceID           string `json:"source_artifact_id"`
	SourceHash         string `json:"source_content_sha256"`
	SourceSnapshotHash string `json:"source_snapshot_sha256"`
	Text               string `json:"text"`
	Lossy              bool   `json:"lossy"`
	Verified           bool   `json:"verified"`
}

func (d DerivedSummary) Bytes() []byte { return Canonical(d) }

type SelectionDecision struct {
	ID     string `json:"artifact_id"`
	Reason string `json:"reason"`
	Score  int    `json:"score"`
}
type SelectionResult struct {
	Mode            string              `json:"mode"`
	PolicyVersion   string              `json:"policy_version"`
	TokenEstimator  string              `json:"token_estimator"`
	Blocked         bool                `json:"blocked"`
	BlockReason     string              `json:"block_reason,omitempty"`
	Admitted        []SelectionDecision `json:"admitted"`
	Rejected        []SelectionDecision `json:"rejected"`
	Summaries       []DerivedSummary    `json:"summaries"`
	WireJSON        string              `json:"wire_json"`
	UsedBytes       int                 `json:"used_bytes"`
	EstimatedTokens int                 `json:"estimated_tokens"`
	RequiredBytes   int                 `json:"required_bytes"`
}

func DerivedID(body []byte) string {
	h := []byte(artifact.Digest(body)[:32])
	h[12] = '8'
	// RFC 9562 UUIDv8 in the RFC 4122 variant, derived within this Task/snapshot.
	h[16] = "89ab"[h[16]%4]
	return string(h[:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:])
}
func terms(value string) map[string]bool {
	result := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(word) > 2 {
			result[word] = true
		}
	}
	return result
}
func Select(s Snapshot, goal, mode string, maxBytes, maxTokens int, content map[string][]byte) (SelectionResult, error) {
	result := SelectionResult{Mode: mode, PolicyVersion: SelectorVersion, TokenEstimator: TokenEstimator, Admitted: []SelectionDecision{}, Rejected: []SelectionDecision{}, Summaries: []DerivedSummary{}}
	if s.Selection != nil || len(goal) > 4096 || !utf8.ValidString(goal) || (mode != "full" && mode != "summary" && mode != "selected") || maxBytes < 1 || maxBytes > 65536 || maxTokens < 1 || maxTokens > 65536 || len(s.Artifacts) > 64 || len(content) != len(s.Artifacts) {
		return result, ErrSelection
	}
	type scored struct {
		ref      Ref
		score    int
		critical bool
	}
	candidates := []scored{}
	seen := map[string]bool{}
	goalTerms := terms(goal)
	for _, ref := range s.Artifacts {
		body, ok := content[ref.ID]
		if !ok || seen[ref.ID] || len(body) > artifact.MaxBytes || int64(len(body)) != ref.ByteLength || artifact.Digest(body) != ref.Hash || !utf8.Valid(body) {
			return result, ErrSelection
		}
		seen[ref.ID] = true
		critical := ref.Kind == "finding" || ref.Kind == "sql_evidence" || ref.Kind == "patch" || ref.Kind == "verdict"
		score := 0
		if critical {
			score = 100
		}
		if s.Role == "data" && ref.Kind == "sql_evidence" {
			score += 40
		}
		if s.Role == "action" && ref.Kind == "patch" {
			score += 40
		}
		words := terms(string(body))
		for word := range goalTerms {
			if words[word] {
				score += 10
			}
		}
		candidates = append(candidates, scored{ref, score, critical})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if mode == "selected" && candidates[i].critical != candidates[j].critical {
			return candidates[i].critical
		}
		if mode == "selected" && candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].ref.ID < candidates[j].ref.ID
	})
	sourceHash := s.Hash()
	envelope := &SelectionEnvelope{SourceSnapshotHash: sourceHash, Mode: mode, PolicyVersion: SelectorVersion, TokenEstimator: TokenEstimator, Items: []SelectedItem{}}
	s.Selection = envelope
	fits := func(wire []byte) bool { return len(wire) <= maxBytes && (len(wire)+3)/4 <= maxTokens }
	if !fits(s.Bytes()) {
		result.Blocked = true
		result.BlockReason = "snapshot_metadata_exceeds_budget"
		result.RequiredBytes = len(s.Bytes())
		return result, nil
	}
	for _, candidate := range candidates {
		ref := candidate.ref
		decision := SelectionDecision{ID: ref.ID, Score: candidate.score}
		if mode == "selected" && candidate.score < 40 {
			decision.Reason = "low_relevance"
			result.Rejected = append(result.Rejected, decision)
			continue
		}
		item := SelectedItem{ID: ref.ID, Hash: ref.Hash, Text: string(content[ref.ID]), Original: true}
		var derived DerivedSummary
		if mode == "summary" {
			excerpt := content[ref.ID]
			if len(excerpt) > 256 {
				excerpt = excerpt[:256]
				for !utf8.Valid(excerpt) {
					excerpt = excerpt[:len(excerpt)-1]
				}
			}
			derived = DerivedSummary{SchemaVersion: "summary.v1", SourceID: ref.ID, SourceHash: ref.Hash, SourceSnapshotHash: sourceHash, Text: string(excerpt), Lossy: true}
			derived.ID = DerivedID(derived.Bytes())
			item.Text = derived.Text
			item.Original = false
			item.Lossy = true
			item.DerivedArtifactURI = "artifact://" + derived.ID
		}
		envelope.Items = append(envelope.Items, item)
		wire := s.Bytes()
		if !fits(wire) {
			envelope.Items = envelope.Items[:len(envelope.Items)-1]
			decision.Reason = "byte_or_estimated_token_budget"
			result.Rejected = append(result.Rejected, decision)
			if candidate.critical {
				result.Blocked = true
				result.BlockReason = "critical_dependency_exceeds_budget"
				result.RequiredBytes = len(wire)
			}
			continue
		}
		decision.Reason = "admitted_" + mode
		result.Admitted = append(result.Admitted, decision)
		if mode == "summary" {
			result.Summaries = append(result.Summaries, derived)
		}
	}
	if result.Blocked {
		return result, nil
	}
	result.WireJSON = string(s.Bytes())
	result.UsedBytes = len(result.WireJSON)
	result.EstimatedTokens = (result.UsedBytes + 3) / 4
	return result, nil
}
