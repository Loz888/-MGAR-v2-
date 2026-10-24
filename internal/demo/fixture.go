// Package demo composes existing authority APIs for owned synthetic experiments.
// It is not a new persistence protocol or a general-purpose worker executor.
package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Order struct {
	EventID int    `json:"event_id"`
	ID      int    `json:"order_id"`
	Status  string `json:"status"`
	Amount  int    `json:"amount"`
}
type Input struct {
	Business  string   `json:"business"`
	Version   int      `json:"input_version"`
	Evidence  string   `json:"evidence"`
	Scope     string   `json:"scope"`
	Orders    []Order  `json:"orders"`
	Prior     []Order  `json:"prior_orders"`
	Documents []string `json:"documents"`
	Goal      string   `json:"goal"`
}
type Options struct{ Root, Case, Fault string }
type fixture struct {
	verification             *verificationTask
	s                        *store.Store
	admin                    *store.Store
	reader                   *pgxpool.Pool
	source                   pgx.Tx
	schema, role, temp, root string
	cas                      *artifact.CAS
	input                    Input
	sql                      verifier.SQLJob
	sandbox                  policy.Sandbox
	original                 map[string]string
}

var caseName = regexp.MustCompile(`^(offline|development)-[0-9]{3}$`)

func newFixture(ctx context.Context, o Options) (_ *fixture, err error) {
	if !caseName.MatchString(o.Case) {
		return nil, errors.New("case_not_authorized")
	}
	f := &fixture{root: o.Root, original: map[string]string{}}
	defer func() {
		if err != nil {
			_ = f.close()
		}
	}()
	raw, e := os.ReadFile(filepath.Join(o.Root, "fixtures/datasets/v1/inputs.json"))
	if e != nil {
		return nil, e
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) != nil || verifier.StrictJSON(all[o.Case], &f.input) != nil {
		return nil, errors.New("invalid_fixture")
	}
	if len(f.input.Orders) < 1 || len(f.input.Orders) > 32 || len(f.input.Documents) > 4 || len(f.input.Goal) > 2048 || (f.input.Business != "incident" && f.input.Business != "analysis") {
		return nil, errors.New("invalid_fixture")
	}
	f.temp, e = os.MkdirTemp("", "mgar-demo-")
	if e != nil {
		return nil, e
	}
	f.cas, e = artifact.New(filepath.Join(f.temp, "cas"))
	if e != nil {
		return nil, e
	}
	f.admin, e = store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
	if e != nil {
		return nil, e
	}
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		return nil, e
	}
	f.schema = "mgar_demo_" + hex.EncodeToString(b[:8])
	ident := pgx.Identifier{f.schema}.Sanitize()
	if _, e = f.admin.Pool.Exec(ctx, "CREATE SCHEMA "+ident); e != nil {
		f.schema = ""
		return nil, e
	}
	cfg := f.admin.Pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = f.schema
	cfg.MaxConns = 12
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	f.s = &store.Store{Pool: pool}
	if e = store.Migrate(ctx, pool); e != nil {
		return nil, e
	}
	if _, e = pool.Exec(ctx, "CREATE TABLE orders(event_id integer PRIMARY KEY,order_id integer NOT NULL,status text NOT NULL,amount integer NOT NULL)"); e != nil {
		return nil, e
	}
	for _, r := range f.input.Orders {
		if _, e = pool.Exec(ctx, "INSERT INTO orders VALUES($1,$2,$3,$4)", r.EventID, r.ID, r.Status, r.Amount); e != nil {
			return nil, e
		}
	}
	f.role = "mgar_demo_reader_" + hex.EncodeToString(b[8:])
	roleSQL := pgx.Identifier{f.role}.Sanitize()
	secret := make([]byte, 24)
	if _, e = rand.Read(secret); e != nil {
		return nil, e
	}
	if _, e = pool.Exec(ctx, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+hex.EncodeToString(secret)+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS"); e != nil {
		f.role = ""
		return nil, errors.New("reader_creation_failed")
	}
	if _, e = pool.Exec(ctx, "GRANT USAGE ON SCHEMA "+ident+" TO "+roleSQL+"; GRANT SELECT ON orders TO "+roleSQL+"; ALTER ROLE "+roleSQL+" SET default_transaction_read_only=on"); e != nil {
		return nil, e
	}
	cfg = f.admin.Pool.Config().Copy()
	cfg.ConnConfig.User = f.role
	cfg.ConnConfig.Password = hex.EncodeToString(secret)
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	cfg.MaxConns = 2
	f.reader, e = pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, errors.New("reader_connection_failed")
	}
	f.source, e = pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	var snapshot string
	if e = f.source.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&snapshot); e != nil {
		return nil, e
	}
	rows := [][]any{}
	for _, r := range f.input.Orders {
		rows = append(rows, []any{r.EventID, r.ID, r.Status, r.Amount})
	}
	body, _ := json.Marshal(rows)
	canonical, e := verifier.NormalizeRows(body)
	if e != nil {
		return nil, e
	}
	f.sql = verifier.SQLJob{Pool: f.reader, Dataset: "orders.v1", Snapshot: snapshot, InputDigest: artifact.Digest([]byte(canonical)), FingerprintSQL: "SELECT event_id,order_id,status,amount FROM " + ident + ".orders", Query: "SELECT COALESCE(sum(amount),0)::bigint,count(*)::bigint,((SELECT count(*) FROM " + ident + ".orders WHERE status='paid')-count(*))::bigint FROM (SELECT DISTINCT order_id,amount FROM " + ident + ".orders WHERE status='paid') d"}
	if e = os.Mkdir(filepath.Join(f.temp, "worktree"), 0700); e != nil {
		return nil, e
	}
	expected := map[string]string{"aggregate.go": "fdb55481f7b24ef7b3b3ca8de27ae224fc8639d9403227c490b0c515274f585b", "aggregate_test.go": "467f3a45b5785ffed04aa1e4a323ba69dfb974b83832802b4a0b5f18f096edcd", "go.mod": "6afc983df3a9d3ae24fa580644b235e0cec3b9cb900ca63a8598fcf95cd4ae16"}
	for name, hash := range expected {
		path := filepath.Join(o.Root, "fixtures/incident/go-repo", name)
		raw, e = os.ReadFile(path)
		if e != nil || artifact.Digest(raw) != hash {
			return nil, errors.New("source_fixture_changed")
		}
		f.original[path] = hash
		if e = os.WriteFile(filepath.Join(f.temp, "worktree", name), raw, 0600); e != nil {
			return nil, e
		}
	}
	if f.input.Business == "incident" {
		raw, e = os.ReadFile(filepath.Join(o.Root, ".local/sandbox-image.id"))
		if e != nil {
			return nil, errors.New("sandbox_image_required")
		}
		f.sandbox = policy.Sandbox{Image: strings.TrimSpace(string(raw)), Timeout: 30 * time.Second, MaxOutput: 1 << 20}
	}
	return f, nil
}
func (f *fixture) unchanged() bool {
	for path, hash := range f.original {
		b, e := os.ReadFile(path)
		if e != nil || artifact.Digest(b) != hash {
			return false
		}
	}
	return true
}
func (f *fixture) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	if f.source != nil {
		_ = f.source.Rollback(ctx)
	}
	if f.reader != nil {
		f.reader.Close()
	}
	if f.s != nil {
		f.s.Pool.Close()
	}
	if f.admin != nil {
		if f.schema != "" {
			_, e := f.admin.Pool.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE")
			errs = append(errs, e)
		}
		if f.role != "" {
			_, e := f.admin.Pool.Exec(ctx, "DROP ROLE "+pgx.Identifier{f.role}.Sanitize())
			errs = append(errs, e)
		}
		f.admin.Pool.Close()
	}
	if f.cas != nil {
		errs = append(errs, f.cas.Close())
	}
	if f.temp != "" {
		errs = append(errs, os.RemoveAll(f.temp))
	}
	return errors.Join(errs...)
}
