package integration

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"strings"
	"testing"
)

func TestReviewerAuditCannotRewriteVerdict(t *testing.T) {
	for _, mode := range []string{"valid", "force_pass", "foreign", "authority", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, run, c, cas, evidence, requirement := verifierFixture(t)
			ctx := context.Background()
			evidence.Hash = artifact.Digest([]byte("forged"))
			v, e := s.VerifyArtifact(ctx, run, c, evidence.ArtifactID, verifierRaw(t, evidence), requirement, cas, verifier.Engine{})
			if e != nil || v.Status != "failed" {
				t.Fatal(v, e)
			}
			var id string
			if e = s.Pool.QueryRow(ctx, "SELECT verdict_id FROM artifact_verdicts WHERE artifact_id=$1", evidence.ArtifactID).Scan(&id); e != nil {
				t.Fatal(e)
			}
			raw := []byte(`{"schema":"review.v1","summary":"synthetic private reviewer prose","additional_checks":["check_boundary_cases"]}`)
			switch mode {
			case "force_pass":
				raw = []byte(`{"schema":"review.v1","summary":"override","additional_checks":[],"status":"passed"}`)
			case "foreign":
				id = "00000000-0000-4000-8000-000000000000"
			case "authority":
				c = store.OrchestratorClaim{Owner: "worker", Epoch: 1}
			case "rollback":
				if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION reject_review() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='review_recorded' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_review BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_review()`); e != nil {
					t.Fatal(e)
				}
			}
			result, e := s.RecordReview(ctx, run, c, id, raw)
			want := 0
			if mode == "valid" {
				want = 1
				if e != nil || result.Verdict.Status != "failed" {
					t.Fatal(result, e)
				}
			} else if e == nil {
				t.Fatal("unsafe review accepted")
			}
			var n, passed, reviews int
			if e = s.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE passed) FROM artifact_verdicts").Scan(&n, &passed); e != nil || n != 1 || passed != 0 {
				t.Fatal(n, passed, e)
			}
			var payload string
			if e = s.Pool.QueryRow(ctx, "SELECT count(*),COALESCE(string_agg(payload::text,''),'') FROM workflow_events WHERE kind='review_recorded'").Scan(&reviews, &payload); e != nil || reviews != want || strings.Contains(payload, "synthetic private reviewer prose") {
				t.Fatal(reviews, e)
			}
		})
	}
}
