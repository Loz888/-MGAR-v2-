package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIDiagnosticsMachineJSONAndScope(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	s := testdb.New(t)
	var out, errs bytes.Buffer
	ctx := context.Background()
	if run(ctx, []string{"submit"}, &out, &errs, s) != 0 {
		t.Fatal(errs.String())
	}
	var w store.Workflow
	if e := json.Unmarshal(out.Bytes(), &w); e != nil {
		t.Fatal(e)
	}
	for _, cmd := range []string{"status", "trace", "budget-report"} {
		out.Reset()
		errs.Reset()
		if code := run(ctx, []string{cmd, w.RunID, "--format", "json"}, &out, &errs, s); code != 0 {
			t.Fatalf("%s exit %d: %s", cmd, code, errs.String())
		}
		var d map[string]any
		if json.Unmarshal(out.Bytes(), &d) != nil {
			t.Fatal("not JSON")
		}
		if d["run_id"] != w.RunID {
			t.Fatal(d)
		}
	}
	out.Reset()
	if code := run(ctx, []string{"trace", "ffffffff-ffff-4fff-8fff-ffffffffffff"}, &out, &errs, s); code != 1 {
		t.Fatal(code)
	}
	if bytes.Contains(out.Bytes(), []byte(w.RunID)) {
		t.Fatal("missing run leaked existing run")
	}
}

