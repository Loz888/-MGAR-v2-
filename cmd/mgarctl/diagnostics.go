package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/budget"
	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/observability"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"regexp"
	"time"
)

var cliUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var errNotAuthorized = errors.New("NOT_AUTHORIZED")

func safeCommandError(e error) string {
	for _, known := range []error{store.ErrBadState, store.ErrFenced, store.ErrBudget, store.ErrPolicy, store.ErrUnsafeReplay, store.ErrAlreadyExists, store.ErrCASConflict} {
		if errors.Is(e, known) {
			return known.Error()
		}
	}
	return "COMMAND_FAILED"
}
func problem(out io.Writer, code string, exit int) int {
	_ = json.NewEncoder(out).Encode(map[string]string{"schema_version": "cli_error.v1", "error": code})
	return exit
}
func encode(out io.Writer, value any) int {
	if json.NewEncoder(out).Encode(value) != nil {
		return 1
	}
	return 0
}
func digestID(text string) string { h := sha256.Sum256([]byte(text)); return hex.EncodeToString(h[:]) }
func diagnostic(ctx context.Context, args []string, out, errs io.Writer, s *store.Store) int {
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	format := flags.String("format", "json", "json only")
	asJSON := flags.Bool("json", false, "JSON output")
	scope := flags.String("run", "", "optional artifact run scope")
	id := ""
	tail := args[1:]
	if command != "doctor" {
		if len(args) < 2 || !cliUUID.MatchString(args[1]) {
			return problem(out, "INVALID_ARGUMENT", 2)
		}
		id = args[1]
		tail = args[2:]
	}
	if flags.Parse(tail) != nil || flags.NArg() != 0 || *format != "json" || (*scope != "" && (command != "inspect-artifact" || !cliUUID.MatchString(*scope))) {
		return problem(out, "INVALID_ARGUMENT", 2)
	}
	_ = asJSON
	if command == "doctor" {
		return doctor(ctx, out, s)
	}
	if _, e := config.Load(os.Getenv); e != nil {
		return problem(out, "INVALID_CONFIGURATION", 1)
	}
	if s == nil {
		var e error
		s, e = store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
		if e != nil {
			return problem(out, "DATABASE_UNAVAILABLE", 1)
		}
		defer s.Pool.Close()
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return problem(out, "DATABASE_UNAVAILABLE", 1)
	}
	defer tx.Rollback(ctx)
	if e = diagnosticACL(ctx, tx); e != nil {
		return problem(out, "NOT_AUTHORIZED", 1)
	}
	var result any
	if command == "inspect-artifact" {
		result, e = inspectArtifact(ctx, tx, id, *scope)
	} else if command == "budget-report" {
		result, e = store.BudgetReportTx(ctx, tx, id)
	} else {
		var report diagnosticReport
		report, e = readReport(ctx, tx, id)
		if e == nil {
			if command == "status" {
				report.Audit = nil
			}
			result = report
		}
	}
	if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, artifact.ErrIntegrity) {
		return problem(out, "NOT_FOUND_OR_UNREADABLE", 1)
	}
	if e != nil {
		return problem(out, "DIAGNOSTIC_FAILED", 1)
	}
	if e = tx.Commit(ctx); e != nil {
		return problem(out, "DIAGNOSTIC_FAILED", 1)
	}
	return encode(out, result)
}
func diagnosticACL(ctx context.Context, tx pgx.Tx) error {
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT current_user<>'mgar_worker' AND has_table_privilege(current_user,'workflow_runs','SELECT') AND has_table_privilege(current_user,'task_instances','SELECT') AND has_table_privilege(current_user,'model_calls','SELECT') AND has_table_privilege(current_user,'artifacts','SELECT')`).Scan(&allowed)
	if e != nil || !allowed {
		return errNotAuthorized
	}
	return nil
}

type runInfo struct {
	RunID             string     `json:"run_id"`
	State             string     `json:"state"`
	ActivePlanVersion int        `json:"active_plan_version"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}
