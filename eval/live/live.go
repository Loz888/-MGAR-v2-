// Package live owns the single approved 96-assignment experiment. Durable
// exclusive files prevent automatic allocation reuse; PG enforces each share.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

const ManifestSHA = "ec6d5576a75fc2724756abeef2628189c2922038f09cb9258d7a1972e493c8ab"
const DatasetSHA = "0b48a86008574d0a8bdbde1d88d37e718d45e75fe33363075fdcd48f0eb2d511"
const experimentID = "P7-06-deepseek-flash-1017-20261026"

var ErrGate = errors.New("live_preflight_denied")
var ErrUsed = errors.New("live_allocation_already_used_or_uncertain")

type Assignment struct {
	Case      string `json:"case_id"`
	Arm       string `json:"arm"`
	RunID     string `json:"run_id"`
	CapMicros int64  `json:"cap_micros"`
	Status    string `json:"status"`
}
type Plan struct {
	Schema           string       `json:"schema_version"`
	ID               string       `json:"experiment_id"`
	Provider         string       `json:"provider"`
	Model            string       `json:"model"`
	CapMicros        int64        `json:"approved_cap_micros"`
	ManifestSHA      string       `json:"manifest_sha256"`
	DatasetSHA       string       `json:"dataset_manifest_sha256"`
	Temperature      int          `json:"temperature"`
	PlannerScope     string       `json:"planner_scope"`
	MaxContextBytes  int          `json:"max_context_bytes"`
	MaxContextTokens int          `json:"max_context_tokens"`
	Arms             []string     `json:"arms"`
	DeferredArms     []string     `json:"not_run_arms"`
	Assignments      []Assignment `json:"assignments"`
}

func (p Plan) Validate() error {
	if p.PlannerScope != "fixed_business_dag_template" {
		return ErrGate
	}
	if p.Schema != "live-experiment.v1" || p.ID != experimentID || p.Provider != "deepseek" || p.Model != "flash-1017" || p.CapMicros != 2_000_000 || p.ManifestSHA != ManifestSHA || p.DatasetSHA != DatasetSHA || p.Temperature != 0 || p.MaxContextBytes != 48000 || p.MaxContextTokens != 12000 || !slices.Equal(p.Arms, []string{"B0", "B1", "B2", "B4"}) || !slices.Equal(p.DeferredArms, []string{"B3", "A1", "A2"}) || len(p.Assignments) != 96 {
		return ErrGate
	}
	for i, a := range p.Assignments {
		id := fmt.Sprintf("live-%03d", i/4+1)
		arm := p.Arms[i%4]
		if a.Case != id || a.Arm != arm || a.RunID != demo.LiveRunID(id, arm) || a.CapMicros != 20833 || a.Status != "NOT_RUN" {
			return ErrGate
		}
	}
	return nil
}
func readJSON(path string, out any) error {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return ErrGate
	}
	raw, e := os.ReadFile(path)
	if e != nil || verifier.StrictJSON(raw, out) != nil {
		return ErrGate
	}
	return nil
}
func LoadPlan(root string) (p Plan, err error) {
	if readJSON(filepath.Join(root, "eval/live/plan.json"), &p) != nil || p.Validate() != nil {
		return p, ErrGate
	}
	for name, hash := range map[string]string{"eval/cases/frozen-live-24.json": ManifestSHA, "eval/dataset_manifest.json": DatasetSHA} {
		raw, e := os.ReadFile(filepath.Join(root, name))
		if e != nil || artifact.Digest(raw) != hash {
			return p, ErrGate
		}
	}
	// Verify all frozen data, including independently frozen Oracle, before any send.
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	raw, _ := os.ReadFile(filepath.Join(root, "eval/dataset_manifest.json"))
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.Files) == 0 {
		return p, ErrGate
	}
	for name, hash := range manifest.Files {
		if !filepath.IsLocal(name) {
			return p, ErrGate
		}
		raw, e := os.ReadFile(filepath.Join(root, name))
		if e != nil || artifact.Digest(raw) != hash {
			return p, ErrGate
		}
	}
	return p, nil
}

type PricingEvidence struct {
	Status     string          `json:"status"`
	Config     provider.Config `json:"config"`
	PricingURL string          `json:"pricing_url"`
	ModelsURL  string          `json:"models_url"`
	ReviewedAt string          `json:"reviewed_at"`
}
type PreflightReport struct {
	Ready           bool            `json:"ready"`
	Assigned        int             `json:"assigned"`
	CapMicros       int64           `json:"cap_micros"`
	AllocatedMicros int64           `json:"allocated_micros"`
	Blockers        []string        `json:"blockers"`
	Network         string          `json:"network"`
	Assignments     []Assignment    `json:"assignments"`
	Pricing         PricingEvidence `json:"pricing"`
}

func statePath(root string) string { return filepath.Join(root, "evidence/eval/live", experimentID) }