func databaseDigest(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	rows, e := s.Pool.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename")
	if e != nil {
		t.Fatal(e)
	}
	tables := []string{}
	for rows.Next() {
		var table string
		if e = rows.Scan(&table); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, table)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	all := ""
	for _, table := range tables {
		var hash string
		if e = s.Pool.QueryRow(ctx, "SELECT md5(COALESCE(string_agg(to_jsonb(t)::text,'|' ORDER BY to_jsonb(t)::text),'')) FROM "+pgx.Identifier{table}.Sanitize()+" t").Scan(&hash); e != nil {
			t.Fatal(e)
		}
		all += table + hash
	}
	return digestID(all)
}
func TestCLIArtifactDoctorReadOnlyAndIdentity(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	ctx := context.Background()
	s := testdb.New(t)
	root := filepath.Join(t.TempDir(), "cas")
	cas, e := artifact.New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	t.Setenv("MGAR_ARTIFACT_ROOT", root)
	var out, errs bytes.Buffer
	call := func(args ...string) int { out.Reset(); errs.Reset(); return run(ctx, args, &out, &errs, s) }
	if call("submit") != 0 {
		t.Fatal(errs.String())
	}
	var w store.Workflow
	if json.Unmarshal(out.Bytes(), &w) != nil {
		t.Fatal("submit JSON")
	}
	oc, e := s.TakeOrchestrator(ctx, w.RunID, "diagnostic-fixture", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "worker", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	content := []byte(`{"synthetic":"PRIVATE_BODY_MARKER"}`)
	a, e := s.PublishArtifact(ctx, c, cas, "finding", "application/json", "research.v1", content)
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.CreateRun(ctx, "", "PRIVATE_OTHER_RUN_MARKER", 4)
	if e != nil {
		t.Fatal(e)
	}
	before := databaseDigest(t, s)
	if call("inspect-artifact", a.ID) != 0 {
		t.Fatal(errs.String(), out.String())
	}
	var inspected artifactInfo
	if json.Unmarshal(out.Bytes(), &inspected) != nil || !inspected.BytesVerified || inspected.Hash != a.Hash || bytes.Contains(out.Bytes(), []byte("PRIVATE_BODY_MARKER")) {
		t.Fatal("unsafe inspect")
	}
	if call("inspect-artifact", a.ID, "--run", other.RunID) != 1 || bytes.Contains(out.Bytes(), []byte(w.RunID)) {
		t.Fatal("foreign artifact leak")
	}
	if call("inspect-artifact", "ffffffff-ffff-4fff-8fff-ffffffffffff") != 1 {
		t.Fatal("missing artifact")
	}
	if call("trace", w.RunID, "--identity", "admin") != 2 {
		t.Fatal("self asserted identity")
	}
	// Use an actual PG Worker role, not a CLI flag or a mocked authorization check.
	cfg := s.Pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, "SET ROLE mgar_worker"); return e }
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	out.Reset()
	if run(ctx, []string{"trace", w.RunID}, &out, &errs, &store.Store{Pool: pool}) != 1 || bytes.Contains(out.Bytes(), []byte(w.RunID)) {
		t.Fatal("Worker read control data")
	}
	// Missing CAS path must not be created, even in degraded doctor mode.
	missing := filepath.Join(t.TempDir(), "must-not-create")
	t.Setenv("MGAR_ARTIFACT_ROOT", missing)
	if call("doctor") != 1 {
		t.Fatal("missing CAS reported healthy")
	}
	var doctor doctorReport
	if json.Unmarshal(out.Bytes(), &doctor) != nil || doctor.Status != "degraded" || len(doctor.Actions) != 0 {
		t.Fatal("doctor JSON")
	}
	if _, e = os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("doctor created path", e)
	}
	t.Setenv("MGAR_ARTIFACT_ROOT", root)
	if code := call("doctor"); code != 0 {
		t.Fatal("healthy doctor", code, out.String())
	}
	if after := databaseDigest(t, s); after != before {
		t.Fatal("diagnostic modified a database row")
	}
	// Tampering is detected without returning contents or repairing the object.
	if e = os.WriteFile(filepath.Join(root, a.Hash), []byte("corrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	if call("inspect-artifact", a.ID) != 1 {
		t.Fatal("corruption accepted")
	}
	body, e := os.ReadFile(filepath.Join(root, a.Hash))
	if e != nil || string(body) != "corrupted" {
		t.Fatal("diagnostic rewrote CAS")
	}
}
func TestCLITraceUnknownAndDenials(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	ctx := context.Background()
	s := testdb.New(t)
	var out, errs bytes.Buffer
	if run(ctx, []string{"submit"}, &out, &errs, s) != 0 {
		t.Fatal(errs.String())
	}
	var w store.Workflow
	if json.Unmarshal(out.Bytes(), &w) != nil {
		t.Fatal("submit JSON")
	}
	oc, e := s.TakeOrchestrator(ctx, w.RunID, "diagnostic-owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ConfigureBudget(ctx, w.RunID, oc, budget.Limits{ModelCalls: 2, ToolCalls: 2, Tokens: 100, CostMicros: 100, ConcurrentCalls: 2, TaskConcurrency: 4, Delegations: 24}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshReady(ctx, w.RunID, oc); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimTask(ctx, w.RunID, store.WorkerIdentity{Owner: "worker", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	cas, e := artifact.New(filepath.Join(t.TempDir(), "cas"))
	if e != nil {
		t.Fatal(e)
	}
	defer cas.Close()
	if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
		t.Fatal(e)
	}
	c, e = s.StartTask(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RecordToolDenial(ctx, c, artifact.Digest([]byte("denied")), "policy_denied"); e != nil {
		t.Fatal(e)
	}
	req := budget.ModelRequest{LogicalID: "11111111-1111-4111-8111-111111111111", Provider: "mock", Model: "mock.v1", Tokens: 10, CostMicros: 10}
	model, e := s.ReserveModelCall(ctx, c, req)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishModelCall(ctx, c, model.ID, budget.ModelUsage{Status: "unknown", Source: "unknown"}); e != nil {
		t.Fatal(e)
	}
	before := databaseDigest(t, s)
	out.Reset()
	if run(ctx, []string{"trace", w.RunID, "--format", "json"}, &out, &errs, s) != 0 {
		t.Fatal(out.String(), errs.String())
	}
	var report diagnosticReport
	if e = json.Unmarshal(out.Bytes(), &report); e != nil || report.Audit == nil || report.DeniedActions != 1 || report.Budget.UnknownModels != 1 || report.Audit.Models[0].Tokens != nil || len(report.Plans) != 1 {
		t.Fatal(report, e)
	}
	actor := false
	for _, event := range report.Audit.Events {
		if event.Kind == "orchestrator_claimed" && event.Epoch > 0 && event.ActorDigest != "" {
			actor = true
		}
	}
	if !actor {
		t.Fatal("takeover identity/epoch not shown")
	}
	if databaseDigest(t, s) != before {
		t.Fatal("trace repaired unknown call")
	}
	out.Reset()
	if run(ctx, []string{"resume", w.RunID}, &out, &errs, s) == 0 {
		t.Fatal("unsafe resume")
	}
}

func TestCLIDoctorSchemaDriftDoesNotRepair(t *testing.T) {
	t.Setenv("MGAR_PROVIDER", "mock")
	t.Setenv("MGAR_ARTIFACT_ROOT", t.TempDir())
	t.Setenv("PATH", "")
	s := testdb.New(t)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, "UPDATE schema_migrations SET checksum=repeat('0',64) WHERE version='006_resource_admission'"); e != nil {
		t.Fatal(e)
	}
	before := databaseDigest(t, s)
	var out, errs bytes.Buffer
	if run(ctx, []string{"doctor"}, &out, &errs, s) != 1 {
		t.Fatal("drift reported healthy")
	}
	var report doctorReport
	if json.Unmarshal(out.Bytes(), &report) != nil {
		t.Fatal("doctor JSON")
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "schema_checksums" && check.Status == "failed" {
			found = true
		}
	}
	if !found || len(report.Actions) != 0 || databaseDigest(t, s) != before {
		t.Fatal("doctor hid or repaired drift")
	}
}
