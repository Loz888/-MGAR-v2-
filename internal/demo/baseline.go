package demo

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/evalbaseline"
	"github.com/Loz888/-MGAR-v2-/internal/observability"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/google/uuid"
)

type BaselineReport struct {
	Schema            string                   `json:"schema_version"`
	Case              string                   `json:"case_id"`
	RunID             string                   `json:"run_id"`
	Provider          string                   `json:"provider"`
	ExecutionStatus   string                   `json:"execution_status"`
	WorkflowState     string                   `json:"workflow_state"`
	WorkflowSuccess   *bool                    `json:"workflow_success"`
	SemanticStatus    string                   `json:"semantic_status"`
	Namespace         string                   `json:"database_namespace"`
	CASNamespace      string                   `json:"artifact_namespace"`
	Answer            map[string]any           `json:"answer"`
	Candidates        []evalbaseline.Candidate `json:"candidates"`
	Workers           []WorkerResult           `json:"workers"`
	Errors            []string                 `json:"errors"`
	Budget            budget.Report            `json:"budget"`
	Audit             observability.Audit      `json:"audit"`
	Trace             json.RawMessage          `json:"otlp_trace"`
	TraceState        string                   `json:"trace_state"`
	VerdictCount      int                      `json:"verdict_count"`
	Parallel          int                      `json:"parallel_workers"`
	Tools             int                      `json:"tools"`
	SandboxExecutions int                      `json:"sandbox_executions"`
	SourceUnchanged   bool                     `json:"source_unchanged"`
}

