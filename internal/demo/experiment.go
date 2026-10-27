package demo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/evalbaseline"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/provider"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Arm treatments are a closed controller-owned vocabulary, never model flags.
type Arm struct {
	Multi   bool   `json:"multi_role"`
	Dynamic bool   `json:"dynamic_plan"`
	Context string `json:"context"`
	Verify  bool   `json:"internal_verifier"`
	Replan  bool   `json:"replan"`
}

func ExperimentArms() map[string]Arm {
	return map[string]Arm{
		"B0": {false, false, "full", false, false}, "B1": {true, false, "full", false, false},
		"B2": {true, true, "full", false, true}, "B3": {true, true, "selected", false, true},
		"B4": {true, true, "selected", true, true}, "A1": {true, true, "selected", true, false},
		"A2": {true, true, "selected", false, true},
	}
}

type ExperimentCase struct {
	ID       string `json:"case_id"`
	Category string `json:"category"`
	Seed     int    `json:"seed"`
	Negative bool   `json:"negative"`
	Limits   struct {
		Models   int64 `json:"model_calls"`
		Tools    int64 `json:"tool_calls"`
		Tokens   int64 `json:"tokens"`
		Cost     int64 `json:"cost_micros"`
		Parallel int   `json:"max_parallel"`
	} `json:"limits"`
}
type ExperimentReport struct {
	Schema              string                         `json:"schema_version"`
	Arm                 string                         `json:"arm"`
	Treatment           Arm                            `json:"treatment"`
	Case                ExperimentCase                 `json:"case"`
	RunID               string                         `json:"run_id"`
	ExecutionStatus     string                         `json:"execution_status"`
	WorkflowSuccess     *bool                          `json:"workflow_success"`
	Answer              map[string]any                 `json:"answer"`
	Namespace           string                         `json:"database_namespace"`
	CASNamespace        string                         `json:"artifact_namespace"`
	Result              any                            `json:"result"`
	Models              []json.RawMessage              `json:"model_calls"`
	Tools               []json.RawMessage              `json:"tool_calls"`
	Plans               []planner.Proposal             `json:"business_plans"`
	Selections          []inputcontext.SelectionResult `json:"candidate_selections"`
	Replans             []store.ReplanResult           `json:"replans"`
	InputTransitions    []string                       `json:"input_transitions"`
	DuplicateDeliveries int                            `json:"duplicate_deliveries"`
	ExternalOracle      map[string]any                 `json:"execution_oracle"`
	ElapsedMS           float64                        `json:"experiment_elapsed_ms"`
	WorkflowMS          *float64                       `json:"durable_workflow_latency_ms"`
	Error               string                         `json:"error,omitempty"`
}
type experiment struct {
	arm       Arm
	c         ExperimentCase
	r         *ExperimentReport
	refreshed bool
	model     *provider.Client
}
type injectedToolFailure struct{}

func (injectedToolFailure) Error() string          { return "injected_data_tool_failure" }
func (injectedToolFailure) ExecutionStopped() bool { return true }