// A fixed, zero-spend control Run is an additional durable allocation marker.
// Its existing UUID uniqueness survives loss of the ENTIRE filesystem journal
// and competing checkouts using the same synthetic DB. No new table/DDL.
func claimExperiment(ctx context.Context, s *store.Store, p Plan) error {
	if p.Validate() != nil {
		return ErrGate
	}
	raw, _ := json.Marshal(p)
	w, c, e := s.CreateRunClaimed(ctx, demo.LiveRunID("allocation", "all"), "Live allocation "+p.ID+" plan_sha256="+artifact.Digest(raw), 1, "live-allocation-gate", time.Minute)
	if e != nil {
		return ErrUsed
	}
	if e = s.ConfigureBudget(ctx, w.RunID, c, budget.Limits{ConcurrentCalls: 1, TaskConcurrency: 1, Delegations: 1}); e != nil {
		return ErrUsed
	}
	if _, e = s.CancelWorkflow(ctx, w.RunID, w.Revision); e != nil {
		return ErrUsed
	}
	return nil
}
func Preflight(root string, confirm bool) (r PreflightReport) {
	r.Blockers = []string{}
	r.Network = "NOT_PROBED: check managed environment readiness before invocation"
	p, e := LoadPlan(root)
	if e != nil {
		r.Blockers = append(r.Blockers, "frozen_plan_or_dataset_invalid")
		return r
	}
	r.Assigned = 96
	r.CapMicros = p.CapMicros
	r.AllocatedMicros = 96 * 20833
	r.Assignments = p.Assignments
	if !confirm {
		r.Blockers = append(r.Blockers, "confirm_live_required")
	}
	if os.Getenv("MGAR_ALLOW_PAID") != "1" {
		r.Blockers = append(r.Blockers, "paid_flag_required")
	}
	if v := os.Getenv("MGAR_MAX_COST_USD"); v != "2" && v != "2.00" && v != "2.000000" {
		r.Blockers = append(r.Blockers, "exact_two_dollar_cap_required")
	}
	if os.Getenv("MGAR_LIVE_CASELIST") != "eval/cases/frozen-live-24.json" {
		r.Blockers = append(r.Blockers, "frozen_caselist_required")
	}
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		r.Blockers = append(r.Blockers, "secure_deepseek_key_required")
	}
	if e = readJSON(filepath.Join(root, "eval/live/pricing.json"), &r.Pricing); e != nil || r.Pricing.Status != "VERIFIED" {
		r.Blockers = append(r.Blockers, "official_model_and_pricing_unverified")
	} else {
		cfg := r.Pricing.Config
		reviewed, e := time.Parse(time.RFC3339, r.Pricing.ReviewedAt)
		raw, readErr := os.ReadFile(filepath.Join(root, "eval/live/official-evidence.json"))
		if e != nil || reviewed.After(time.Now()) || time.Since(reviewed) > 7*24*time.Hour || readErr != nil || artifact.Digest(raw) != cfg.EvidenceSHA256 || r.Pricing.PricingURL != "https://api-docs.deepseek.com/quick_start/pricing" || r.Pricing.ModelsURL != "https://api.deepseek.com/models" || cfg.Price.Provider != "deepseek" || cfg.Price.Model != p.Model || cfg.Price.Version == "" || cfg.Price.InputMicrosPerMillion <= 0 || cfg.Price.OutputMicrosPerMillion <= 0 || cfg.ContextTokens <= 0 || cfg.OutputTokens <= 0 || cfg.OutputTokens > 8192 || cfg.MaxPromptBytes != 65536 {
			r.Blockers = append(r.Blockers, "official_evidence_invalid_or_expired")
		}
		price, priceErr := cfg.Price.Cost(cfg.ContextTokens, cfg.OutputTokens)
		if priceErr != nil || price > 20833 || cfg.ContextTokens+cfg.OutputTokens > 65536 {
			r.Blockers = append(r.Blockers, "conservative_call_bound_exceeds_slot_budget")
		}
	}
	if _, e = os.Lstat(statePath(root)); !errors.Is(e, os.ErrNotExist) {
		r.Blockers = append(r.Blockers, "experiment_already_started_or_state_uncertain")
	}
	r.Ready = len(r.Blockers) == 0
	return r
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func saveNew(path string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(append(raw, '\n'))
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDir(filepath.Dir(path))
}

// Never remove a start directory on failure, even before the first HTTP request.
// Lost manifest, partial writes and crashes all consume the allocation pending
// manual reconciliation. There is intentionally no force/resume/reset switch.
func begin(path string, p Plan) (string, error) {
	if p.Validate() != nil {
		return "", ErrGate
	}
	parent := filepath.Dir(path)
	if e := os.MkdirAll(parent, 0700); e != nil {
		return "", ErrUsed
	}
	if resolved, e := filepath.EvalSymlinks(parent); e != nil || resolved != parent {
		return "", ErrUsed
	}
	if e := os.Mkdir(path, 0700); e != nil {
		return "", ErrUsed
	}
	if e := syncDir(parent); e != nil {
		return "", ErrUsed
	}
	if e := saveNew(filepath.Join(path, "assignment.json"), p); e != nil {
		return "", ErrUsed
	}
	return path, nil
}

