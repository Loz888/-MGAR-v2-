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
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/observability"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/google/uuid"
)

type ArtifactResult struct {
	Metadata artifact.Metadata `json:"metadata"`
	Body     string            `json:"body"`
	Evidence verifier.Evidence `json:"evidence"`
	Verdict  verifier.Verdict  `json:"verdict"`
}
type Report struct {
	Case              string              `json:"case_id"`
	RunID             string              `json:"run_id"`
	Provider          string              `json:"provider"`
	State             string              `json:"state"`
	PlanVersion       int                 `json:"plan_version"`
	ErrorKind         string              `json:"error_kind,omitempty"`
	Answer            map[string]any      `json:"answer"`
	Merge             handoff.MergeResult `json:"merge"`
	Workers           []WorkerResult      `json:"workers"`
	Artifacts         []ArtifactResult    `json:"artifacts"`
	Parallel          int                 `json:"parallel_workers"`
	Tools             int                 `json:"tools"`
	SandboxExecutions int                 `json:"sandbox_executions"`
	SourceUnchanged   bool                `json:"source_unchanged"`
	Audit             observability.Audit `json:"audit"`
	Budget            budget.Report       `json:"budget"`
	Trace             json.RawMessage     `json:"otlp_trace"`
	TraceState        string              `json:"trace_state"`
}

