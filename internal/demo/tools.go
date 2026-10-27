package demo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/handoff"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/google/uuid"
)

type taskTools struct {
	mu           sync.Mutex
	expected     []byte
	recipe       verifier.Recipe
	observation  verifier.Observation
	replayer     verifier.Reexecutor
	executed     int
	sandboxCalls int
	testInput    verifier.TestInput
}

func fact(key string, value any) handoff.Claim {
	return handoff.Claim{Key: key, Value: inputcontext.Canonical(value)}
}
func facts(items ...handoff.Claim) []byte {
	return inputcontext.Canonical(map[string]any{"schema_version": "facts.v1", "facts": items})
}
func (f *fixture) definitions(c store.TaskClaim, snap inputcontext.Snapshot, state *taskTools) []policy.Definition {
	define := func(name, effect string, params map[string]policy.Parameter, fn func(context.Context, map[string]any) ([]byte, error)) policy.Definition {
		return policy.Definition{Name: name, Version: "demo.v1", Role: c.Role, Effect: effect, Resources: []string{"fixture"}, Parameters: params, Timeout: 30 * time.Second, MaxOutput: 60000, Execute: func(ctx context.Context, _ string, args map[string]any) ([]byte, error) {
			state.mu.Lock()
			defer state.mu.Unlock()
			body, e := fn(ctx, args)
			if e == nil {
				state.executed++
			}
			return body, e
		}}
	}
	empty := map[string]policy.Parameter{}
	switch c.Role {
	case "research":
		return []policy.Definition{define("research.read", "readonly", empty, func(ctx context.Context, _ map[string]any) ([]byte, error) {
			body := inputcontext.Canonical(f.input.Documents)
			state.expected = facts(fact("research.source_hash", artifact.Digest(body)), fact("research.rule", f.input.Documents[0]))
			return state.expected, nil
		})}
	case "data":
		return []policy.Definition{define("data.query", "readonly", map[string]policy.Parameter{"query": {Kind: "string", Enum: []string{"paid-metrics"}}}, func(ctx context.Context, _ map[string]any) ([]byte, error) {
			state.recipe = verifier.Recipe{Method: "readonly_sql", Dataset: "orders.v1", Command: "paid-metrics.v1", InputDigest: f.sql.InputDigest}
			state.replayer = verifier.Reexecutor{SQL: map[string]verifier.SQLJob{"paid-metrics.v1": f.sql}}
			observed, e := state.replayer.Replay(ctx, state.recipe)
			if e != nil {
				return nil, e
			}
			state.observation = observed
			state.expected = inputcontext.Canonical(observed)
			return state.expected, nil
		})}
	case "action":
		return []policy.Definition{
			define("action.patch", "idempotent_write", map[string]policy.Parameter{"patch": {Kind: "string", Enum: []string{"deduplicate-only", "deduplicate-paid"}}}, func(ctx context.Context, args map[string]any) ([]byte, error) {
				// Source and enum are controller-owned; no model-provided path or source code.
				condition := "!seen[order.ID]"
				if args["patch"] == "deduplicate-paid" {
					condition = "order.Status == \"paid\" && !seen[order.ID]"
				}
				code := "package aggregate\ntype Order struct { ID int; Status string; Amount int }\nfunc PaidTotal(orders []Order) int { total:=0; seen:=map[int]bool{}; for _,order:=range orders { if " + condition + " { total+=order.Amount; seen[order.ID]=true } }; return total }\n"
				if e := os.WriteFile(filepath.Join(f.temp, "worktree/aggregate.go"), []byte(code), 0600); e != nil {
					return nil, e
				}
				digest, e := verifier.SourceDigest(filepath.Join(f.temp, "worktree"))
				if e != nil {
					return nil, e
				}
				state.testInput = verifier.TestInput{Directory: filepath.Join(f.temp, "worktree"), Digest: digest, Toolchain: "go version go1.27.3 linux/amd64", Package: "example.invalid/mgar/incident", Tests: []string{"TestPaidDeduplicatedTotal"}}
				return inputcontext.Canonical(map[string]any{"patched": true, "source_sha256": digest, "patch": args["patch"]}), nil
			}),
			define("action.test", "readonly", empty, func(ctx context.Context, _ map[string]any) ([]byte, error) {
				if state.testInput.Digest == "" {
					return nil, errors.New("patch_required")
				}
				state.recipe = verifier.Recipe{Method: "go_test", Command: "incident-tests.v1", InputDigest: state.testInput.Digest}
				state.replayer = verifier.Reexecutor{Tests: map[string]verifier.TestInput{"incident-tests.v1": state.testInput}, Runner: f.sandbox}
				observed, e := state.replayer.Replay(ctx, state.recipe)
				state.sandboxCalls++
				if e != nil {
					return nil, e
				}
				zero, one := 0, 1
				state.observation = verifier.Observation{ExitCode: &zero, Tests: &one}
				state.expected = inputcontext.Canonical(state.observation)
				return inputcontext.Canonical(observed), nil
			}),
		}
	case "aggregate":
		return []policy.Definition{define("aggregate.merge", "readonly", empty, func(ctx context.Context, _ map[string]any) ([]byte, error) {
			merged, e := f.merge(ctx, c, snap)
			if e != nil {
				return nil, e
			}
			if !merged.CanFinalize {
				return nil, errors.New("merge_incomplete")
			}
			state.expected = inputcontext.Canonical(merged)
			if len(state.expected) > 60000 {
				return nil, errors.New("merge_capacity")
			}
			return receiptFor(state.expected), nil
		})}
	}
	return nil
}