type Record struct {
	Assignment      Assignment `json:"assignment"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	RawSHA          string     `json:"raw_sha256,omitempty"`
	KnownCostMicros int64      `json:"known_cost_micros"`
	UpperCostMicros int64      `json:"upper_cost_micros"`
}
type Report struct {
	Experiment      string   `json:"experiment_id"`
	Status          string   `json:"status"`
	Records         []Record `json:"records"`
	KnownCostMicros int64    `json:"known_cost_micros"`
	UpperCostMicros int64    `json:"upper_cost_micros"`
}

func execute(ctx context.Context, path string, p Plan, invoke func(context.Context, Assignment) (demo.ExperimentReport, error)) (r Report, err error) {
	r = Report{Experiment: p.ID, Status: "EXECUTED_PENDING_INDEPENDENT_EVALUATION", Records: make([]Record, len(p.Assignments))}
	for i, a := range p.Assignments {
		r.Records[i] = Record{Assignment: a, Status: "NOT_RUN", Reason: "not_started"}
	}
	for i, a := range p.Assignments {
		if ctx.Err() != nil {
			r.Status = "INTERRUPTED"
			break
		}
		if e := saveNew(filepath.Join(path, fmt.Sprintf("%03d.started.json", i)), a); e != nil {
			return r, ErrUsed
		}
		record := &r.Records[i]
		record.Status = "UNKNOWN"
		record.Reason = "execution_or_accounting_unconfirmed"
		record.UpperCostMicros = a.CapMicros
		child, cancel := context.WithTimeout(ctx, 210*time.Second)
		observed, e := invoke(child, a)
		cancel()
		raw, _ := json.Marshal(observed)
		if saveErr := saveNew(filepath.Join(path, fmt.Sprintf("%03d.raw.json", i)), observed); saveErr != nil {
			return r, saveErr
		}
		record.RawSHA = artifact.Digest(append(raw, '\n'))
		// A complete report still may contain unknown reservations: stop the whole
		// experiment and retain the share. Never open a fresh Run for that assignment.
		if e == nil && observed.RunID == a.RunID && observed.Case.ID == a.Case && observed.Arm == a.Arm && slices.Contains([]string{"completed", "failed", "blocked"}, observed.ExecutionStatus) {
			var result struct {
				Budget budget.Report `json:"budget"`
			}
			resultBytes, _ := json.Marshal(observed.Result)
			if json.Unmarshal(resultBytes, &result) == nil && result.Budget.Conserved && result.Budget.Limits.CostMicros == a.CapMicros && int64(len(observed.Models)) == result.Budget.ReservedModels+result.Budget.SettledModels && result.Budget.SettledCostMicros >= 0 && result.Budget.ReservedCostMicros >= 0 && result.Budget.SettledCostMicros+result.Budget.ReservedCostMicros <= a.CapMicros {
				record.KnownCostMicros = result.Budget.SettledCostMicros
				if result.Budget.UnknownModels == 0 && result.Budget.PendingModels == 0 {
					record.Status = observed.ExecutionStatus
					record.Reason = ""
					record.UpperCostMicros = result.Budget.SettledCostMicros + result.Budget.ReservedCostMicros
				}
			}
		}
		r.KnownCostMicros += record.KnownCostMicros
		r.UpperCostMicros += record.UpperCostMicros
		if e := saveNew(filepath.Join(path, fmt.Sprintf("%03d.result.json", i)), record); e != nil {
			return r, e
		}
		if record.Status == "UNKNOWN" {
			r.Status = "STOPPED_UNKNOWN"
			break
		}
	}
	if e := saveNew(filepath.Join(path, "report.json"), r); e != nil {
		return r, e
	}
	return r, nil
}
func Run(ctx context.Context, root string, confirm bool) (Report, error) {
	checked := Preflight(root, confirm)
	if !checked.Ready {
		return Report{}, ErrGate
	}
	p, e := LoadPlan(root)
	if e != nil {
		return Report{}, e
	}
	allocation, e := store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
	if e != nil {
		return Report{}, ErrGate
	}
	e = claimExperiment(ctx, allocation, p)
	allocation.Pool.Close()
	if e != nil {
		return Report{}, e
	}
	path, e := begin(statePath(root), p)
	if e != nil {
		return Report{}, e
	}
	if e = saveNew(filepath.Join(path, "preflight.json"), checked); e != nil {
		return Report{}, e
	}
	exe, e := os.Executable()
	if e != nil {
		return Report{}, e
	}
	binary, e := os.ReadFile(exe)
	if e != nil {
		return Report{}, e
	}
	if e = saveNew(filepath.Join(path, "executor.json"), map[string]string{"sha256": artifact.Digest(binary), "started_at": time.Now().UTC().Format(time.RFC3339)}); e != nil {
		return Report{}, e
	}
	return execute(ctx, path, p, func(ctx context.Context, a Assignment) (demo.ExperimentReport, error) {
		client, e := provider.NewDeepSeek(checked.Pricing.Config, a.Case, confirm)
		if e != nil {
			return demo.ExperimentReport{}, e
		}
		defer client.Close()
		return demo.RunExperiment(ctx, demo.Options{Root: root, Case: a.Case, Fault: "none", Model: client, RunID: a.RunID, CostMicros: a.CapMicros}, a.Arm)
	})
}
