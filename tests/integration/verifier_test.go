package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/testdb"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/jackc/pgx/v5"
)

func verifierFixture(t *testing.T) (*store.Store, string, store.OrchestratorClaim, *artifact.CAS, verifier.Evidence, verifier.Requirement) {
	t.Helper()
	s := testdb.New(t)
	ctx := context.Background()
	run, c := planFixture(t, s)
	if _, e := s.ProposeAndCommit(ctx, run, c, simplePlan()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshReady(ctx, run, c); e != nil {
		t.Fatal(e)
	}
	worker, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "producer", Role: "research"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "cas")
	cas, e := artifact.New(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cas.Close() })
	t.Setenv("MGAR_TEST_CAS_ROOT", root)
	snap, e := s.BuildSnapshot(ctx, worker, "mock.v1", "research.v1", cas)
	if e != nil {
		t.Fatal(e)
	}
	worker, e = s.StartTask(ctx, worker)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishArtifact(ctx, worker, cas, "finding", "text/plain", "research.v1", []byte("independent fixed input"))
	if e != nil {
		t.Fatal(e)
	}
	observed := verifier.Observation{Hash: a.Hash}
	recipe := verifier.Recipe{Method: "cas_hash"}
	evidence := verifier.Evidence{Schema: "evidence.v1", Claim: "hash matches fixed bytes", Kind: "file_hash", ArtifactID: a.ID, Hash: a.Hash, RunID: run, TaskID: worker.TaskID, PlanVersion: 1, SnapshotID: snap.ID, ToolVersion: a.ToolVersion, ExecutionStatus: "succeeded", Recipe: recipe, Observed: observed, Expected: observed}
	requirement := verifier.Requirement{Kind: evidence.Kind, ToolVersion: evidence.ToolVersion, Recipe: recipe, Expected: observed}
	return s, run, c, cas, evidence, requirement
}
func verifierRaw(t *testing.T, e verifier.Evidence) []byte {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestVerifierEvidencePersistentVerdicts(t *testing.T) {
	for _, mode := range []string{"valid", "forged", "unverifiable", "rollback", "authority", "foreign", "old_source"} {
		t.Run(mode, func(t *testing.T) {
			s, run, c, cas, e, r := verifierFixture(t)
			ctx := context.Background()
			id := e.ArtifactID
			want := "passed"
			if mode == "authority" {
				bad := c
				bad.Owner = "producer"
				if _, err := s.VerifyArtifact(ctx, run, bad, id, verifierRaw(t, e), r, cas, verifier.Engine{}); !errors.Is(err, store.ErrFenced) {
					t.Fatal(err)
				}
				return
			}
			if mode == "foreign" {
				other, owner := planFixture(t, s)
				if _, err := s.VerifyArtifact(ctx, other, owner, id, verifierRaw(t, e), r, cas, verifier.Engine{}); !errors.Is(err, store.ErrPolicy) {
					t.Fatal(err)
				}
				return
			}
			if mode == "old_source" {
				p := simplePlan()
				p.BaseVersion = 1
				p.Nodes[0].Goal = "new generation"
				if _, err := s.ProposeAndCommit(ctx, run, c, p); err != nil {
					t.Fatal(err)
				}
				if _, err := s.VerifyArtifact(ctx, run, c, id, verifierRaw(t, e), r, cas, verifier.Engine{}); !errors.Is(err, store.ErrPolicy) {
					t.Fatal(err)
				}
				return
			}
			if mode == "forged" {
				e.Hash = artifact.Digest([]byte("forged"))
				want = "failed"
			}
			if mode == "unverifiable" {
				e.ExecutionStatus = "unknown"
				want = "unverifiable"
			}
			if mode == "rollback" {
				if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION reject_verdict() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='verdict_recorded' THEN RAISE EXCEPTION 'synthetic outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_verdict BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_verdict()`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.VerifyArtifact(ctx, run, c, id, verifierRaw(t, e), r, cas, verifier.Engine{}); err == nil {
					t.Fatal("partial verdict accepted")
				}
				var n int
				if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifact_verdicts WHERE artifact_id=$1", id).Scan(&n); err != nil || n != 0 {
					t.Fatal(n, err)
				}
				if _, err := s.Pool.Exec(ctx, "DROP TRIGGER reject_verdict ON outbox_events"); err != nil {
					t.Fatal(err)
				}
			}
			v, err := s.VerifyArtifact(ctx, run, c, id, verifierRaw(t, e), r, cas, verifier.Engine{})
			if err != nil || v.Status != want {
				t.Fatal(v, err)
			}
			var passed bool
			var status string
			if err = s.Pool.QueryRow(ctx, `SELECT v.passed,ev.payload->'verdict'->>'status' FROM artifact_verdicts v JOIN workflow_events ev ON ev.entity_id=v.verdict_id WHERE v.artifact_id=$1`, id).Scan(&passed, &status); err != nil || passed != (want == "passed") || status != want {
				t.Fatal(passed, status, err)
			}
			for _, sql := range []string{"UPDATE artifact_verdicts SET passed=true WHERE artifact_id=$1", "DELETE FROM artifact_verdicts WHERE artifact_id=$1"} {
				if _, err = s.Pool.Exec(ctx, sql, id); err == nil {
					t.Fatal("mutable verdict")
				}
			}
			if err = s.ApplyVerdicts(ctx, run, c); err != nil {
				t.Fatal(err)
			}
			w, err := s.FinalizeWorkflow(ctx, run, c)
			if err != nil {
				t.Fatal(err)
			}
			if want == "passed" {
				if w.State != "verifying" {
					t.Fatal(w)
				}
				w, err = s.FinalizeWorkflow(ctx, run, c)
				if err != nil || w.State != "succeeded" {
					t.Fatal(w, err)
				}
			} else if w.State != "failed" {
				t.Fatal("unproved success", w)
			}
		})
	}
}

