package demo

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

type WorkerResult struct {
	PID       int             `json:"pid"`
	Role      string          `json:"role"`
	Status    string          `json:"status"`
	Verified  bool            `json:"verified"`
	Output    json.RawMessage `json:"output"`
	ErrorKind *string         `json:"error_kind"`
	Turns     int             `json:"turns"`
	ToolCalls int             `json:"tool_calls"`
	Started   time.Time       `json:"started_at"`
	Ended     time.Time       `json:"ended_at"`
}
type mockObservation struct {
	Turn   int    `json:"turn"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output_json"`
	Hash   string `json:"output_sha256"`
}
type mockRequest struct {
	RunID        string            `json:"run_id"`
	TaskID       string            `json:"task_id"`
	PlanVersion  int               `json:"plan_version"`
	Turn         int               `json:"turn"`
	LogicalID    string            `json:"logical_call_id"`
	CaseID       string            `json:"case_id"`
	Goal         string            `json:"goal"`
	Snapshot     string            `json:"snapshot_json"`
	Observations []mockObservation `json:"observations"`
	Tools        string            `json:"tools_json"`
}
type processCounts struct {
	sync.Mutex
	active, peak int
}

func (p *processCounts) add(delta int) {
	p.Lock()
	defer p.Unlock()
	p.active += delta
	if p.active > p.peak {
		p.peak = p.active
	}
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("worker_output_limit")
	}
	return b.Buffer.Write(p)
}

type workerAdapter struct {
	mode        string
	definitions []policy.Definition
	decide      func(string, mockRequest, *planner.Proposal, string) ([]byte, error)
}

