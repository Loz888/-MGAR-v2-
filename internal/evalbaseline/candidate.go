// Package evalbaseline transports explicitly unverified experimental candidates.
// It never writes verdicts, task states, production input snapshots or DDL.
package evalbaseline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/google/uuid"
)

var ErrScope = errors.New("CANDIDATE_SCOPE_OR_INTEGRITY_DENIED")

const MaxContextBytes = 48000

type Candidate struct {
	Schema         string `json:"schema_version"`
	ID             string `json:"artifact_id"`
	RunID          string `json:"producer_run_id"`
	TaskID         string `json:"producer_task_id"`
	Role           string `json:"producer_role"`
	Node           string `json:"producer_node"`
	PlanVersion    int    `json:"source_plan_version"`
	Kind           string `json:"kind"`
	ToolVersion    string `json:"tool_version"`
	Hash           string `json:"content_sha256"`
	Size           int64  `json:"byte_length"`
	Content        string `json:"content"`
	SemanticStatus string `json:"semantic_status"`
}
type Context struct {
	Schema      string      `json:"schema_version"`
	RunID       string      `json:"run_id"`
	TaskID      string      `json:"consumer_task_id"`
	Role        string      `json:"consumer_role"`
	PlanVersion int         `json:"plan_version"`
	Inputs      []Candidate `json:"inputs"`
}
type grant struct {
	claim   store.TaskClaim
	context Context
	digest  string
}
type Broker struct {
	s         *store.Store
	cas       *artifact.CAS
	run       string
	version   int
	mu        sync.Mutex
	grants    map[string]grant
	consumers map[string]string
}

// New is a controller-only constructor for one disposable experiment. Workers
// receive only Gateway tokens, never this Store, Broker or its grant handles.
func New(ctx context.Context, s *store.Store, cas *artifact.CAS, run string, version int) (*Broker, error) {
	if s == nil || cas == nil || version < 1 {
		return nil, ErrScope
	}
	if id, e := uuid.Parse(run); e != nil || id.String() != run {
		return nil, ErrScope
	}
	w, e := s.Workflow(ctx, run)
	if e != nil || w.State != "running" || w.ActivePlanVersion != version {
		return nil, ErrScope
	}
	var edges int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM plan_edges WHERE run_id=$1 AND plan_version=$2", run, version).Scan(&edges); e != nil || edges != 0 {
		return nil, ErrScope
	}
	return &Broker{s: s, cas: cas, run: run, version: version, grants: map[string]grant{}, consumers: map[string]string{}}, nil
}
func (b *Broker) inspect(ctx context.Context, id string) (out Candidate, owner string, epoch, revision int64, err error) {
	out.Schema = "candidate_evidence.v1"
	out.SemanticStatus = "unverified"
	err = b.s.Pool.QueryRow(ctx, `SELECT a.artifact_id,a.run_id,a.producer_task,t.role,n.node_key,a.source_plan_version,a.kind,a.tool_schema_version,a.content_sha256,a.byte_length,t.owner,t.epoch,t.revision
 FROM artifacts a JOIN task_instances t ON t.task_id=a.producer_task AND t.run_id=a.run_id AND t.result_artifact_id=a.artifact_id
 JOIN workflow_runs w ON w.run_id=a.run_id
 JOIN plan_nodes n ON n.run_id=w.run_id AND n.plan_version=w.active_plan_version AND n.task_instance_id=t.task_id
 WHERE a.artifact_id=$1 AND a.run_id=$2 AND w.active_plan_version=$3 AND w.state='running'
 AND a.source_plan_version=$3 AND t.role=n.role AND t.state='output_ready'
 AND a.kind=n.requirements->>'required_output_kind' AND a.content_type='application/json'
 AND NOT EXISTS(SELECT 1 FROM artifact_verdicts v WHERE v.artifact_id=a.artifact_id)`, id, b.run, b.version).Scan(&out.ID, &out.RunID, &out.TaskID, &out.Role, &out.Node, &out.PlanVersion, &out.Kind, &out.ToolVersion, &out.Hash, &out.Size, &owner, &epoch, &revision)
	if err != nil || out.Size < 1 || out.Size > 16000 {
		return Candidate{}, "", 0, 0, ErrScope
	}
	raw, e := b.cas.Read(out.Hash, out.Size)
	if e != nil {
		return Candidate{}, "", 0, 0, ErrScope
	}
	out.Content = string(raw)
	return out, owner, epoch, revision, nil
}

