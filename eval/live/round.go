package live

import (
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"os"
	"path/filepath"
	"strings"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
)

const round2ExperimentID = "P7-06-round2-deepseek-flash-1017-20261027"

type RoundCheckpoint struct {
	Experiment      string `json:"experiment_id"`
	ReportSHA       string `json:"report_sha256"`
	UpperCostMicros int64  `json:"upper_cost_micros"`
	ProtocolSHA     string `json:"protocol_sha256"`
}

func expectedRoundCheckpoint() RoundCheckpoint {
	return RoundCheckpoint{Experiment: experimentID, ReportSHA: "5ce3bc0799cc2811ec7cd93bbeb3a1143ec7d8ef4b01fa31579077d1d70a4d87", UpperCostMicros: 33850, ProtocolSHA: "f82ddafa9f150280cd59f066021cd8bc1ed488f516d4fd97b013d078b3ff119e"}
}
func normalizeRound(round string) string {
	if round == "original" {
		return ""
	}
	return round
}
func (p Plan) roundName() string {
	if p.ID == round2ExperimentID {
		return "round2"
	}
	if p.ID == experimentID {
		return ""
	}
	return "invalid"
}
func (p Plan) allocationID() string {
	return demo.LiveRunIDForRound(p.roundName(), "allocation", "all")
}
func (p Plan) roundSpec() (int64, int64, bool) {
	if p.ID == experimentID {
		return 2000000, 20833, p.Schema == "live-experiment.v1" && p.Previous == nil && p.Purpose == ""
	}
	if p.ID == round2ExperimentID {
		return 1900000, 19791, p.Schema == "live-experiment.v2" && p.Purpose == "post_fix_regression" && p.Previous != nil && *p.Previous == expectedRoundCheckpoint()
	}
	return 0, 0, false
}
func statePathFor(root, id string) string { return filepath.Join(root, "evidence/eval/live", id) }
func protocolFingerprint(root string) (string, error) {
	names := []string{"internal/demo/dispatch.go", "internal/demo/worker.go", "internal/policy/gateway.go", "eval/live/pricing.json", "eval/live/official-evidence.json"}
	var combined strings.Builder
	for _, name := range names {
		path := filepath.Join(root, name)
		if !safeRecoveryPath(path) {
			return "", ErrGate
		}
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			return "", ErrGate
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return "", ErrGate
		}
		combined.WriteString(name + "\n" + artifact.Digest(raw) + "\n")
	}
	return artifact.Digest([]byte(combined.String())), nil
}
func verifyRoundCheckpoint(root string, p Plan) error {
	if p.Previous == nil {
		return nil
	}
	c := p.Previous
	if *c != expectedRoundCheckpoint() || c.UpperCostMicros+p.CapMicros > 2000000 {
		return ErrGate
	}
	path := statePathFor(root, c.Experiment) + "/report.json"
	if !safeRecoveryPath(path) {
		return ErrGate
	}
	raw, e := os.ReadFile(path)
	if e != nil || len(raw) > 65536 || artifact.Digest(raw) != c.ReportSHA {
		return ErrGate
	}
	var prior Report
	if json.Unmarshal(raw, &prior) != nil || prior.Experiment != experimentID || prior.Status != "STOPPED_UNKNOWN" || prior.UpperCostMicros != c.UpperCostMicros || len(prior.Records) != 96 {
		return ErrGate
	}
	sha, e := protocolFingerprint(root)
	if e != nil || sha != c.ProtocolSHA {
		return ErrGate
	}
	return nil
}

// Readiness is read-only and catches an existing marker even if its filesystem
// journal is missing. The actual claim repeats schema/admission checks.
func allocationReadiness(ctx context.Context, s *store.Store, p Plan) error {
	if !allocationSchemaReady(ctx, s) {
		return ErrGate
	}
	var used bool
	if s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_runs WHERE run_id=$1)", p.allocationID()).Scan(&used) != nil {
		return ErrGate
	}
	if used {
		return ErrUsed
	}
	return nil
}