func (f *fixture) worker(ctx context.Context, o Options, c store.TaskClaim, snap inputcontext.Snapshot, state *taskTools, proposal *planner.Proposal, counts *processCounts) (out WorkerResult, err error) {
	return f.workerWith(ctx, o, c, snap, state, proposal, counts, nil)
}
func (f *fixture) workerWith(ctx context.Context, o Options, c store.TaskClaim, snap inputcontext.Snapshot, state *taskTools, proposal *planner.Proposal, counts *processCounts, adapter *workerAdapter) (out WorkerResult, err error) {
	out = WorkerResult{Role: c.NodeKey}
	mode := "selected"
	decide := mockDecision
	if adapter != nil {
		mode = adapter.mode
		decide = adapter.decide
	}
	maxBytes, maxTokens := 60000, 15000
	if adapter != nil {
		maxBytes, maxTokens = 48000, 12000
	}
	selected, e := f.s.PrepareContext(ctx, c, f.cas, snap.ID, f.input.Goal, mode, maxBytes, maxTokens)
	if e != nil || selected.Blocked {
		return out, errors.New("context_selection_blocked")
	}
	defs := f.definitions(c, snap, state)
	if adapter != nil {
		defs = adapter.definitions
	}
	gateway, e := policy.NewGateway(f.s, f.cas, defs, func(policy.Denial) {})
	if e != nil {
		return out, e
	}
	token, e := gateway.Issue(ctx, c, []string{"fixture"}, 2*time.Minute)
	if e != nil {
		return out, e
	}
	resources, e := scheduler.NewResources(ctx, f.s)
	if e != nil {
		return out, e
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/tools", gateway)
	var lock sync.Mutex
	next := 1
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		deny := func() { writeModelFailure(w, store.ErrPolicy, budget.ModelCall{}) }
		raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		request, parseErr := parseModelRequest(r, raw, token, c, o.Case, f.input.Goal, selected.WireJSON, next)
		if err != nil || parseErr != nil {
			deny()
			return
		}
		if err = f.s.ValidateToolClaim(r.Context(), c); err != nil {
			deny()
			return
		}
		if o.Model != nil {
			// Controller-owned dispatch contract, never supplied as authority by a Worker.
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			contract, contractErr := liveDispatch(c.NodeKey, f.input.Business, f.input.Version, proposal, defs)
			if contractErr != nil {
				deny()
				return
			}
			payload["dispatch_contract"] = contract
			decision, call, err := o.Model.Call(r.Context(), resources, c, request.LogicalID, inputcontext.Canonical(payload))
			if err != nil {
				writeModelFailure(w, err, call)
				return
			}
			next++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(decision)
			return
		}
		quote := budget.Pricing{Version: "demo-free.v1", Provider: "mock", Model: "demo-mock.v1"}
		input := int64((len(raw) + 3) / 4)
		req, err := budget.PricedRequest(request.LogicalID, quote, input, 16000, artifact.Digest(raw))
		if err != nil {
			writeModelFailure(w, err, budget.ModelCall{})
			return
		}
		var decision []byte
		call, err := resources.Model(r.Context(), c, req, func(context.Context) (budget.ModelUsage, bool, error) {
			decision, err = decide(c.NodeKey, request, proposal, o.Fault)
			if err != nil {
				return budget.ModelUsage{}, true, err
			}
			output := int64((len(decision) + 3) / 4)
			return budget.ModelUsage{Status: "succeeded", Known: true, Source: "local_stub", InputTokens: input, OutputTokens: output, Tokens: input + output, PricingVersion: quote.Version}, true, nil
		})
		if err != nil {
			writeModelFailure(w, err, call)
			return
		}
		next++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(decision)
	})
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return out, e
	}
	httpContext, stopHTTP := context.WithCancel(ctx)
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return httpContext }, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() {
		stopHTTP()
		drain, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if e := server.Shutdown(drain); e != nil {
			err = errors.Join(err, errors.New("worker_transport_not_drained"))
		}
		_ = server.Close()
		<-done
	}()
	declarations := []map[string]any{}
	if proposal == nil {
		for _, d := range defs {
			properties := map[string]any{}
			required := []string{}
			for name, p := range d.Parameters {
				prop := map[string]any{"type": p.Kind}
				if p.Kind == "string" {
					prop["enum"] = p.Enum
				}
				properties[name] = prop
				required = append(required, name)
			}
			declarations = append(declarations, map[string]any{"name": d.Name, "effect": d.Effect, "schema": map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}})
		}
	}
	job := map[string]any{"claim": c, "endpoint": "http://" + listener.Addr().String(), "token": token, "tools": declarations, "goal": f.input.Goal, "case_id": o.Case, "snapshot": selected.WireJSON}
	encoded := inputcontext.Canonical(job)
	if len(encoded) > 65536 {
		return out, errors.New("dispatch_limit")
	}
	workerCtx, cancel := context.WithTimeout(ctx, 95*time.Second)
	defer cancel()
	cmd := exec.CommandContext(workerCtx, filepath.Join(f.root, ".venv/bin/python"), "-m", "mgar_agents.runtime_worker")
	cmd.Dir = f.temp
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PYTHONPATH=" + filepath.Join(f.root, "agentkit"), "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1", "HOME=" + f.temp, "TMPDIR=" + f.temp}
	cmd.Stdin = bytes.NewReader(encoded)
	stdout := &limitedBuffer{max: 1 << 20}
	stderr := &limitedBuffer{max: 8192}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	out.Started = time.Now().UTC()
	if e = cmd.Start(); e != nil {
		return out, errors.New("worker_start_failed")
	}
	pid := cmd.Process.Pid
	counts.add(1)
	e = cmd.Wait()
	counts.add(-1)
	out.Ended = time.Now().UTC()
	if e != nil {
		return out, errors.New("worker_process_failed")
	}
	// SDK output contains audit fields; only the bounded result subset is consumed.
	if json.Unmarshal(stdout.Bytes(), &out) != nil || out.PID != pid || out.Verified {
		return out, errors.New("invalid_worker_result")
	}
	out.Role = c.NodeKey
	if out.Status != "completed" {
		return out, errors.New("worker_did_not_complete")
	}
	return out, nil
}
func mockDecision(role string, r mockRequest, p *planner.Proposal, fault string) ([]byte, error) {
	final := func(v any) []byte { return inputcontext.Canonical(map[string]any{"type": "final", "output": v}) }
	if p != nil {
		if fault == "forge-planner" {
			return final(map[string]any{"schema_version": "plan.v1", "nodes": []any{}}), nil
		}
		return final(p), nil
	}
	tool := func(name string, args map[string]any) []byte {
		return inputcontext.Canonical(map[string]any{"type": "tool_call", "call_id": "turn_" + string(rune('0'+r.Turn)), "name": name, "arguments": args})
	}
	if len(r.Observations) == 0 {
		switch role {
		case "research":
			return tool("research.read", map[string]any{}), nil
		case "data":
			return tool("data.query", map[string]any{"query": "paid-metrics"}), nil
		case "action":
			// Evidence must actually be present in the selected snapshot before choosing a patch.
			if !actionEvidence(r.Snapshot) {
				return nil, errors.New("action_missing_inputs")
			}
			patch := "deduplicate-paid"
			if fault == "wrong-patch" {
				patch = "deduplicate-only"
			}
			return tool("action.patch", map[string]any{"patch": patch}), nil
		case "aggregate":
			return tool("aggregate.merge", map[string]any{}), nil
		}
	}
	if role == "action" && len(r.Observations) == 1 {
		return tool("action.test", map[string]any{}), nil
	}
	if fault == "forge-"+role {
		return final(map[string]any{"fabricated": true}), nil
	}
	if len(r.Observations) == 0 {
		return nil, errors.New("missing_observation")
	}
	var result map[string]any
	if verifier.StrictJSON([]byte(r.Observations[len(r.Observations)-1].Output), &result) != nil {
		return nil, errors.New("invalid_observation")
	}
	return final(result), nil
}