type planInfo struct {
	Version        int       `json:"plan_version"`
	Hash           string    `json:"plan_hash"`
	ProposerDigest string    `json:"proposer_digest"`
	CreatedAt      time.Time `json:"created_at"`
}
type artifactInfo struct {
	ID            string `json:"artifact_id"`
	RunID         string `json:"run_id"`
	TaskID        string `json:"producer_task_id"`
	Kind          string `json:"kind"`
	Hash          string `json:"content_sha256"`
	Version       int    `json:"source_plan_version"`
	Size          int64  `json:"byte_length"`
	Verdict       string `json:"latest_verdict"`
	BytesVerified bool   `json:"bytes_verified"`
}
type inputInfo struct {
	TaskID     string `json:"task_id"`
	ArtifactID string `json:"artifact_id"`
	Hash       string `json:"content_sha256"`
	Version    int    `json:"source_plan_version"`
}
type toolInfo struct {
	ID               string `json:"invocation_id"`
	TaskID           string `json:"task_id"`
	CapabilityDigest string `json:"capability_digest"`
	Effect           string `json:"effect"`
	Status           string `json:"status"`
	ResultID         string `json:"result_artifact_id"`
}
type resourceInfo struct {
	Class          string `json:"class"`
	Capacity       int    `json:"capacity"`
	QueueLimit     int    `json:"queue_limit"`
	Queued         int    `json:"queued"`
	Held           int    `json:"held"`
	Unknown        int    `json:"unknown"`
	GlobalOccupied int    `json:"global_occupied"`
}
type diagnosticReport struct {
	runInfo
	Schema        string                    `json:"schema_version"`
	Plans         []planInfo                `json:"plans"`
	Tasks         []observability.AuditTask `json:"tasks"`
	Artifacts     []artifactInfo            `json:"artifacts"`
	Inputs        []inputInfo               `json:"inputs"`
	Tools         []toolInfo                `json:"tools"`
	Resources     []resourceInfo            `json:"resources"`
	Budget        budget.Report             `json:"budget"`
	DeniedActions int                       `json:"denied_tool_actions"`
	Audit         *observability.Audit      `json:"audit,omitempty"`
	Truncated     bool                      `json:"truncated"`
}

const artifactInfoColumns = `a.artifact_id,a.run_id,a.producer_task,a.kind,a.content_sha256,a.source_plan_version,a.byte_length,COALESCE((SELECT CASE WHEN v.passed THEN 'passed' ELSE 'failed' END FROM artifact_verdicts v WHERE v.artifact_id=a.artifact_id ORDER BY v.created_at DESC,v.verdict_id DESC LIMIT 1),'unverifiable')`

