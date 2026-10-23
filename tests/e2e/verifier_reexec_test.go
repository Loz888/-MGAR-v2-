package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type oracleData struct {
	PaidTotal int             `json:"paid_total"`
	Orders    json.RawMessage `json:"distinct_paid_orders"`
	Events    json.RawMessage `json:"all_events"`
}

func incidentOracle(t *testing.T) oracleData {
	t.Helper()
	body, e := os.ReadFile("../../fixtures/incident/oracles/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	var out oracleData
	if e = json.Unmarshal(body, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func sqlReplayFixture(t *testing.T) (verifier.Reexecutor, *pgxpool.Pool, pgx.Tx, string) {
	t.Helper()
	ctx := context.Background()
	s := testdb.New(t)
	schema := s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	ident := pgx.Identifier{schema}.Sanitize()
	raw, e := os.ReadFile("../../fixtures/incident/sql/orders.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, strings.ReplaceAll(string(raw), "__SCHEMA__", ident)); e != nil {
		t.Fatal(e)
	}
	secret := make([]byte, 24)
	if _, e = rand.Read(secret); e != nil {
		t.Fatal(e)
	}
	role := schema + "_oracle"
	roleSQL := pgx.Identifier{role}.Sanitize()
	if _, e = s.Pool.Exec(ctx, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+hex.EncodeToString(secret)+"'"); e != nil {
		t.Fatal("owned reader login creation failed")
	}
	if _, e = s.Pool.Exec(ctx, "GRANT USAGE ON SCHEMA "+ident+" TO "+roleSQL+"; GRANT SELECT ON "+ident+".orders TO "+roleSQL); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := s.Pool.Exec(context.Background(), "REVOKE ALL ON "+ident+".orders FROM "+roleSQL+"; REVOKE ALL ON SCHEMA "+ident+" FROM "+roleSQL+"; DROP ROLE "+roleSQL)
		if e != nil {
			t.Error("owned oracle login cleanup failed")
		}
	})
	cfg, e := pgxpool.ParseConfig(testdb.DSN(t))
	if e != nil {
		t.Fatal("synthetic reader config")
	}
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = hex.EncodeToString(secret)
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	cfg.MaxConns = 2
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal("reader pool")
	}
	t.Cleanup(pool.Close)
	source, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = source.Rollback(context.Background()) })
	var snapshot string
	if e = source.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	canonical, e := verifier.NormalizeRows(incidentOracle(t).Events)
	if e != nil {
		t.Fatal(e)
	}
	digest := artifact.Digest([]byte(canonical))
	job := verifier.SQLJob{Pool: pool, Dataset: "orders.v1", Snapshot: snapshot, InputDigest: digest, FingerprintSQL: "SELECT event_id,order_id,status,amount FROM " + ident + ".orders", Query: "SELECT order_id,max(amount) FROM " + ident + ".orders WHERE status='paid' GROUP BY order_id"}
	return verifier.Reexecutor{SQL: map[string]verifier.SQLJob{"paid-orders.v1": job}}, s.Pool, source, digest
}
func proofForObservation(t *testing.T, kind string, recipe verifier.Recipe, observed, expected verifier.Observation) ([]byte, verifier.Proof, verifier.Requirement) {
	t.Helper()
	body, e := json.Marshal(observed)
	if e != nil {
		t.Fatal(e)
	}
	m := artifact.Metadata{ID: "1290c937-a6e5-432a-bfa2-8bf4b42931a1", RunID: "1290c937-a6e5-432a-bfa2-8bf4b42931a2", ProducerTaskID: "1290c937-a6e5-432a-bfa2-8bf4b42931a3", SourcePlanVersion: 1, Hash: artifact.Digest(body), ByteLength: int64(len(body)), ToolVersion: "oracle.v1"}
	snapshot := inputcontext.Snapshot{ID: "1290c937-a6e5-432a-bfa2-8bf4b42931a4", RunID: m.RunID, TaskID: m.ProducerTaskID, PlanVersion: 1}
	evidence := verifier.Evidence{Schema: "evidence.v1", Claim: "independently reproduce the fixed observation", Kind: kind, ArtifactID: m.ID, Hash: m.Hash, RunID: m.RunID, TaskID: m.ProducerTaskID, PlanVersion: 1, SnapshotID: snapshot.ID, ToolVersion: m.ToolVersion, ExecutionStatus: "succeeded", Recipe: recipe, Observed: observed, Expected: expected}
	raw, e := json.Marshal(evidence)
	if e != nil {
		t.Fatal(e)
	}
	return raw, verifier.Proof{Metadata: m, Bytes: body, Snapshot: snapshot}, verifier.Requirement{Kind: kind, ToolVersion: m.ToolVersion, Recipe: recipe, Expected: expected}
}
func TestReexecSQLIndependentOracle(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_value", "missing_row", "changed_live_data", "no_snapshot", "expired_snapshot", "wrong_digest", "privileged_connection", "write_query", "detached_artifact"} {
		t.Run(mode, func(t *testing.T) {
			replay, admin, source, digest := sqlReplayFixture(t)
			ctx := context.Background()
			two := 2
			expected := verifier.Observation{Rows: incidentOracle(t).Orders, RowCount: &two}
			observed := expected
			want := "passed"
			switch mode {
			case "wrong_value":
				observed.Rows = json.RawMessage(`[[101,200],[102,50]]`)
				expected = observed // Even a wrong configured expectation cannot override actual SQL.
				want = "failed"
			case "missing_row":
				one := 1
				observed.Rows = json.RawMessage(`[[101,100]]`)
				observed.RowCount = &one
				expected = observed
				want = "failed"
			case "changed_live_data":
				if _, e := admin.Exec(ctx, "INSERT INTO orders VALUES(5,104,'paid',999)"); e != nil {
					t.Fatal(e)
				}
			case "no_snapshot":
				job := replay.SQL["paid-orders.v1"]
				job.Snapshot = ""
				replay.SQL["paid-orders.v1"] = job
				want = "unverifiable"
			case "expired_snapshot":
				if e := source.Rollback(ctx); e != nil {
					t.Fatal(e)
				}
				want = "unverifiable"
			case "wrong_digest":
				digest = artifact.Digest([]byte("different snapshot"))
				want = "unverifiable"
			case "privileged_connection":
				job := replay.SQL["paid-orders.v1"]
				job.Pool = admin
				replay.SQL["paid-orders.v1"] = job
				want = "unverifiable"
			case "write_query":
				job := replay.SQL["paid-orders.v1"]
				job.Query = strings.Replace(job.Query, "SELECT order_id,max(amount) FROM ", "DELETE FROM ", 1)
				job.Query = strings.Split(job.Query, " WHERE")[0] + " RETURNING order_id,amount"
				replay.SQL["paid-orders.v1"] = job
				want = "unverifiable"
			case "detached_artifact":
				want = "failed"
			}
			recipe := verifier.Recipe{Method: "readonly_sql", Dataset: "orders.v1", Command: "paid-orders.v1", InputDigest: digest}
			raw, p, r := proofForObservation(t, "sql_result", recipe, observed, expected)
			if mode == "detached_artifact" {
				p.Bytes = []byte(`{"row_count":1,"rows":[[999,999]]}`)
				p.Metadata.Hash = artifact.Digest(p.Bytes)
				p.Metadata.ByteLength = int64(len(p.Bytes))
				var wire map[string]any
				_ = json.Unmarshal(raw, &wire)
				wire["content_sha256"] = p.Metadata.Hash
				raw, _ = json.Marshal(wire)
			}
			v := (verifier.Engine{Replayer: replay}).Check(ctx, raw, p, r)
			if v.Status != want {
				t.Fatal(v)
			}
			if mode == "changed_live_data" {
				var count int
				if e := admin.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&count); e != nil || count != 5 {
					t.Fatal(count, e)
				}
			}
			if mode == "write_query" {
				var count int
				if e := admin.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&count); e != nil || count != 4 {
					t.Fatal("read-only boundary failed", count, e)
				}
			}
		})
	}
}