func proposal(business string) planner.Proposal {
	nodes := []planner.Node{{Key: "research", Role: "research", Goal: "Read the bounded business rule and cite its source", RequiredInputs: []string{}, OutputKind: "finding"}, {Key: "data", Role: "data", Goal: "Measure paid totals using the authorized fixed query", RequiredInputs: []string{}, OutputKind: "sql_evidence"}}
	nodes = append(nodes, planner.Node{Key: "verify-runtime", Role: "verify", Goal: "Independently replay fixed observations under tool and resource admission", RequiredInputs: []string{}, OutputKind: "verdict"})
	inputs := []string{"research", "data", "verify-runtime"}
	edges := []planner.Edge{}
	if business == "incident" {
		nodes = append(nodes, planner.Node{Key: "action", Role: "action", Goal: "Choose a reviewed sandbox patch using upstream evidence and execute tests", RequiredInputs: []string{"research", "data"}, OutputKind: "patch"})
		edges = append(edges, planner.Edge{From: "research", To: "action"}, planner.Edge{From: "data", To: "action"})
		inputs = append(inputs, "action")
	}
	nodes = append(nodes, planner.Node{Key: "aggregate", Role: "aggregate", Goal: "Report verified facts and exact original citations", RequiredInputs: inputs, OutputKind: "report"})
	for _, input := range inputs {
		edges = append(edges, planner.Edge{From: input, To: "aggregate"})
	}
	return planner.Proposal{SchemaVersion: "plan.v1", BaseVersion: 1, Nodes: nodes, Edges: edges, Rationale: "Choose a bounded business-specific DAG from the submitted goal; analysis needs no write capability"}
}
func Run(ctx context.Context, o Options) (report Report, err error) {
	return runFull(ctx, o, nil)
}
func runFull(ctx context.Context, o Options, x *experiment) (report Report, err error) {
	switch o.Fault {
	case "none", "forge-planner", "forge-research", "forge-data", "forge-action", "forge-aggregate", "wrong-patch":
	default:
		return report, errors.New("unknown_fault")
	}
	f, e := newFixture(ctx, o)
	if e != nil {
		return report, e
	}
	defer func() { err = errors.Join(err, f.close()) }()
	if x != nil {
		if e = x.prepare(ctx, f); e != nil {
			return report, e
		}
		defer func() { err = errors.Join(err, x.finish(f, report.RunID)) }()
	}
	report = Report{Case: o.Case, RunID: uuid.NewString(), Provider: "mock", Workers: []WorkerResult{}, Artifacts: []ArtifactResult{}, Answer: map[string]any{}}
	if o.Model != nil {
		report.Provider = "deepseek"
	}
	_, owner, e := f.s.CreateRunClaimed(ctx, report.RunID, f.input.Goal, 32, "demo-controller", 5*time.Minute)
	if e != nil {
		return report, e
	}
	counts := &processCounts{}
	defer func() {
		if f.verification != nil {
			report.Tools += f.verification.calls
		}
		report.SourceUnchanged = f.unchanged()
		report.Parallel = counts.peak
		w, e := f.s.Workflow(ctx, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		report.State = w.State
		report.PlanVersion = w.ActivePlanVersion
		report.Budget, e = f.s.BudgetReport(ctx, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		report.Audit, e = observability.ReadAudit(ctx, f.s, report.RunID)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		report.Trace, report.TraceState, e = collectTrace(ctx, report.Audit)
		err = errors.Join(err, e)
	}()
	if _, e = f.s.BeginPlanning(ctx, report.RunID, owner); e != nil {
		return report, e
	}
	limits := budget.Limits{ModelCalls: 64, ToolCalls: 64, Tokens: 65536, CostMicros: 1_000_000, ConcurrentCalls: 4, TaskConcurrency: 4, Delegations: 32}
	if x != nil {
		limits = x.limits()
	}
	if e = f.s.ConfigureBudget(ctx, report.RunID, owner, limits); e != nil {
		return report, e
	}
	if f.input.Evidence != "complete" || f.input.Scope == "production_delete" {
		reason := "insufficient_evidence"
		if f.input.Scope == "production_delete" {
			reason = "policy_denied"
		}
		report.ErrorKind = reason
		report.Answer = map[string]any{"decision": "refuse", "metrics": nil, "reason": reason, "input_version": f.input.Version}
		_, e = f.s.InterruptWorkflow(ctx, report.RunID, owner, "blocked", reason)
		return report, e
	}
	initial := planner.Proposal{SchemaVersion: "plan.v1", Nodes: []planner.Node{{Key: "planner", Role: "research", Goal: "Propose a bounded business plan", RequiredInputs: []string{}, OutputKind: "finding"}}, Edges: []planner.Edge{}, Rationale: "Account for the planner mock call under a read-only bootstrap task before committing its proposal"}
	if _, e = f.s.ProposeAndCommit(ctx, report.RunID, owner, initial); e != nil {
		return report, e
	}
	p := proposal(f.input.Business)
	if e = f.batchWith(ctx, o, &report, owner, []planner.Node{initial.Nodes[0]}, &p, counts, x); e != nil {
		return f.stop(ctx, report, owner, e)
	}
	candidate, e := planner.Decode(report.Workers[0].Output)
	if e != nil || string(inputcontext.Canonical(candidate)) != string(inputcontext.Canonical(p)) {
		return f.stop(ctx, report, owner, errors.New("planner_rejected"))
	}
	if _, e = f.s.ProposeAndCommit(ctx, report.RunID, owner, candidate); e != nil {
		return report, e
	}
	if x != nil {
		x.r.Plans = append(x.r.Plans, candidate)
	}
	if e = f.startVerification(ctx, report.RunID, owner); e != nil {
		return report, e
	}
	if e = f.batchWith(ctx, o, &report, owner, p.Nodes[:2], nil, counts, x); e != nil {
		if x == nil || !x.arm.Replan || !x.refreshed {
			return f.stop(ctx, report, owner, e)
		}
		p, e = x.replanStrict(ctx, f, report.RunID, owner, p)
		if e != nil {
			return f.stop(ctx, report, owner, e)
		}
		report.Tools += f.verification.calls
		if e = f.startVerification(ctx, report.RunID, owner); e != nil {
			return report, e
		}
		if e = f.batchWith(ctx, o, &report, owner, p.Nodes[1:2], nil, counts, x); e != nil {
			return f.stop(ctx, report, owner, e)
		}
	}
	for _, n := range p.Nodes[2:] {
		if n.Key == "verify-runtime" {
			continue
		}
		if n.Role == "aggregate" {
			if e = f.finishVerification(ctx, &report, owner); e != nil {
				return f.stop(ctx, report, owner, e)
			}
		}
		if e = f.batchWith(ctx, o, &report, owner, []planner.Node{n}, nil, counts, x); e != nil {
			return f.stop(ctx, report, owner, e)
		}
	}
	for i := 0; i < 2; i++ {
		if _, e = f.s.FinalizeWorkflow(ctx, report.RunID, owner); e != nil {
			return report, e
		}
	}
	metrics := map[string]any{}
	for _, fact := range report.Merge.Facts {
		if fact.Key == "paid_total" || fact.Key == "paid_orders" || fact.Key == "duplicate_paid_events" {
			var value any
			if e = json.Unmarshal(fact.Value, &value); e != nil {
				return report, e
			}
			metrics[fact.Key] = value
		}
	}
	report.Answer = map[string]any{"decision": "answer", "metrics": metrics, "reason": nil, "input_version": f.input.Version}
	return report, nil
}
func (f *fixture) stop(ctx context.Context, r Report, c store.OrchestratorClaim, cause error) (Report, error) {
	r.ErrorKind = cause.Error()
	w, e := f.s.FinalizeWorkflow(ctx, r.RunID, c)
	if e == nil && w.State != "failed" {
		_, e = f.s.InterruptWorkflow(ctx, r.RunID, c, "interrupted", "demo_execution_not_confirmed")
	}
	return r, e
}
func (f *fixture) deliver(ctx context.Context, run string, c store.OrchestratorClaim) error {
	d := handoff.Dispatcher{Outbox: f.s, Consumer: handoff.SchedulerConsumer, Deliver: func(ctx context.Context, event handoff.Event) error {
		_, e := f.s.ConsumeHandoff(ctx, run, c, handoff.SchedulerConsumer, event.ID)
		return e
	}}
	for i := 0; i < 8; i++ {
		n, e := d.Dispatch(ctx, run, 256)
		if e != nil {
			return e
		}
		if n == 0 {
			return nil
		}
	}
	return errors.New("handoff_bound")
}
func (f *fixture) batch(ctx context.Context, o Options, r *Report, owner store.OrchestratorClaim, nodes []planner.Node, p *planner.Proposal, counts *processCounts) error {
	return f.batchWith(ctx, o, r, owner, nodes, p, counts, nil)
}
func (f *fixture) batchWith(ctx context.Context, o Options, r *Report, owner store.OrchestratorClaim, nodes []planner.Node, p *planner.Proposal, counts *processCounts, x *experiment) error {
	if e := x.duplicate(ctx, f, r.RunID, owner); e != nil {
		return e
	}
	if e := f.deliver(ctx, r.RunID, owner); e != nil {
		return e
	}
	if _, e := f.s.RefreshReady(ctx, r.RunID, owner); e != nil {
		return e
	}
	type work struct {
		claim  store.TaskClaim
		snap   inputcontext.Snapshot
		state  *taskTools
		result WorkerResult
		err    error
		node   planner.Node
	}
	jobs := make([]work, len(nodes))
	for i, n := range nodes {
		c, e := (scheduler.Scheduler{Store: f.s}).ClaimScheduled(ctx, r.RunID, store.WorkerIdentity{Owner: "demo-" + n.Key, Role: n.Role}, 3*time.Minute, "critical_path")
		if e != nil {
			return e
		}
		if c.NodeKey != n.Key {
			return errors.New("unexpected_scheduled_node")
		}
		snap, e := f.s.BuildSnapshot(ctx, c, f.modelVersion, "demo.v1", f.cas)
		if e != nil {
			return e
		}
		c, e = f.s.StartTask(ctx, c)
		if e != nil {
			return e
		}
		state := &taskTools{}
		if p != nil {
			state.expected = inputcontext.Canonical(p)
		}
		jobs[i] = work{claim: c, snap: snap, state: state, node: n}
	}
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j *work) {
			defer wg.Done()
			var adapter *workerAdapter
			if x != nil {
				adapter = &workerAdapter{mode: x.arm.Context, definitions: x.definitions(f, j.claim, j.snap, j.state, false), decide: mockDecision}
			}
			j.result, j.err = f.workerWith(ctx, o, j.claim, j.snap, j.state, p, counts, adapter)
		}(&jobs[i])
	}
	wg.Wait()
	if e := x.afterMeasurements(ctx, f, nodes); e != nil {
		return e
	}
	var failed error
	for _, j := range jobs {
		r.Workers = append(r.Workers, j.result)
		r.Tools += j.state.executed
		r.SandboxExecutions += j.state.sandboxCalls
		if j.err != nil {
			failed = errors.Join(failed, j.err)
			continue
		}
		body := inputcontext.Canonical(j.result.Output)
		a, e := f.s.PublishArtifact(ctx, j.claim, f.cas, j.node.OutputKind, "application/json", "demo.v1", body)
		if e != nil {
			return e
		}
		req := verifier.Requirement{Kind: "file_hash", ToolVersion: "demo.v1", Recipe: verifier.Recipe{Method: "cas_hash"}, Expected: verifier.Observation{Hash: artifact.Digest(j.state.expected)}}
		observed := verifier.Observation{Hash: a.Hash}
		engine := verifier.Engine{}
		if string(body) == string(j.state.expected) && (j.node.Role == "data" || j.node.Role == "action") {
			req.Kind = "sql_result"
			if j.node.Role == "action" {
				req.Kind = "test_run"
				r.SandboxExecutions++
			}
			req.Recipe = j.state.recipe
			req.Expected = j.state.observation
			observed = j.state.observation
			if f.verification == nil {
				return errors.New("verification_task_required")
			}
			engine.Replayer = admittedReplay{f: f, inner: j.state.replayer}
		}
		evidence := verifier.Evidence{Schema: "evidence.v1", Claim: "Reproduce the actual fixed authorized observation", Kind: req.Kind, ArtifactID: a.ID, Hash: a.Hash, RunID: a.RunID, TaskID: a.ProducerTaskID, PlanVersion: a.SourcePlanVersion, SnapshotID: j.snap.ID, ToolVersion: "demo.v1", ExecutionStatus: "succeeded", Recipe: req.Recipe, Observed: observed, Expected: req.Expected}
		verdict, e := f.s.VerifyArtifact(ctx, r.RunID, owner, a.ID, inputcontext.Canonical(evidence), req, f.cas, engine)
		if e != nil {
			return e
		}
		r.Artifacts = append(r.Artifacts, ArtifactResult{a, string(body), evidence, verdict})
		if verdict.Status != "passed" {
			failed = errors.Join(failed, errors.New("verification_failed_"+j.node.Key))
		}
		if j.node.Role == "aggregate" && verdict.Status == "passed" {
			if e = json.Unmarshal(body, &r.Merge); e != nil {
				return e
			}
		}
	}
	if e := f.s.ApplyVerdicts(ctx, r.RunID, owner); e != nil {
		return e
	}
	if e := f.deliver(ctx, r.RunID, owner); e != nil {
		return e
	}
	return failed
}