func RunExperiment(ctx context.Context, o Options, arm string) (r ExperimentReport, err error) {
	cfg, ok := ExperimentArms()[arm]
	if !ok || o.Fault != "none" {
		return r, errors.New("experiment_configuration_denied")
	}
	// No truth file is read by an executor. The caller additionally validates the frozen manifest.
	manifest := "eval/cases/offline-60.json"
	if o.Model != nil {
		if arm != "B0" && arm != "B1" && arm != "B2" && arm != "B4" {
			return r, errors.New("live_arm_denied")
		}
		if o.CostMicros <= 0 || o.CostMicros > 20833 {
			return r, errors.New("live_budget_denied")
		}
		if o.Model.Local() && strings.HasPrefix(o.Case, "development-") {
			manifest = "eval/cases/development.json"
		} else if !o.Model.Local() && strings.HasPrefix(o.Case, "live-") && o.RunID == LiveRunIDForRound(o.Round, o.Case, arm) && o.CostMicros == LiveCostMicros(o.Round) && LiveCostMicros(o.Round) > 0 {
			manifest = "eval/cases/frozen-live-24.json"
		} else {
			return r, errors.New("live_case_identity_denied")
		}
	}
	raw, e := os.ReadFile(filepath.Join(o.Root, manifest))
	if e != nil {
		return r, e
	}
	var cases []ExperimentCase
	if json.Unmarshal(raw, &cases) != nil {
		return r, errors.New("invalid_case_manifest")
	}
	var c ExperimentCase
	for _, candidate := range cases {
		if candidate.ID == o.Case {
			c = candidate
		}
	}
	if c.ID == "" || c.Limits.Parallel != 4 || c.Limits.Models < 0 || c.Limits.Models > 64 || c.Limits.Tools != 64 || c.Limits.Tokens != 65536 || c.Limits.Cost != 1000000 {
		return r, errors.New("experiment_case_denied")
	}
	if o.Model != nil {
		c.Limits.Cost = o.CostMicros
	}
	r = ExperimentReport{Schema: "experiment.v1", Arm: arm, Treatment: cfg, Case: c, Models: []json.RawMessage{}, Tools: []json.RawMessage{}, Plans: []planner.Proposal{}, Selections: []inputcontext.SelectionResult{}, Replans: []store.ReplanResult{}, InputTransitions: []string{}, ExecutionStatus: "failed", Answer: map[string]any{}}
	x := &experiment{arm: cfg, c: c, r: &r, model: o.Model}
	start := time.Now()
	defer func() {
		r.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			r.Error = "experiment_control_error"
		}
	}()
	if cfg.Verify {
		result, e := runFull(ctx, o, x)
		err = e
		r.Result = result
		r.RunID = result.RunID
		r.Answer = result.Answer
		success := result.State == "succeeded"
		r.WorkflowSuccess = &success
		if success {
			r.ExecutionStatus = "completed"
		} else if result.State == "blocked" {
			r.ExecutionStatus = "blocked"
		}
	} else {
		result, e := runBaseline(ctx, o, x)
		err = e
		r.Result = result
		r.RunID = result.RunID
		r.Answer = result.Answer
		r.ExecutionStatus = result.ExecutionStatus
	}
	if err != nil {
		r.ExecutionStatus = "failed"
	}
	return r, err
}
func (x *experiment) limits() budget.Limits {
	return budget.Limits{ModelCalls: x.c.Limits.Models, ToolCalls: x.c.Limits.Tools, Tokens: x.c.Limits.Tokens, CostMicros: x.c.Limits.Cost, ConcurrentCalls: int64(x.c.Limits.Parallel), TaskConcurrency: x.c.Limits.Parallel, Delegations: 32}
}
func (x *experiment) prepare(ctx context.Context, f *fixture) error {
	x.r.Namespace = f.schema
	x.r.CASNamespace = artifact.Digest([]byte(f.temp))
	if len(f.input.Prior) > 0 {
		if e := x.rotate(ctx, f, f.input.Prior); e != nil {
			return e
		}
		x.r.InputTransitions = append(x.r.InputTransitions, "prior_input_opened")
	}
	return nil
}
func (x *experiment) rotate(ctx context.Context, f *fixture, orders []Order) error {
	if f.source != nil {
		if e := f.source.Rollback(ctx); e != nil {
			return e
		}
		f.source = nil
	}
	tx, e := f.s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, row := range orders {
		tag, e := tx.Exec(ctx, "UPDATE orders SET amount=$1 WHERE event_id=$2 AND order_id=$3 AND status=$4", row.Amount, row.EventID, row.ID, row.Status)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errors.New("owned_fixture_row_mismatch")
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	f.source, e = f.s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return e
	}
	if e = f.source.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&f.sql.Snapshot); e != nil {
		return e
	}
	rows := [][]any{}
	for _, r := range orders {
		rows = append(rows, []any{r.EventID, r.ID, r.Status, r.Amount})
	}
	normalized, e := verifier.NormalizeRows(inputcontext.Canonical(rows))
	if e != nil {
		return e
	}
	f.sql.InputDigest = artifact.Digest([]byte(normalized))
	return nil
}
func (x *experiment) afterMeasurements(ctx context.Context, f *fixture, nodes []planner.Node) error {
	if x == nil || x.refreshed || len(f.input.Prior) == 0 {
		return nil
	}
	hasData := false
	for _, n := range nodes {
		hasData = hasData || n.Role == "data" || n.Key == "single"
	}
	if !hasData {
		return nil
	}
	if e := x.rotate(ctx, f, f.input.Orders); e != nil {
		return e
	}
	x.refreshed = true
	x.r.InputTransitions = append(x.r.InputTransitions, "current_input_opened_after_prior_observation")
	return nil
}
func (x *experiment) definitions(f *fixture, c store.TaskClaim, snap inputcontext.Snapshot, state *taskTools, single bool) []policy.Definition {
	defs := f.definitions(c, snap, state)
	if single {
		defs = nil
		roles := []string{"research", "data"}
		if f.input.Business == "incident" {
			roles = append(roles, "action")
		}
		for _, role := range roles {
			source := c
			source.Role = role
			for _, d := range f.definitions(source, snap, state) {
				d.Role = c.Role
				defs = append(defs, d)
			}
		}
	}
	for i := range defs {
		if defs[i].Name != "data.query" {
			continue
		}
		execute := defs[i].Execute
		defs[i].Execute = func(ctx context.Context, resource string, args map[string]any) ([]byte, error) {
			if x.c.Category == "task_failure" {
				return nil, injectedToolFailure{}
			}
			if !single {
				return execute(ctx, resource, args)
			}
			// Single action-role carrier also acquires the SQL resource pool; gateway
			// authorization and the business Reader Role remain unchanged.
			resources, e := scheduler.NewResources(ctx, f.s)
			if e != nil {
				return nil, e
			}
			var raw []byte
			e = resources.Run(ctx, c, "db", 0, func(ctx context.Context) (bool, error) {
				var inner error
				raw, inner = execute(ctx, resource, args)
				return true, inner
			})
			return raw, e
		}
	}
	return defs
}
func (x *experiment) selectCandidates(c store.TaskClaim, all []evalbaseline.Candidate) ([]evalbaseline.Candidate, error) {
	refs := []inputcontext.Ref{}
	content := map[string][]byte{}
	for _, a := range all {
		refs = append(refs, inputcontext.Ref{ID: a.ID, Hash: a.Hash, ProducerNode: a.Node, ProducerTaskID: a.TaskID, SourcePlanVersion: a.PlanVersion, ToolVersion: a.ToolVersion, ByteLength: a.Size, Kind: a.Kind, ContentType: "application/json"})
		content[a.ID] = []byte(a.Content)
	}
	// This local value is only a scoring input. It is never stored, dispatched,
	// or accepted by strict ReadInput; original unverified envelopes are returned.
	local := inputcontext.Snapshot{Role: c.Role, Artifacts: refs}
	selected, e := inputcontext.Select(local, "Use the business rule and measured paid orders", x.arm.Context, 48000, 12000, content)
	if e != nil {
		return nil, e
	}
	if selected.Blocked {
		return nil, errors.New("candidate_context_budget")
	}
	selected.WireJSON = ""
	x.r.Selections = append(x.r.Selections, selected)
	admitted := map[string]bool{}
	for _, item := range selected.Admitted {
		admitted[item.ID] = true
	}
	out := []evalbaseline.Candidate{}
	for _, a := range all {
		if admitted[a.ID] {
			out = append(out, a)
		}
	}
	return out, nil
}
func (x *experiment) duplicate(ctx context.Context, f *fixture, run string, c store.OrchestratorClaim) error {
	if x == nil || x.c.Category != "reordered_duplicate" {
		return nil
	}
	pending, e := f.s.PendingHandoffs(ctx, run, handoff.SchedulerConsumer, 256)
	if e != nil {
		return e
	}
	for i := len(pending) - 1; i >= 0; i-- {
		for attempt := 0; attempt < 2; attempt++ {
			if _, e = f.s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, pending[i].ID); e != nil {
				return e
			}
			if attempt == 1 {
				x.r.DuplicateDeliveries++
			}
		}
	}
	return nil
}
func (x *experiment) replanStrict(ctx context.Context, f *fixture, run string, owner store.OrchestratorClaim, p planner.Proposal) (planner.Proposal, error) {
	p.BaseVersion = 2
	for i := range p.Nodes {
		if p.Nodes[i].Role == "data" {
			p.Nodes[i].Goal += "; recompute after observed input version change"
		}
	}
	request := mockRequest{RunID: run, TaskID: f.verification.claim.TaskID, PlanVersion: 2, Turn: 1, LogicalID: uuid.NewString(), CaseID: x.c.ID, Goal: "Propose bounded recomputation after rejected data evidence", Snapshot: string(inputcontext.Canonical(map[string]any{"failure_kind": "evidence_rejected", "failed_node": "data", "current_plan": p})), Tools: "[]"}
	requestBytes := inputcontext.Canonical(request)
	resources, e := scheduler.NewResources(ctx, f.s)
	if e != nil {
		return p, e
	}
	req, e := budget.PricedRequest(request.LogicalID, budget.Pricing{Version: "demo-free.v1", Provider: "mock", Model: "demo-mock.v1"}, int64((len(requestBytes)+3)/4), 16000, artifact.Digest(requestBytes))
	if e != nil {
		return p, e
	}
	// Invoke the same frozen mock adapter only after reservation. Its proposal
	// remains untrusted until the existing bounded Replan API accepts it.
	var decision []byte
	if x.model != nil {
		var payload map[string]any
		_ = json.Unmarshal(requestBytes, &payload)
		contract, contractErr := liveDispatch("planner", f.input.Business, f.input.Version, &p, nil)
		if contractErr != nil {
			return p, contractErr
		}
		payload["dispatch_contract"] = contract
		requestBytes = inputcontext.Canonical(payload)
		decision, _, e = x.model.Call(ctx, resources, f.verification.claim, request.LogicalID, requestBytes)
	} else {
		_, e = resources.Model(ctx, f.verification.claim, req, func(context.Context) (budget.ModelUsage, bool, error) {
			var err error
			decision, err = mockDecision("planner", request, &p, "none")
			in, out := int64((len(requestBytes)+3)/4), int64((len(decision)+3)/4)
			return budget.ModelUsage{Status: "succeeded", Known: true, Source: "local_stub", InputTokens: in, OutputTokens: out, Tokens: in + out, PricingVersion: "demo-free.v1"}, true, err
		})
	}
	if e != nil {
		return p, e
	}
	var decoded struct {
		Type   string           `json:"type"`
		Output planner.Proposal `json:"output"`
	}
	if verifier.StrictJSON(decision, &decoded) != nil || decoded.Type != "final" || planner.Validate(decoded.Output) != nil || string(inputcontext.Canonical(decoded.Output)) != string(inputcontext.Canonical(p)) {
		return p, errors.New("replan_model_output_invalid")
	}
	p = decoded.Output
	candidate := planner.ReplanSuggestion{SchemaVersion: "replan.v1", FailureKind: "evidence_rejected", FailedNode: "data", Proposal: p}
	raw := inputcontext.Canonical(candidate)
	configs := map[string]inputcontext.ExecutionConfig{}
	for _, n := range p.Nodes {
		configs[n.Key] = inputcontext.ExecutionConfig{ModelVersion: f.modelVersion, PolicyVersion: "demo.v1", OutputToolVersion: "demo.v1"}
	}
	result, e := f.s.Replan(ctx, run, owner, raw, configs, planner.ReplanPolicy{MaxReplans: 1, MaxDepth: 6, MaxCostMicros: x.c.Limits.Cost, CostPerTaskMicros: 1}, f.cas)
	x.r.Replans = append(x.r.Replans, result)
	if e == nil {
		x.r.Plans = append(x.r.Plans, p)
	}
	return p, e
}
func (x *experiment) finish(f *fixture, run string) error {
	if x == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	rows, e := f.s.Pool.Query(ctx, `SELECT jsonb_build_object('call_id',call_id,'logical_call_id',logical_call_id,'run_id',run_id,'task_id',task_id,'provider',provider,'model',model,'status',status,'usage',usage,'usage_known',usage_known,'usage_source',usage_source,'reservation',reservation,'settlement',settlement,'created_at',created_at) FROM model_calls WHERE run_id=$1 ORDER BY created_at,call_id`, run)
	if e != nil {
		return e
	}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		x.r.Models = append(x.r.Models, json.RawMessage(raw))
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	rows, e = f.s.Pool.Query(ctx, `SELECT jsonb_build_object('invocation_id',v.invocation_id,'run_id',t.run_id,'task_id',v.task_id,'capability',v.capability,'request_hash',v.request_hash,'effect',v.effect_class,'tool_version',v.scope->>'Version','plan_version',v.scope->'Plan','status',v.status,'result_artifact_id',v.result_artifact_id) FROM tool_invocations v JOIN task_instances t USING(task_id) WHERE t.run_id=$1 ORDER BY v.invocation_id`, run)
	if e != nil {
		return e
	}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		x.r.Tools = append(x.r.Tools, json.RawMessage(raw))
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	if x.arm.Verify {
		if e = f.s.Pool.QueryRow(ctx, "SELECT extract(epoch FROM (finished_at-created_at))*1000 FROM workflow_runs WHERE run_id=$1", run).Scan(&x.r.WorkflowMS); e != nil {
			return e
		}
	}
	// Independent post-run phase; its result is never returned to a Worker.
	started := time.Now()
	required := f.input.Business == "incident" && !x.c.Negative
	x.r.ExternalOracle = map[string]any{"version": "experiment-execution-oracle.v1", "required": required, "passed": !required, "sandbox_executions": 0}
	if required {
		digest, e := verifier.SourceDigest(filepath.Join(f.temp, "worktree"))
		if e != nil {
			return e
		}
		replay := verifier.Reexecutor{Tests: map[string]verifier.TestInput{"external-incident.v1": {Directory: filepath.Join(f.temp, "worktree"), Digest: digest, Toolchain: "go version go1.27.3 linux/amd64", Package: "example.invalid/mgar/incident", Tests: []string{"TestPaidDeduplicatedTotal"}}}, Runner: f.sandbox}
		observed, e := replay.Replay(ctx, verifier.Recipe{Method: "go_test", Command: "external-incident.v1", InputDigest: digest})
		x.r.ExternalOracle["source_sha256"] = digest
		x.r.ExternalOracle["observation"] = observed
		x.r.ExternalOracle["sandbox_executions"] = 1
		x.r.ExternalOracle["passed"] = e == nil && observed.ExitCode != nil && *observed.ExitCode == 0 && observed.Tests != nil && *observed.Tests > 0
		if e != nil {
			x.r.ExternalOracle["error"] = "independent_reexecution_failed"
		}
	}
	x.r.ExternalOracle["elapsed_ms"] = float64(time.Since(started).Microseconds()) / 1000
	return nil
}