type verifierReplay func(context.Context, verifier.Recipe) (verifier.Observation, error)

func (f verifierReplay) Replay(ctx context.Context, r verifier.Recipe) (verifier.Observation, error) {
	return f(ctx, r)
}
func TestVerifierEvidenceRechecksAfterIndependentWork(t *testing.T) {
	s, run, c, cas, e, r := verifierFixture(t)
	ctx := context.Background()
	zero, one := 0, 1
	e.Kind = "test_run"
	e.Recipe = verifier.Recipe{Method: "go_test", Command: "fixture.v1", InputDigest: e.Hash}
	e.Expected = verifier.Observation{ExitCode: &zero, Tests: &one}
	e.Observed = e.Expected
	r = verifier.Requirement{Kind: e.Kind, ToolVersion: e.ToolVersion, Recipe: e.Recipe, Expected: e.Expected}
	calls := 0
	engine := verifier.Engine{Replayer: verifierReplay(func(ctx context.Context, _ verifier.Recipe) (verifier.Observation, error) {
		calls++
		// This must acquire workflow locks successfully: no DB transaction may be
		// held across independent execution. It also invalidates the in-flight proof.
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		p := simplePlan()
		p.BaseVersion = 1
		p.Nodes[0].Goal = "changed during check"
		_, err := s.ProposeAndCommit(short, run, c, p)
		return e.Expected, err
	})}
	if _, err := s.VerifyArtifact(ctx, run, c, e.ArtifactID, verifierRaw(t, e), r, cas, engine); !errors.Is(err, store.ErrStaleInput) {
		t.Fatal("stale verification committed", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM artifact_verdicts WHERE artifact_id=$1", e.ArtifactID).Scan(&n); err != nil || n != 0 || calls != 1 {
		t.Fatal(n, calls, err)
	}
}
func TestVerifierEvidenceConcurrentHistory(t *testing.T) {
	s, run, _, _, e, r := verifierFixture(t)
	t.Setenv("MGAR_TEST_EVIDENCE", string(verifierRaw(t, e)))
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MGAR_TEST_REQUIREMENT", string(raw))
	for _, out := range processBatch(t, s, "verifier", run, 2) {
		if !strings.Contains(out, "RESULT passed") {
			t.Fatal(out)
		}
	}
	var verdicts, events int
	if err = s.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM artifact_verdicts WHERE artifact_id=$1),(SELECT count(*) FROM workflow_events WHERE run_id=$2 AND kind='verdict_recorded')`, e.ArtifactID, run).Scan(&verdicts, &events); err != nil || verdicts != 2 || events != 2 {
		t.Fatal(verdicts, events, err)
	}
}

func TestVerifierWorkerCredentialCannotWriteVerdict(t *testing.T) {
	s, run, _, _, e, _ := verifierFixture(t)
	ctx := context.Background()
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	role := s.Pool.Config().ConnConfig.RuntimeParams["search_path"] + "_worker"
	ident := pgx.Identifier{role}.Sanitize()
	password := hex.EncodeToString(token)
	// A fresh login on the synthetic local PG instance, with only mgar_worker
	// membership. Neither privileged DSN nor plaintext password enters evidence.
	if _, err := s.Pool.Exec(ctx, "CREATE ROLE "+ident+" LOGIN PASSWORD '"+password+"' IN ROLE mgar_worker"); err != nil {
		t.Fatal("could not create owned worker login")
	}
	t.Cleanup(func() {
		_, err := s.Pool.Exec(context.Background(), "DROP ROLE "+ident)
		if err != nil {
			t.Error("owned login cleanup failed")
		}
	})
	cfg, err := pgx.ParseConfig(testdb.DSN(t))
	if err != nil {
		t.Fatal("synthetic config")
	}
	cfg.User = role
	cfg.Password = password
	cfg.RuntimeParams["search_path"] = s.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("restricted worker login failed")
	}
	defer conn.Close(ctx)
	var current string
	if err = conn.QueryRow(ctx, "SELECT current_user").Scan(&current); err != nil || current != role {
		t.Fatal("worker identity mismatch")
	}
	if _, err = conn.Exec(ctx, "INSERT INTO artifact_verdicts(artifact_id,checker_version,passed) VALUES($1,'forged',true)", e.ArtifactID); err == nil {
		t.Fatal("worker forged verifier success")
	}
	if _, err = conn.Exec(ctx, "UPDATE workflow_runs SET state='succeeded',finished_at=clock_timestamp() WHERE run_id=$1", run); err == nil {
		t.Fatal("worker bypassed finalization")
	}
	if _, err = conn.Exec(ctx, "SET ROLE mgar_reader"); err == nil {
		t.Fatal("worker elevated role")
	}
}