type verificationTask struct {
	claim    store.TaskClaim
	snapshot inputcontext.Snapshot
	calls    int
}

func (f *fixture) startVerification(ctx context.Context, run string, owner store.OrchestratorClaim) error {
	if _, e := f.s.RefreshReady(ctx, run, owner); e != nil {
		return e
	}
	c, e := (scheduler.Scheduler{Store: f.s}).ClaimScheduled(ctx, run, store.WorkerIdentity{Owner: "independent-verifier", Role: "verify"}, 3*time.Minute, "critical_path")
	if e != nil {
		return e
	}
	snap, e := f.s.BuildSnapshot(ctx, c, f.modelVersion, "demo.v1", f.cas)
	if e != nil {
		return e
	}
	c, e = f.s.StartTask(ctx, c)
	if e != nil {
		return e
	}
	f.verification = &verificationTask{claim: c, snapshot: snap}
	return nil
}
func (f *fixture) finishVerification(ctx context.Context, r *Report, owner store.OrchestratorClaim) error {
	v := f.verification
	if v == nil || v.calls < 1 {
		return errors.New("independent_replay_missing")
	}
	body := facts(fact("verification.replayed_tools", v.calls))
	a, e := f.s.PublishArtifact(ctx, v.claim, f.cas, "verdict", "application/json", "demo.v1", body)
	if e != nil {
		return e
	}
	req := verifier.Requirement{Kind: "file_hash", ToolVersion: "demo.v1", Recipe: verifier.Recipe{Method: "cas_hash"}, Expected: verifier.Observation{Hash: artifact.Digest(body)}}
	proof := verifier.Evidence{Schema: "evidence.v1", Claim: "Accounted independent replay receipt", Kind: req.Kind, ArtifactID: a.ID, Hash: a.Hash, RunID: a.RunID, TaskID: a.ProducerTaskID, PlanVersion: a.SourcePlanVersion, SnapshotID: v.snapshot.ID, ToolVersion: "demo.v1", ExecutionStatus: "succeeded", Recipe: req.Recipe, Observed: req.Expected, Expected: req.Expected}
	result, e := f.s.VerifyArtifact(ctx, r.RunID, owner, a.ID, inputcontext.Canonical(proof), req, f.cas, verifier.Engine{})
	if e != nil {
		return e
	}
	r.Artifacts = append(r.Artifacts, ArtifactResult{a, string(body), proof, result})
	if result.Status != "passed" {
		return errors.New("verification_receipt_failed")
	}
	return f.s.ApplyVerdicts(ctx, r.RunID, owner)
}