// The mock chooses its reviewed patch from actual selected source text and SQL
// observations. Admission/verification comes from the server snapshot guard;
// the selection envelope is content, not an authority flag. Lossy inputs fail.
func actionEvidence(wire string) bool {
	var snapshot inputcontext.Snapshot
	if json.Unmarshal([]byte(wire), &snapshot) != nil || snapshot.Selection == nil {
		return false
	}
	rule, metrics := false, false
	for _, item := range snapshot.Selection.Items {
		if !item.Original || item.Lossy {
			continue
		}
		claims, e := handoff.DecodeClaims([]byte(item.Text))
		if e == nil {
			for _, claim := range claims {
				if claim.Key == "research.rule" {
					var text string
					if json.Unmarshal(claim.Value, &text) == nil && strings.Contains(text, "paid order once") && strings.Contains(text, "pending orders are excluded") {
						rule = true
					}
				}
			}
		}
		var observed verifier.Observation
		var rows [][]int64
		if verifier.StrictJSON([]byte(item.Text), &observed) == nil && json.Unmarshal(observed.Rows, &rows) == nil && len(rows) == 1 && len(rows[0]) == 3 && rows[0][0] >= 0 && rows[0][1] > 0 && rows[0][2] >= 0 {
			metrics = true
		}
	}
	return rule && metrics
}

func parseModelRequest(r *http.Request, raw []byte, token string, c store.TaskClaim, caseID, goal, snapshot string, next int) (request mockRequest, err error) {
	if len(raw) > 65536 || r.Method != "POST" || r.URL.RawQuery != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 || verifier.StrictJSON(raw, &request) != nil {
		return request, store.ErrPolicy
	}
	if request.RunID != c.RunID || request.TaskID != c.TaskID || request.PlanVersion != c.PlanVersion || request.CaseID != caseID || request.Goal != goal || request.Snapshot != snapshot || request.Turn != next || next < 1 || next > 8 || !json.Valid([]byte(request.Tools)) {
		return request, store.ErrPolicy
	}
	return request, nil
}