// Typed business interpretation of verified Observation artifacts. References
// remain the original direct inputs; no derived self-asserted verification flag.
func (f *fixture) merge(ctx context.Context, c store.TaskClaim, snap inputcontext.Snapshot) (handoff.MergeResult, error) {
	sources := []handoff.Source{}
	for _, ref := range snap.Artifacts {
		body, e := f.s.ReadInput(ctx, c, f.cas, snap.ID, ref.ID)
		if e != nil {
			return handoff.MergeResult{}, e
		}
		var claims []handoff.Claim
		switch ref.Kind {
		case "finding", "verdict":
			claims, e = handoff.DecodeClaims(body)
		case "sql_evidence":
			var o verifier.Observation
			var rows [][]int64
			if verifier.StrictJSON(body, &o) != nil || json.Unmarshal(o.Rows, &rows) != nil || len(rows) != 1 || len(rows[0]) != 3 {
				return handoff.MergeResult{}, errors.New("invalid_sql_input")
			}
			claims = []handoff.Claim{fact("paid_total", rows[0][0]), fact("paid_orders", rows[0][1]), fact("duplicate_paid_events", rows[0][2])}
		case "patch":
			var o verifier.Observation
			if verifier.StrictJSON(body, &o) != nil || o.ExitCode == nil || *o.ExitCode != 0 || o.Tests == nil || *o.Tests < 1 {
				return handoff.MergeResult{}, errors.New("invalid_test_input")
			}
			claims = []handoff.Claim{fact("action.tests", *o.Tests), fact("action.exit_code", *o.ExitCode)}
		default:
			return handoff.MergeResult{}, errors.New("unsupported_input")
		}
		if e != nil {
			return handoff.MergeResult{}, e
		}
		sources = append(sources, handoff.Source{Ref: ref, Verified: true, Claims: claims})
	}
	return handoff.Merge(sources, nil)
}

// The independent checker has its own executing verify claim. This preserves
// the output_ready producer fence while accounting for every physical replay.
type admittedReplay struct {
	f     *fixture
	inner verifier.Reexecutor
}

func (a admittedReplay) Replay(ctx context.Context, recipe verifier.Recipe) (observation verifier.Observation, err error) {
	f := a.f
	c := f.verification.claim
	if c.Role != "verify" {
		return observation, store.ErrPolicy
	}
	req := store.ToolRequest{ID: uuid.NewString(), Capability: "verify." + recipe.Method, Version: "demo.v1", Resource: "fixture", Hash: artifact.Digest(inputcontext.Canonical(recipe)), Effect: "readonly"}
	cached, e := f.s.BeginTool(ctx, c, req)
	if e != nil || cached {
		return observation, errors.Join(e, errors.New("verification_reservation_rejected"))
	}
	status := "failed"
	var body []byte
	defer func() {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		err = errors.Join(err, f.s.FinishTool(settle, c, req, f.cas, body, status))
	}()
	resources, e := scheduler.NewResources(ctx, f.s)
	if e != nil {
		return observation, e
	}
	class := "db"
	stopped := true
	replay := a.inner
	if recipe.Method == "go_test" {
		class = "cpu"
		stopped = false
		replay.Runner = measuredSandbox{sandbox: f.sandbox, stopped: &stopped}
	}
	executed := false
	err = resources.Run(ctx, c, class, 0, func(ctx context.Context) (bool, error) {
		executed = true
		f.verification.calls++
		observation, e = replay.Replay(ctx, recipe)
		return stopped, e
	})
	if err != nil {
		if executed && !stopped {
			status = "unknown"
		}
		return observation, err
	}
	status = "succeeded"
	body = inputcontext.Canonical(observation)
	return observation, nil
}

type measuredSandbox struct {
	sandbox policy.Sandbox
	stopped *bool
}

func (s measuredSandbox) Run(ctx context.Context, input verifier.TestInput) (verifier.TestExecution, error) {
	r, e := s.sandbox.Execute(ctx, input)
	*s.stopped = r.Removed
	return r.TestExecution, e
}
