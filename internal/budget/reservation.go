package budget

import (
	"errors"
	"regexp"
)

var ErrPricing = errors.New("INVALID_FROZEN_PRICING")
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Pricing struct {
	Version                string `json:"version"`
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	InputMicrosPerMillion  int64  `json:"input_micros_per_million"`
	OutputMicrosPerMillion int64  `json:"output_micros_per_million"`
}

func (p Pricing) Valid() bool {
	return modelName.MatchString(p.Version) && (p.Provider == "mock" || p.Provider == "deepseek") && modelName.MatchString(p.Model) && p.InputMicrosPerMillion >= 0 && p.InputMicrosPerMillion <= 1_000_000_000 && p.OutputMicrosPerMillion >= 0 && p.OutputMicrosPerMillion <= 1_000_000_000
}
func (p Pricing) Cost(input, output int64) (int64, error) {
	if !p.Valid() || input < 0 || output < 0 || input > 10_000_000 || output > 10_000_000 || input+output > 10_000_000 {
		return 0, ErrPricing
	}
	return (input*p.InputMicrosPerMillion + output*p.OutputMicrosPerMillion + 999999) / 1000000, nil
}
func PricedRequest(id string, p Pricing, input, output int64, inputDigest string) (ModelRequest, error) {
	cost, e := p.Cost(input, output)
	if e != nil {
		return ModelRequest{}, e
	}
	r := ModelRequest{LogicalID: id, Provider: p.Provider, Model: p.Model, Tokens: input + output, CostMicros: cost, Pricing: &p, InputTokens: input, OutputTokens: output, InputDigest: inputDigest}
	if !r.Valid() {
		return ModelRequest{}, ErrPricing
	}
	return r, nil
}

type Limits struct {
	ModelCalls      int64 `json:"model_calls"`
	ToolCalls       int64 `json:"tool_calls"`
	Tokens          int64 `json:"tokens"`
	CostMicros      int64 `json:"cost_micros"`
	ConcurrentCalls int64 `json:"concurrent_calls"`
	TaskConcurrency int   `json:"task_concurrency"`
	Delegations     int   `json:"delegations"`
}

func (l Limits) Valid() bool {
	return l.ModelCalls >= 0 && l.ModelCalls <= 1_000_000 && l.ToolCalls >= 0 && l.ToolCalls <= 1_000_000 && l.Tokens >= 0 && l.Tokens <= 1_000_000_000_000 && l.CostMicros >= 0 && l.CostMicros <= 1_000_000_000_000 && l.ConcurrentCalls >= 1 && l.ConcurrentCalls <= 1024 && l.TaskConcurrency >= 1 && l.TaskConcurrency <= 1024 && l.Delegations >= 0 && l.Delegations <= 1024
}

type Report struct {
	RunID              string `json:"run_id"`
	Limits             Limits `json:"limits"`
	ReservedModels     int64  `json:"reserved_models"`
	SettledModels      int64  `json:"settled_models"`
	ReservedTools      int64  `json:"reserved_tools"`
	SettledTools       int64  `json:"settled_tools"`
	ReservedTokens     int64  `json:"reserved_tokens"`
	SettledTokens      int64  `json:"settled_tokens"`
	ReservedCostMicros int64  `json:"reserved_cost_micros"`
	SettledCostMicros  int64  `json:"settled_cost_micros"`
	PendingModels      int64  `json:"pending_models"`
	UnknownModels      int64  `json:"unknown_models"`
	PendingTools       int64  `json:"pending_tools"`
	UnknownTools       int64  `json:"unknown_tools"`
	RunningTasks       int64  `json:"running_tasks"`
	Delegations        int64  `json:"delegations"`
	Conserved          bool   `json:"conserved"`
}
