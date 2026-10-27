package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5"
)

// These are the reviewed b01594e zero-send failure artifacts, not user-selectable
// evidence. Future mutable C reports cannot rewrite this original checkpoint.
var recoveryEvidence = map[string]string{
	"actual-live-run.log":            "e4c2e417ed2ade93201df051a747d63aef4584458b0515dbdfb18d6515e57c6d",
	"launch-provenance.json":         "5f6f9a98fedfbab10a2309cd71c73194792f454ab07d60b91ce2942e69f00d3e",
	"schema-before-repair.json":      "10a1cb2f06fc92811effd8c31b4e809f9a340a95a16fd9164f38529dd96a95cf",
	"schema-after-repair.json":       "20f659a768eade81d09020e4f1f9f0fca46a0130d12d3bfad69b8b8c4ad416ef",
	"allocation-lease-readonly.json": "3b050527aee2197e3170efb5b12c263398562ee71ef140c4bd46b689ee14bf24",
	"accept.json":                    "904b772c2077b8534f484998ae339ef73d26be0b18fd9f32ce6ff1dc0ceaaca9",
}

type recoveryProof struct {
	Previous                     store.OrchestratorCheckpoint
	Schema, Pattern, Fingerprint string
	Namespaces                   []string
}

func safeRecoveryPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for p := path; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return false
		}
		if filepath.Dir(p) == p {
			return true
		}
	}
}
func loadRecoveryProof(root string) (proof recoveryProof, err error) {
	proof.Schema = "public"
	proof.Namespaces = []string{"public"}
	proof.Pattern = "^mgar_demo_[0-9a-f]{16}$"
	names := make([]string, 0, len(recoveryEvidence))
	for name := range recoveryEvidence {
		names = append(names, name)
	}
	sort.Strings(names)
	var fingerprint strings.Builder
	for _, name := range names {
		path := filepath.Join(root, "evidence/batches/P7-06E/original", name)
		if !safeRecoveryPath(path) {
			return proof, ErrUsed
		}
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return proof, ErrUsed
		}
		raw, e := os.ReadFile(path)
		if e != nil || artifact.Digest(raw) != recoveryEvidence[name] {
			return proof, ErrUsed
		}
		fingerprint.WriteString(name + "\n" + recoveryEvidence[name] + "\n")
		if name == "allocation-lease-readonly.json" {
			var row struct {
				Goal   string
				Owner  string `json:"owner_id"`
				Expiry string `json:"lease_until"`
			}
			if json.Unmarshal(raw, &row) != nil {
				return proof, ErrUsed
			}
			expiry, e := time.Parse(time.RFC3339Nano, strings.Replace(row.Expiry, " ", "T", 1))
			if e != nil {
				return proof, ErrUsed
			}
			proof.Previous = store.OrchestratorCheckpoint{Goal: row.Goal, Owner: row.Owner, Epoch: 1, Revision: 0, LeaseUntil: expiry}
		}
	}
	proof.Fingerprint = artifact.Digest([]byte(fingerprint.String()))
	return proof, nil
}
func zeroSendFiles(root string) bool {
	if !safeRecoveryPath(root) || !safeRecoveryPath(statePath(root)) {
		return false
	}
	if _, e := os.Lstat(statePath(root)); !errors.Is(e, os.ErrNotExist) {
		return false
	}
	cas := filepath.Join(root, ".local/live-cas")
	if !safeRecoveryPath(cas) {
		return false
	}
	entries, e := os.ReadDir(cas)
	return errors.Is(e, os.ErrNotExist) || (e == nil && len(entries) == 0)
}