// Publish registers a real immutable output_ready artifact, never an Agent's
// metadata assertion. No semantic comparison or verdict is performed here.
func (b *Broker) Publish(ctx context.Context, c store.TaskClaim, a artifact.Metadata) (Candidate, error) {
	if c.RunID != b.run || c.PlanVersion != b.version || a.RunID != b.run || a.ProducerTaskID != c.TaskID || a.SourcePlanVersion != b.version {
		return Candidate{}, ErrScope
	}
	out, owner, epoch, revision, e := b.inspect(ctx, a.ID)
	if e != nil || out.Hash != a.Hash || out.Kind != a.Kind || out.ToolVersion != a.ToolVersion || out.Size != a.ByteLength || owner != c.Owner || epoch != c.Epoch || revision != c.Revision+1 || out.Role != c.Role || out.Node != c.NodeKey {
		return Candidate{}, ErrScope
	}
	return out, nil
}
func allowed(consumer string, source Candidate) bool {
	switch consumer {
	case "action":
		return (source.Role == "research" && source.Kind == "finding") || (source.Role == "data" && source.Kind == "sql_evidence")
	case "aggregate":
		return (source.Role == "research" && source.Kind == "finding") || (source.Role == "data" && source.Kind == "sql_evidence") || (source.Role == "action" && source.Kind == "patch")
	default:
		return false
	}
}

// Grant freezes one exact context per consumer Task. It cannot be upgraded or
// rebound after publication, even if caller-owned slices are later mutated.
func (b *Broker) Grant(ctx context.Context, c store.TaskClaim, inputs []Candidate) (string, error) {
	if c.RunID != b.run || c.PlanVersion != b.version || len(inputs) < 1 || len(inputs) > 24 || b.s.ValidateToolClaim(ctx, c) != nil {
		return "", ErrScope
	}
	inputs = append([]Candidate{}, inputs...)
	seen := map[string]bool{}
	for _, item := range inputs {
		if !allowed(c.Role, item) || seen[item.ID] || seen[item.Node] || item.TaskID == c.TaskID {
			return "", ErrScope
		}
		seen[item.ID] = true
		seen[item.Node] = true
		actual, _, _, _, e := b.inspect(ctx, item.ID)
		if e != nil || actual != item {
			return "", ErrScope
		}
	}
	context := Context{Schema: "candidate_context.v1", RunID: b.run, TaskID: c.TaskID, Role: c.Role, PlanVersion: b.version, Inputs: inputs}
	raw := inputcontext.Canonical(context)
	if len(raw) > MaxContextBytes {
		return "", ErrScope
	}
	digest := artifact.Digest(raw)
	if b.s.ValidateToolClaim(ctx, c) != nil {
		return "", ErrScope
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if handle, ok := b.consumers[c.TaskID]; ok {
		old := b.grants[handle]
		if old.claim != c || old.digest != digest {
			return "", ErrScope
		}
		return handle, nil
	}
	if len(b.grants) >= 24 {
		return "", ErrScope
	}
	nonce := make([]byte, 32)
	if _, e := rand.Read(nonce); e != nil {
		return "", e
	}
	handle := hex.EncodeToString(nonce)
	b.grants[handle] = grant{c, context, digest}
	b.consumers[c.TaskID] = handle
	return handle, nil
}
func (b *Broker) Read(ctx context.Context, c store.TaskClaim, handle string) ([]byte, error) {
	b.mu.Lock()
	g, ok := b.grants[handle]
	b.mu.Unlock()
	if !ok || g.claim != c || b.s.ValidateToolClaim(ctx, c) != nil {
		return nil, ErrScope
	}
	for _, item := range g.context.Inputs {
		actual, _, _, _, e := b.inspect(ctx, item.ID)
		if e != nil || actual != item {
			return nil, ErrScope
		}
	}
	raw := inputcontext.Canonical(g.context)
	if artifact.Digest(raw) != g.digest || len(raw) > MaxContextBytes || b.s.ValidateToolClaim(ctx, c) != nil {
		return nil, ErrScope
	}
	return raw, nil
}
