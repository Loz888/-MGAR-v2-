package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, nil)) }

// The injected Store lets command tests exercise the real PostgreSQL protocol
// inside a disposable schema; the executable always opens its configured DB.
func run(ctx context.Context, args []string, out, errs io.Writer, s *store.Store) int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	usage := func() int {
		fmt.Fprintln(errs, "usage: mgarctl health | submit [--fixture incident --provider mock --run-id UUID --max-delegations 24] | status/cancel/resume RUN_ID | approve APPROVAL_ID --action-digest SHA256 [--deny]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "health" {
		if len(args) != 1 {
			return usage()
		}
		if err = json.NewEncoder(out).Encode(map[string]string{"status": "ok", "provider": cfg.Provider}); err != nil {
			return 1
		}
		return 0
	}
	if args[0] == "approve" {
		return approve(ctx, args[1:], out, errs, s)
	}
	var id string
	budget := 24
	switch args[0] {
	case "submit":
		flags := flag.NewFlagSet("submit", flag.ContinueOnError)
		flags.SetOutput(errs)
		fixture := flags.String("fixture", "incident", "offline plan fixture")
		provider := flags.String("provider", "mock", "mock only")
		flags.StringVar(&id, "run-id", "", "optional unique UUID")
		flags.IntVar(&budget, "max-delegations", 24, "finite delegation limit")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *fixture != "incident" || *provider != "mock" || budget < 1 || budget > 1024 {
			return usage()
		}
	case "status", "cancel", "resume":
		if len(args) != 2 {
			return usage()
		}
		id = args[1]
	default:
		return usage()
	}
	if s == nil {
		s, err = store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
		if err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		defer s.Pool.Close()
	}
	var w store.Workflow
	switch args[0] {
	case "submit":
		var c store.OrchestratorClaim
		w, c, err = s.CreateRunClaimed(ctx, id, "Offline incident workflow fixture", budget, "cli-submit-"+fmt.Sprint(os.Getpid()), 30*time.Second)
		if err == nil {
			// Each durable checkpoint is recoverable; no Agent/model executes here.
			fmt.Fprintln(errs, "created run", w.RunID)
			_, err = s.BeginPlanning(ctx, w.RunID, c)
			if err == nil {
				_, err = s.ProposeAndCommit(ctx, w.RunID, c, incidentPlan())
			}
			if err == nil {
				err = s.RelinquishOrchestrator(ctx, w.RunID, c)
			}
			if err == nil {
				w, err = s.Workflow(ctx, w.RunID)
			}
		}
	case "status":
		w, err = s.Workflow(ctx, id)
	case "cancel":
		w, err = s.Workflow(ctx, id)
		if err == nil {
			w, err = s.CancelWorkflow(ctx, id, w.Revision)
		}
	case "resume":
		var c store.OrchestratorClaim
		c, err = s.TakeOrchestrator(ctx, id, "cli-resume-"+fmt.Sprint(os.Getpid()), 30*time.Second)
		if err == nil {
			w, err = s.ResumeWorkflow(ctx, id, c)
			// A refused resume still relinquishes this CLI's lease.
			err = errors.Join(err, s.RelinquishOrchestrator(ctx, id, c))
		}
	}
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	if err = json.NewEncoder(out).Encode(w); err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	return 0
}

func incidentPlan() planner.Proposal {
	return planner.Proposal{
		SchemaVersion: "plan.v1", BaseVersion: 0, Rationale: "Offline control-plane fan-out/fan-in fixture; awaits future workers and independent verification",
		Nodes: []planner.Node{
			{Key: "A", Role: "research", Goal: "Collect fixture findings", RequiredInputs: []string{}, OutputKind: "finding"},
			{Key: "B", Role: "data", Goal: "Collect fixture numeric evidence", RequiredInputs: []string{}, OutputKind: "sql_evidence"},
			{Key: "C", Role: "aggregate", Goal: "Combine independently verified inputs", RequiredInputs: []string{"A", "B"}, OutputKind: "report"},
		},
		Edges: []planner.Edge{{From: "A", To: "C"}, {From: "B", To: "C"}},
	}
}

// approve is local-only: the configured control-plane DB session supplies the
// approver identity. It returns a one-use token without executing the action.
func approve(ctx context.Context, args []string, out, errs io.Writer, s *store.Store) int {
	if len(args) < 3 {
		fmt.Fprintln(errs, "usage: mgarctl approve APPROVAL_ID --action-digest SHA256 [--deny]")
		return 2
	}
	id := args[0]
	flags := flag.NewFlagSet("approve", flag.ContinueOnError)
	flags.SetOutput(errs)
	digest := flags.String("action-digest", "", "exact reviewed action digest")
	deny := flags.Bool("deny", false, "deny this exact action")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(*digest) {
		return 2
	}
	if s == nil {
		var err error
		s, err = store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
		if err != nil {
			fmt.Fprintln(errs, "control database unavailable")
			return 1
		}
		defer s.Pool.Close()
	}
	decision, err := s.DecideApproval(ctx, id, *digest, !*deny)
	if err != nil {
		fmt.Fprintln(errs, "approval rejected:", err)
		return 1
	}
	if err = json.NewEncoder(out).Encode(decision); err != nil {
		return 1
	}
	return 0
}