// inspectZeroSend uses a single read-only catalog/data snapshot. All namespaces
// from the reviewed inventory must still exist. Every additional matching demo
// namespace is checked too; a missing table or unreadable namespace is not zero.
func inspectZeroSend(ctx context.Context, s *store.Store, p Plan, proof recoveryProof) bool {
	if !allocationSchemaReady(ctx, s) {
		return false
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var schema string
	if tx.QueryRow(ctx, "SELECT current_schema()").Scan(&schema) != nil || schema != proof.Schema {
		return false
	}
	rows, e := tx.Query(ctx, "SELECT nspname FROM pg_namespace WHERE nspname=$1 OR nspname~$2 ORDER BY nspname", proof.Schema, proof.Pattern)
	if e != nil {
		return false
	}
	names := []string{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) != nil {
			rows.Close()
			return false
		}
		names = append(names, n)
	}
	rows.Close()
	if rows.Err() != nil {
		return false
	}
	for _, n := range proof.Namespaces {
		if !slices.Contains(names, n) {
			return false
		}
	}
	ids := []string{demo.LiveRunID("allocation", "all")}
	for _, a := range p.Assignments {
		ids = append(ids, a.RunID)
	}
	for _, name := range names {
		qualified := func(table string) string { return pgx.Identifier{name, table}.Sanitize() }
		// Locking is unnecessary for legitimate senders: no live original lease or
		// assignment exists, and both normal/recovery allocation admissions serialize
		// on the original marker. The final CAS rechecks its exact expired checkpoint.
		for table, column := range map[string]string{"workflow_runs": "run_id", "task_instances": "run_id", "model_calls": "run_id", "tool_invocations": "run_id"} {
			var count int64
			query := "SELECT count(*) FROM " + qualified(table) + " WHERE " + column + "::text=ANY($1::text[])"
			if table == "tool_invocations" {
				query = "SELECT count(*) FROM " + qualified(table) + " v JOIN " + qualified("task_instances") + " t USING(task_id) WHERE t.run_id::text=ANY($1::text[])"
			}
			if tx.QueryRow(ctx, query, ids).Scan(&count) != nil {
				return false
			}
			expected := int64(0)
			if table == "workflow_runs" && name == proof.Schema {
				expected = 1
			}
			if count != expected {
				return false
			}
		}
		var count int64
		if tx.QueryRow(ctx, "SELECT count(*) FROM "+qualified("plans")+" WHERE run_id::text=ANY($1::text[])", ids).Scan(&count) != nil || count != 0 {
			return false
		}
	}
	// The marker must still have its original default, unused limits too: no
	// funded model/tool allocation has ever been configured on it.
	b, e := store.BudgetReportTx(ctx, tx, ids[0])
	if e != nil || !b.Conserved {
		return false
	}
	expected := budget.Limits{ModelCalls: 0, ToolCalls: 64, Tokens: 0, CostMicros: 0, ConcurrentCalls: 4, TaskConcurrency: 4, Delegations: 1}
	if b.Limits != expected || b.ReservedModels+b.SettledModels+b.ReservedTools+b.SettledTools+b.ReservedTokens+b.SettledTokens+b.ReservedCostMicros+b.SettledCostMicros+b.RunningTasks+int64(b.Delegations) != 0 {
		return false
	}
	var events int64
	if tx.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1", ids[0]).Scan(&events) != nil || events != 2 {
		return false
	}
	return tx.Commit(ctx) == nil
}
func claimZeroSend(ctx context.Context, root string, s *store.Store, p Plan, proof recoveryProof) (store.OrchestratorClaim, error) {
	raw, _ := json.Marshal(p)
	if p.Validate() != nil || proof.Previous.Goal != "Live allocation "+p.ID+" plan_sha256="+artifact.Digest(raw) || !zeroSendFiles(root) || !inspectZeroSend(ctx, s, p, proof) {
		return store.OrchestratorClaim{}, ErrUsed
	}
	// The lease expiry must equal the reviewed original timestamp. No caller can
	// turn another newly created allocation into this exception.
	return s.TakeOrchestratorOnce(ctx, demo.LiveRunID("allocation", "all"), proof.Previous, "live-zero-send-recovery", time.Minute)
}

// RecoveryPreflight is read-only, including the marker/lease exact checkpoint.
// It never opens a provider or consumes the epoch. Normal Preflight is unchanged.
func RecoveryPreflight(ctx context.Context, root string) (map[string]any, error) {
	p, e := LoadPlan(root)
	if e != nil {
		return nil, ErrGate
	}
	proof, e := loadRecoveryProof(root)
	if e != nil {
		return nil, ErrUsed
	}
	s, e := store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
	if e != nil {
		return nil, ErrGate
	}
	defer s.Pool.Close()
	if !zeroSendFiles(root) || !inspectZeroSend(ctx, s, p, proof) {
		return nil, ErrUsed
	}
	var state, goal, owner string
	var revision, version, epoch, leaseRev int64
	var expiry time.Time
	var expired bool
	e = s.Pool.QueryRow(ctx, `SELECT w.state,w.goal,w.revision,w.active_plan_version,l.owner_id,l.epoch,l.revision,l.lease_until,l.lease_until<=clock_timestamp() FROM workflow_runs w JOIN orchestrator_leases l USING(run_id) WHERE w.run_id=$1`, demo.LiveRunID("allocation", "all")).Scan(&state, &goal, &revision, &version, &owner, &epoch, &leaseRev, &expiry, &expired)
	if e != nil || state != "queued" || revision != 0 || version != 0 || goal != proof.Previous.Goal || owner != proof.Previous.Owner || epoch != 1 || leaseRev != 0 || !expired || !expiry.Equal(proof.Previous.LeaseUntil) {
		return nil, ErrUsed
	}
	return map[string]any{"ready": true, "mode": "approved_zero_send_epoch_1_to_2", "proof_sha256": proof.Fingerprint, "epoch": epoch, "assigned": 96, "cap_micros": p.CapMicros, "generation_requests_sent": 0}, nil
}

// finishZeroSend consumes the single CAS before any remaining initialization.
// A crash/error after that CAS leaves epoch 2 persisted and cannot be retried.
func finishZeroSend(ctx context.Context, root string, s *store.Store, p Plan, proof recoveryProof) error {
	c, e := claimZeroSend(ctx, root, s, p, proof)
	if e != nil {
		return e
	}
	id := demo.LiveRunID("allocation", "all")
	if e = s.ConfigureBudget(ctx, id, c, budget.Limits{ConcurrentCalls: 1, TaskConcurrency: 1, Delegations: 1}); e != nil {
		return e
	}
	_, e = s.CancelWorkflow(ctx, id, 0)
	return e
}