func fixedTestInput(t *testing.T, fixed bool) verifier.TestInput {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"go.mod", "aggregate.go", "aggregate_test.go"} {
		path := "../../fixtures/incident/go-repo/" + name
		if fixed && name == "aggregate.go" {
			path = "../../fixtures/incident/oracles/fixed_aggregate.go.txt"
		}
		body, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), body, 0600); e != nil {
			t.Fatal(e)
		}
	}
	digest, e := verifier.SourceDigest(dir)
	if e != nil {
		t.Fatal(e)
	}
	return verifier.TestInput{Directory: dir, Digest: digest, Toolchain: "go version go1.27.3 linux/amd64", Package: "example.invalid/mgar/incident", Tests: []string{"TestPaidDeduplicatedTotal"}}
}
func TestReexecGoTestIndependentOracle(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		name := "forged_success_on_bug"
		want := "failed"
		if fixed {
			name = "valid_fix"
			want = "passed"
		}
		t.Run(name, func(t *testing.T) {
			input := fixedTestInput(t, fixed)
			zero, one := 0, 1
			expected := verifier.Observation{ExitCode: &zero, Tests: &one}
			recipe := verifier.Recipe{Method: "go_test", Command: "incident-tests.v1", InputDigest: input.Digest}
			replay := verifier.Reexecutor{Tests: map[string]verifier.TestInput{recipe.Command: input}, Runner: oracleSandbox(t)}
			raw, p, r := proofForObservation(t, "test_run", recipe, expected, expected)
			v := (verifier.Engine{Replayer: replay}).Check(context.Background(), raw, p, r)
			if v.Status != want {
				t.Fatal(v)
			}
			if fixed {
				wrong := input
				wrong.Toolchain = "go version go0.0.0 linux/amd64"
				replay.Tests[recipe.Command] = wrong
				if result := (verifier.Engine{Replayer: replay}).Check(context.Background(), raw, p, r); result.Status != "unverifiable" {
					t.Fatal("wrong toolchain accepted", result)
				}
				replay.Tests[recipe.Command] = input
				if e := os.WriteFile(filepath.Join(input.Directory, "aggregate.go"), []byte("unfixed replacement"), 0600); e != nil {
					t.Fatal(e)
				}
				v = (verifier.Engine{Replayer: replay}).Check(context.Background(), raw, p, r)
				if v.Status != "unverifiable" {
					t.Fatal("changed source accepted", v)
				}
			}
		})
	}
}

func oracleSandbox(t *testing.T) policy.Sandbox {
	t.Helper()
	raw, e := os.ReadFile("../../.local/sandbox-image.id")
	if e != nil {
		t.Fatal("NOT_RUN: isolated Docker image required")
	}
	return policy.Sandbox{Image: strings.TrimSpace(string(raw)), Timeout: 30 * time.Second, MaxOutput: 1024 * 1024}
}