// RunBaseline is the approved isolated reference path, not a permissive flag on
// Run. Carrier tasks enforce identity/quota but never receive a passed verdict.
func RunBaseline(ctx context.Context, o Options) (report BaselineReport, err error) {
	report = BaselineReport{Schema: "baseline_execution.v1", Case: o.Case, RunID: uuid.NewString(), Provider: "mock", ExecutionStatus: "failed", SemanticStatus: "unverified", Candidates: []evalbaseline.Candidate{}, Workers: []WorkerResult{}, Errors: []string{}, Answer: map[string]any{}}
	switch o.Fault {
	case "none", "wrong-data", "zero-tools", "forge-action", "forge-aggregate", "wrong-patch":
	default:
		report.Errors = append(report.Errors, "unknown_fault")
		return report, errors.New("unknown_fault")
	}
	f, e := newFixture(ctx, o)
	if e != nil {
		report.Errors = append(report.Errors, "fixture_initialization_failed")
		return report, e
	}
	defer func() { err = errors.Join(err, f.close()) }()
	report.Namespace = f.schema
	report.CASNamespace = artifact.Digest([]byte(f.temp))
	_, owner, e := f.s.CreateRunClaimed(ctx, report.RunID, f.input.Goal, 32, "baseline-controller", 5*time.Minute)
	if e != nil {
		return report, e
	}
	counts := &processCounts{}
	defer func() {
		if err != nil {
			report.ExecutionStatus = "failed"
			report.Errors = append(report.Errors, "baseline_control_error")
		}
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		w, e := f.s.Workflow(finish, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		if w.State == "running" || w.State == "planning" {
			w, e = f.s.InterruptWorkflow(finish, report.RunID, owner, "interrupted", "isolated_baseline_finished_without_semantic_verdict")
			if e != nil {
				err = errors.Join(err, e)
				return
			}
		}
		report.WorkflowState = w.State
		report.Parallel = counts.peak
		report.SourceUnchanged = f.unchanged()
		report.Budget, e = f.s.BudgetReport(finish, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		report.Audit, e = observability.ReadAudit(finish, f.s, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		e = f.s.Pool.QueryRow(finish, "SELECT count(*) FROM artifact_verdicts v JOIN artifacts a USING(artifact_id) WHERE a.run_id=$1", report.RunID).Scan(&report.VerdictCount)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		report.Trace, report.TraceState, e = collectTrace(finish, report.Audit)
		err = errors.Join(err, e)
	}()
	if _, e = f.s.BeginPlanning(ctx, report.RunID, owner); e != nil {
		return report, e
	}
	limits := budget.Limits{ModelCalls: 64, ToolCalls: 64, Tokens: 65536, CostMicros: 1_000_000, ConcurrentCalls: 4, TaskConcurrency: 4, Delegations: 32}
	if o.Fault == "zero-tools" {
		limits.ToolCalls = 0
	}
	if e = f.s.ConfigureBudget(ctx, report.RunID, owner, limits); e != nil {
		return report, e
	}
	if f.input.Evidence != "complete" || f.input.Scope == "production_delete" {
		reason := "insufficient_evidence"
		if f.input.Scope == "production_delete" {
			reason = "policy_denied"
		}
		report.ExecutionStatus = "blocked"
		report.Errors = append(report.Errors, reason)
		report.Answer = map[string]any{"decision": "refuse", "metrics": nil, "reason": reason, "input_version": f.input.Version}
		_, e = f.s.InterruptWorkflow(ctx, report.RunID, owner, "blocked", reason)
		return report, e
	}
	carrier := proposal(f.input.Business)
	carrier.BaseVersion = 0
	carrier.Edges = []planner.Edge{}
	carrier.Rationale = "isolated experimental admission carriers.v1; business ordering is outside strict dependency/verification semantics"
	nodes := []planner.Node{}
	for _, n := range carrier.Nodes {
		if n.Role != "verify" {
			n.RequiredInputs = []string{}
			nodes = append(nodes, n)
		}
	}
	carrier.Nodes = nodes
	if _, e = f.s.ProposeAndCommit(ctx, report.RunID, owner, carrier); e != nil {
		return report, e
	}
	broker, e := evalbaseline.New(ctx, f.s, f.cas, report.RunID, 1)
	if e != nil {
		return report, e
	}
	if e = f.baselineBatch(ctx, o, &report, owner, broker, nodes[:2], counts); e != nil {
		report.Errors = append(report.Errors, e.Error())
		return report, nil
	}
	for _, n := range nodes[2:] {
		if e = f.baselineBatch(ctx, o, &report, owner, broker, []planner.Node{n}, counts); e != nil {
			report.Errors = append(report.Errors, e.Error())
			return report, nil
		}
	}
	last := report.Workers[len(report.Workers)-1]
	var result struct {
		Answer         map[string]any           `json:"answer"`
		Citations      []evalbaseline.Candidate `json:"citations"`
		SemanticStatus string                   `json:"semantic_status"`
	}
	if json.Unmarshal(last.Output, &result) != nil || result.SemanticStatus != "unverified" || result.Answer == nil {
		report.Errors = append(report.Errors, "invalid_candidate_answer")
		return report, nil
	}
	report.Answer = result.Answer
	report.ExecutionStatus = "completed"
	return report, nil
}
func (f *fixture) baselineBatch(ctx context.Context, o Options, r *BaselineReport, owner store.OrchestratorClaim, broker *evalbaseline.Broker, nodes []planner.Node, counts *processCounts) error {
	if _, e := f.s.RefreshReady(ctx, r.RunID, owner); e != nil {
		return e
	}
	type job struct {
		c      store.TaskClaim
		snap   inputcontext.Snapshot
		state  *taskTools
		node   planner.Node
		defs   []policy.Definition
		result WorkerResult
		err    error
	}
	jobs := make([]job, len(nodes))
	for i, n := range nodes {
		c, e := (scheduler.Scheduler{Store: f.s}).ClaimScheduled(ctx, r.RunID, store.WorkerIdentity{Owner: "baseline-" + n.Key, Role: n.Role}, 3*time.Minute, "critical_path")
		if e != nil {
			return e
		}
		if c.NodeKey != n.Key {
			return errors.New("baseline_scheduled_node_mismatch")
		}
		snap, e := f.s.BuildSnapshot(ctx, c, "demo-mock.v1", "demo.v1", f.cas)
		if e != nil {
			return e
		}
		c, e = f.s.StartTask(ctx, c)
		if e != nil {
			return e
		}
		state := &taskTools{}
		defs := f.definitions(c, snap, state)
		if c.Role == "action" || c.Role == "aggregate" {
			handle, e := broker.Grant(ctx, c, r.Candidates)
			if e != nil {
				return e
			}
			read := policy.Definition{Name: "candidate.read", Version: "demo.v1", Role: c.Role, Effect: "readonly", Resources: []string{"fixture"}, Parameters: map[string]policy.Parameter{}, Timeout: 30 * time.Second, MaxOutput: evalbaseline.MaxContextBytes, Execute: func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
				raw, e := broker.Read(ctx, c, handle)
				if e == nil {
					state.mu.Lock()
					state.executed++
					state.mu.Unlock()
				}
				return raw, e
			}}
			if c.Role == "aggregate" {
				defs = []policy.Definition{read}
			} else {
				defs = append(defs, read)
			}
		}
		jobs[i] = job{c: c, snap: snap, state: state, node: n, defs: defs}
	}
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			j.result, j.err = f.workerWith(ctx, o, j.c, j.snap, j.state, nil, counts, &workerAdapter{mode: "full", definitions: j.defs, decide: baselineDecision(f.input.Version)})
		}(&jobs[i])
	}
	wg.Wait()
	var failed error
	for _, j := range jobs {
		r.Workers = append(r.Workers, j.result)
		r.Tools += j.state.executed
		r.SandboxExecutions += j.state.sandboxCalls
		if j.err != nil {
			failed = errors.Join(failed, errors.New(j.node.Key+": "+j.err.Error()))
			continue
		}
		a, e := f.s.PublishArtifact(ctx, j.c, f.cas, j.node.OutputKind, "application/json", "demo.v1", inputcontext.Canonical(j.result.Output))
		if e != nil {
			failed = errors.Join(failed, e)
			continue
		}
		candidate, e := broker.Publish(ctx, j.c, a)
		if e != nil {
			failed = errors.Join(failed, e)
			continue
		}
		r.Candidates = append(r.Candidates, candidate)
	}
	// No ApplyVerdicts or FinalizeWorkflow here. This path cannot elevate results.
	return failed
}