func scanArtifact(row pgx.Row) (artifactInfo, error) {
	var a artifactInfo
	e := row.Scan(&a.ID, &a.RunID, &a.TaskID, &a.Kind, &a.Hash, &a.Version, &a.Size, &a.Verdict)
	return a, e
}
func inspectArtifact(ctx context.Context, tx pgx.Tx, id, run string) (artifactInfo, error) {
	a, e := scanArtifact(tx.QueryRow(ctx, "SELECT "+artifactInfoColumns+" FROM artifacts a WHERE a.artifact_id=$1 AND ($2::text='' OR a.run_id::text=$2)", id, run))
	if e != nil {
		return a, e
	}
	root := os.Getenv("MGAR_ARTIFACT_ROOT")
	if root == "" {
		return artifactInfo{}, artifact.ErrIntegrity
	}
	cas, e := artifact.OpenReadOnly(root)
	if e != nil {
		return artifactInfo{}, artifact.ErrIntegrity
	}
	defer cas.Close()
	if _, e = cas.Read(a.Hash, a.Size); e != nil {
		return artifactInfo{}, artifact.ErrIntegrity
	}
	a.BytesVerified = true
	return a, nil
}
func readReport(ctx context.Context, tx pgx.Tx, run string) (diagnosticReport, error) {
	r := diagnosticReport{Schema: "run_diagnostics.v1", Plans: []planInfo{}, Artifacts: []artifactInfo{}, Inputs: []inputInfo{}, Tools: []toolInfo{}, Resources: []resourceInfo{}}
	e := tx.QueryRow(ctx, "SELECT run_id,state,active_plan_version,revision,created_at,finished_at FROM workflow_runs WHERE run_id=$1", run).Scan(&r.RunID, &r.State, &r.ActivePlanVersion, &r.Revision, &r.CreatedAt, &r.FinishedAt)
	if e != nil {
		return r, e
	}
	a, e := observability.ReadAuditTx(ctx, tx, run)
	if e != nil {
		return r, e
	}
	r.Audit = &a
	r.Tasks = a.Tasks
	r.Truncated = a.Truncated
	r.Budget, e = store.BudgetReportTx(ctx, tx, run)
	if e != nil {
		return r, e
	}
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM workflow_events WHERE run_id=$1 AND kind='tool_denied'", run).Scan(&r.DeniedActions); e != nil {
		return r, e
	}
	rows, e := tx.Query(ctx, "SELECT plan_version,plan_hash,proposer,created_at FROM plans WHERE run_id=$1 ORDER BY plan_version LIMIT 4097", run)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var p planInfo
		var proposer string
		if e = rows.Scan(&p.Version, &p.Hash, &proposer, &p.CreatedAt); e != nil {
			rows.Close()
			return r, e
		}
		p.ProposerDigest = digestID(proposer)
		r.Plans = append(r.Plans, p)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return r, e
	}
	rows, e = tx.Query(ctx, "SELECT "+artifactInfoColumns+" FROM artifacts a WHERE a.run_id=$1 ORDER BY a.created_at,a.artifact_id LIMIT 4097", run)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		a, e := scanArtifact(rows)
		if e != nil {
			rows.Close()
			return r, e
		}
		r.Artifacts = append(r.Artifacts, a)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return r, e
	}
	rows, e = tx.Query(ctx, "SELECT task_id,artifact_id,content_sha256,source_plan_version FROM task_inputs WHERE run_id=$1 ORDER BY task_id,artifact_id LIMIT 4097", run)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var in inputInfo
		if e = rows.Scan(&in.TaskID, &in.ArtifactID, &in.Hash, &in.Version); e != nil {
			rows.Close()
			return r, e
		}
		r.Inputs = append(r.Inputs, in)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return r, e
	}
	rows, e = tx.Query(ctx, `SELECT v.invocation_id,v.task_id,v.capability,v.effect_class,v.status,COALESCE(v.result_artifact_id::text,'') FROM tool_invocations v JOIN task_instances t USING(task_id) WHERE t.run_id=$1 ORDER BY v.invocation_id LIMIT 4097`, run)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var tool toolInfo
		var name string
		if e = rows.Scan(&tool.ID, &tool.TaskID, &name, &tool.Effect, &tool.Status, &tool.ResultID); e != nil {
			rows.Close()
			return r, e
		}
		tool.CapabilityDigest = digestID(name)
		r.Tools = append(r.Tools, tool)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return r, e
	}
	rows, e = tx.Query(ctx, `SELECT p.class,p.capacity,p.queue_limit,count(q.request_id) FILTER(WHERE q.run_id=$1 AND q.state='queued' AND q.deadline>statement_timestamp()),count(q.request_id) FILTER(WHERE q.run_id=$1 AND q.state='held'),count(q.request_id) FILTER(WHERE q.run_id=$1 AND q.state='unknown'),count(q.request_id) FILTER(WHERE q.state IN ('held','unknown')) FROM resource_pools p LEFT JOIN resource_requests q USING(class) GROUP BY p.class ORDER BY p.class`, run)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var resource resourceInfo
		if e = rows.Scan(&resource.Class, &resource.Capacity, &resource.QueueLimit, &resource.Queued, &resource.Held, &resource.Unknown, &resource.GlobalOccupied); e != nil {
			rows.Close()
			return r, e
		}
		r.Resources = append(r.Resources, resource)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return r, e
	}
	r.Truncated = r.Truncated || len(r.Plans) > 4096 || len(r.Artifacts) > 4096 || len(r.Inputs) > 4096 || len(r.Tools) > 4096
	return r, nil
}
