package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnverifiable = errors.New("UNVERIFIABLE_FIXED_INPUT_OR_EXECUTION")
var snapshotName = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{8}-[0-9]+$`)

type SQLJob struct {
	Pool                                                  *pgxpool.Pool
	Dataset, Snapshot, InputDigest, FingerprintSQL, Query string
}
type TestInput struct {
	Directory, Digest, Toolchain, Package string
	Tests                                 []string
}
type TestExecution struct {
	Output            []byte
	ExitCode          int
	Toolchain, Reason string
}

// SandboxRunner collects real execution results. P5-02 tests use only frozen
// trusted source; P5-04 supplies the uncredentialed Docker implementation.
type SandboxRunner interface {
	Run(context.Context, TestInput) (TestExecution, error)
}
type Reexecutor struct {
	SQL    map[string]SQLJob
	Tests  map[string]TestInput
	Runner SandboxRunner
}

func (r Reexecutor) Replay(ctx context.Context, recipe Recipe) (Observation, error) {
	allowed := Recipe{Method: recipe.Method, Dataset: recipe.Dataset, Command: recipe.Command, InputDigest: recipe.InputDigest}
	if !reflect.DeepEqual(recipe, allowed) || !hash.MatchString(recipe.InputDigest) || recipe.Command == "" {
		return Observation{}, ErrUnverifiable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch recipe.Method {
	case "readonly_sql":
		job, ok := r.SQL[recipe.Command]
		if !ok || job.Pool == nil || job.Dataset == "" || job.Dataset != recipe.Dataset || job.InputDigest != recipe.InputDigest || !snapshotName.MatchString(job.Snapshot) || job.Query == "" || job.FingerprintSQL == "" {
			return Observation{}, ErrUnverifiable
		}
		return replaySQL(ctx, job)
	case "go_test":
		job, ok := r.Tests[recipe.Command]
		if !ok || r.Runner == nil || recipe.Dataset != "" || job.Digest != recipe.InputDigest || job.Toolchain == "" || job.Package == "" || len(job.Tests) == 0 || len(job.Tests) > 64 {
			return Observation{}, ErrUnverifiable
		}
		digest, e := SourceDigest(job.Directory)
		if e != nil || digest != job.Digest {
			return Observation{}, ErrUnverifiable
		}
		execution, e := r.Runner.Run(ctx, job)
		if e != nil || execution.Reason != "" || execution.Toolchain != job.Toolchain || len(execution.Output) > 1024*1024 {
			return Observation{}, ErrUnverifiable
		}
		// The runner must pin/copy the source before executing it; a second digest
		// also detects ordinary mutation during execution at this contract boundary.
		digest, e = SourceDigest(job.Directory)
		if e != nil || digest != job.Digest {
			return Observation{}, ErrUnverifiable
		}
		return testObservation(job, execution)
	default:
		return Observation{}, ErrUnverifiable
	}
}
func replaySQL(ctx context.Context, job SQLJob) (Observation, error) {
	tx, e := job.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Observation{}, ErrUnverifiable
	}
	defer tx.Rollback(context.Background())
	// Snapshot is a trusted registry value and must match the restricted syntax;
	// SET TRANSACTION does not accept a bind parameter. No live-query fallback.
	if _, e = tx.Exec(ctx, "SET TRANSACTION SNAPSHOT '"+job.Snapshot+"'"); e != nil {
		return Observation{}, ErrUnverifiable
	}
	if _, e = tx.Exec(ctx, "SET LOCAL statement_timeout='1000ms'; SET LOCAL lock_timeout='300ms'; SET LOCAL idle_in_transaction_session_timeout='3000ms'"); e != nil {
		return Observation{}, ErrUnverifiable
	}
	var elevated bool
	if e = tx.QueryRow(ctx, "SELECT rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname=current_user").Scan(&elevated); e != nil || elevated {
		return Observation{}, ErrUnverifiable
	}
	inputs, e := queryRows(ctx, tx, job.FingerprintSQL)
	if e != nil {
		return Observation{}, ErrUnverifiable
	}
	normalized, e := NormalizeRows(inputs)
	if e != nil || artifact.Digest([]byte(normalized)) != job.InputDigest {
		return Observation{}, ErrUnverifiable
	}
	rows, e := queryRows(ctx, tx, job.Query)
	if e != nil {
		return Observation{}, ErrUnverifiable
	}
	var all [][]json.RawMessage
	if json.Unmarshal(rows, &all) != nil {
		return Observation{}, ErrUnverifiable
	}
	n := len(all)
	if e = tx.Commit(ctx); e != nil {
		return Observation{}, ErrUnverifiable
	}
	return Observation{Rows: rows, RowCount: &n}, nil
}
func queryRows(ctx context.Context, tx pgx.Tx, query string) (json.RawMessage, error) {
	rows, e := tx.Query(ctx, query)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := [][]any{}
	for rows.Next() {
		if len(result) >= 256 || len(rows.FieldDescriptions()) > 32 {
			return nil, ErrUnverifiable
		}
		row, e := rows.Values()
		if e != nil {
			return nil, e
		}
		result = append(result, row)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(result)
	if e != nil || len(raw) > MaxEvidenceBytes {
		return nil, ErrUnverifiable
	}
	if _, e = NormalizeRows(raw); e != nil {
		return nil, e
	}
	return raw, nil
}

// NormalizeRows preserves duplicate rows and column order; row order is not a
// fact unless encoded as an ordinal column. JSON numbers compare as rationals.
func NormalizeRows(raw json.RawMessage) (string, error) {
	if len(raw) > MaxEvidenceBytes {
		return "", ErrStructure
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var rows [][]any
	if d.Decode(&rows) != nil || rows == nil || len(rows) > 256 {
		return "", ErrStructure
	}
	if _, e := d.Token(); e != io.EOF {
		return "", ErrStructure
	}
	canonical := []string{}
	for _, row := range rows {
		if row == nil || len(row) > 32 {
			return "", ErrStructure
		}
		cells := []string{}
		for _, cell := range row {
			var value string
			switch v := cell.(type) {
			case nil:
				value = "null"
			case bool:
				value = "bool:" + strconv.FormatBool(v)
			case string:
				if len(v) > 4096 {
					return "", ErrStructure
				}
				value = "string:" + v
			case json.Number:
				number := string(v)
				if len(number) > 128 {
					return "", ErrStructure
				}
				if i := strings.IndexAny(number, "eE"); i >= 0 {
					exponent, e := strconv.Atoi(number[i+1:])
					if e != nil || exponent < -1000 || exponent > 1000 {
						return "", ErrStructure
					}
				}
				n, ok := new(big.Rat).SetString(number)
				if !ok {
					return "", ErrStructure
				}
				value = "number:" + n.RatString()
			default:
				return "", ErrStructure
			}
			cells = append(cells, value)
		}
		encoded, _ := json.Marshal(cells)
		canonical = append(canonical, string(encoded))
	}
	sort.Strings(canonical)
	out, _ := json.Marshal(canonical)
	return string(out), nil
}

// SourceDigest pins every regular source file, including test files. Symlinks,
// special files, hard links and unbounded trees are never accepted as inputs.
func SourceDigest(directory string) (string, error) {
	root, e := os.OpenRoot(directory)
	if e != nil {
		return "", ErrUnverifiable
	}
	defer root.Close()
	entries := map[string]string{}
	total := int64(0)
	visited := 0
	e = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > 1024 || len(path) > 4096 || strings.Count(path, "/") > 16 {
			return ErrUnverifiable
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || len(entries) >= 128 {
			return ErrUnverifiable
		}
		f, e := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return ErrUnverifiable
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink != 1 {
			return ErrUnverifiable
		}
		body, e := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		if e != nil || int64(len(body)) != info.Size() {
			return ErrUnverifiable
		}
		total += int64(len(body))
		if total > 4*1024*1024 {
			return ErrUnverifiable
		}
		entries[path] = artifact.Digest(body)
		return nil
	})
	if e != nil || len(entries) == 0 {
		return "", ErrUnverifiable
	}
	raw, _ := json.Marshal(entries)
	return artifact.Digest(raw), nil
}
func testObservation(job TestInput, result TestExecution) (Observation, error) {
	passed, exit := 0, result.ExitCode
	if exit != 0 {
		return Observation{ExitCode: &exit, Tests: &passed}, nil
	}
	required := map[string]bool{}
	for _, name := range job.Tests {
		if name == "" || required[name] {
			return Observation{}, ErrUnverifiable
		}
		required[name] = true
	}
	started := false
	complete := false
	runs := map[string]bool{}
	done := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(result.Output), []byte("\n")) {
		var e struct{ Action, Package, Test string }
		if json.Unmarshal(line, &e) != nil || e.Package != job.Package || complete {
			return Observation{}, ErrUnverifiable
		}
		switch e.Action {
		case "start":
			if started || e.Test != "" {
				return Observation{}, ErrUnverifiable
			}
			started = true
		case "run":
			if !started || !required[e.Test] || runs[e.Test] {
				return Observation{}, ErrUnverifiable
			}
			runs[e.Test] = true
		case "pass":
			if e.Test == "" {
				if !started || len(done) != len(required) {
					return Observation{}, ErrUnverifiable
				}
				complete = true
			} else {
				if !runs[e.Test] || done[e.Test] {
					return Observation{}, ErrUnverifiable
				}
				done[e.Test] = true
				passed++
			}
		case "output", "pause", "cont":
			if !started {
				return Observation{}, ErrUnverifiable
			}
		default:
			return Observation{}, ErrUnverifiable
		}
	}
	if !complete {
		return Observation{}, ErrUnverifiable
	}
	return Observation{ExitCode: &exit, Tests: &passed}, nil
}
