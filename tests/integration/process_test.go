package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
	"github.com/Loz888/-MGAR-v2-/internal/planner"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func attachedStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(os.Getenv("MGAR_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal("invalid synthetic DB configuration")
	}
	schema := os.Getenv("MGAR_TEST_SCHEMA")
	if !strings.HasPrefix(schema, "mgar_test_") {
		t.Fatal("test schema required")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	return &store.Store{Pool: pool}
}
func TestProcessHelper(t *testing.T) {
	kind := os.Getenv("MGAR_TEST_PROCESS")
	if kind == "" {
		return
	}
	s := attachedStore(t)
	fmt.Println("READY")
	if _, e := bufio.NewReader(os.Stdin).ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	run := os.Getenv("MGAR_TEST_RUN")
	ctx := context.Background()
	switch kind {
	case "replan":
		configs := map[string]inputcontext.ExecutionConfig{}
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CONFIGS")), &configs); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		_, e = s.Replan(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}, []byte(os.Getenv("MGAR_TEST_PROPOSAL")), configs, replanPolicy(), cas)
		if e == nil {
			fmt.Println("RESULT winner")
		} else if errors.Is(e, store.ErrPlanConflict) {
			fmt.Println("RESULT conflict")
		} else {
			t.Fatal(e)
		}
	case "incremental":
		p, e := planner.Decode([]byte(os.Getenv("MGAR_TEST_PROPOSAL")))
		if e != nil {
			t.Fatal(e)
		}
		configs := map[string]inputcontext.ExecutionConfig{}
		if e = json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CONFIGS")), &configs); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		_, _, e = s.ProposeIncremental(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}, p, configs, cas)
		if e == nil {
			fmt.Println("RESULT winner")
		} else if errors.Is(e, store.ErrPlanConflict) {
			fmt.Println("RESULT conflict")
		} else {
			t.Fatal(e)
		}
	case "selection":
		var c store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &c); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		snap, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
		if e != nil {
			t.Fatal(e)
		}
		chosen, e := s.PrepareContext(ctx, c, cas, snap.ID, "duplicate paid orders", "summary", 16384, 4096)
		if e != nil || chosen.Blocked {
			t.Fatal(e)
		}
		fmt.Println("RESULT summary", chosen.Summaries[0].ID)
	case "context":
		var c store.TaskClaim
		if e := json.Unmarshal([]byte(os.Getenv("MGAR_TEST_CLAIM")), &c); e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		snapshot, e := s.BuildSnapshot(ctx, c, "mock.v1", "action.v1", cas)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Println("RESULT snapshot", snapshot.ID, snapshot.Hash())
	case "artifact":
		c, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "publisher-" + os.Getenv("MGAR_TEST_ACTOR"), Role: "research"}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		c, e = s.StartTask(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		cas, e := artifact.New(os.Getenv("MGAR_TEST_CAS_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		defer cas.Close()
		if _, e = s.BuildSnapshot(ctx, c, "mock.v1", "research.v1", cas); e != nil {
			t.Fatal(e)
		}
		a, e := s.PublishArtifact(ctx, c, cas, "finding", "text/plain", "fixture.v1", []byte("same cross-process bytes"))
		if e != nil {
			t.Fatal(e)
		}
		fmt.Println("RESULT published", a.ID, a.Hash)
	case "claim":
		_, e := s.ClaimTask(ctx, run, store.WorkerIdentity{Owner: "worker-" + os.Getenv("MGAR_TEST_ACTOR"), Role: "research"}, time.Minute)
		if e == nil {
			fmt.Println("RESULT winner")
		} else if errors.Is(e, store.ErrNoReady) || errors.Is(e, store.ErrBudget) {
			fmt.Println("RESULT denied")
		} else {
			t.Fatal(e)
		}
	case "ready":
		ready, e := s.RefreshReady(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0})
		if e != nil {
			t.Fatal(e)
		}
		fmt.Println("RESULT ready", len(ready))
	case "plan":
		_, e := s.ProposeAndCommit(ctx, run, store.OrchestratorClaim{Owner: "orchestrator", Epoch: 1, Revision: 0}, simplePlan())
		if e == nil {
			fmt.Println("RESULT winner")
		} else if errors.Is(e, store.ErrPlanConflict) {
			fmt.Println("RESULT conflict")
		} else {
			t.Fatal(e)
		}
	default:
		t.Fatal("unknown process fixture")
	}
}
func processBatch(t *testing.T, s *store.Store, kind, run string, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmds := make([]*exec.Cmd, n)
	readers := make([]*bufio.Reader, n)
	writers := make([]io.WriteCloser, n)
	for i := 0; i < n; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
		cmd.Env = append(os.Environ(), "MGAR_TEST_PROCESS="+kind, "MGAR_TEST_RUN="+run, "MGAR_TEST_SCHEMA="+s.Pool.Config().ConnConfig.RuntimeParams["search_path"], "MGAR_TEST_ACTOR="+strconv.Itoa(i))
		out, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		writers[i], e = cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		cmds[i] = cmd
		readers[i] = bufio.NewReader(out)
	}
	for _, r := range readers {
		line, e := r.ReadString('\n')
		if e != nil || line != "READY\n" {
			t.Fatalf("child startup %q %v", line, e)
		}
	}
	for _, w := range writers {
		if _, e := io.WriteString(w, "START\n"); e != nil {
			t.Fatal(e)
		}
		w.Close()
	}
	outputs := []string{}
	for i, cmd := range cmds {
		out, e := io.ReadAll(readers[i])
		if e != nil {
			t.Fatal(e)
		}
		if e = cmd.Wait(); e != nil {
			t.Fatalf("child failure %s %v", out, e)
		}
		outputs = append(outputs, string(out))
	}
	return outputs
}
