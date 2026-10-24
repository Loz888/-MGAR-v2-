package main

import (
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"github.com/Loz888/-MGAR-v2-/migrations"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type doctorOutput struct {
	data     []byte
	overflow bool
}

func (b *doctorOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type doctorReport struct {
	Schema  string        `json:"schema_version"`
	Status  string        `json:"status"`
	Checks  []doctorCheck `json:"checks"`
	Actions []string      `json:"actions_taken"`
}

func doctor(ctx context.Context, out io.Writer, s *store.Store) int {
	r := doctorReport{Schema: "doctor.v1", Status: "ok", Checks: []doctorCheck{}, Actions: []string{}}
	add := func(name string, ok bool, detail string) {
		status := "ok"
		if !ok {
			status = "failed"
			r.Status = "degraded"
		}
		r.Checks = append(r.Checks, doctorCheck{name, status, detail})
	}
	_, e := config.Load(os.Getenv)
	add("configuration", e == nil, "mock_provider_and_synthetic_loopback_database_required")
	if s == nil && e == nil {
		local, err := store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
		e = err
		if err == nil {
			s = local
			defer local.Pool.Close()
		}
	}
	if s == nil {
		add("database", false, "unavailable")
	} else {
		tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			add("database", false, "unavailable")
		} else {
			defer tx.Rollback(ctx)
			if diagnosticACL(ctx, tx) != nil {
				add("database_identity", false, "not_authorized")
			} else {
				add("database_identity", true, "trusted_diagnostic_role")
				expected := map[string]string{"001_control": digestID(migrations.ControlSQL), "002_artifact_cas": digestID(migrations.ArtifactSQL), "003_context_snapshots": digestID(migrations.ContextSQL), "004_plan_node_configs": digestID(migrations.PlanConfigSQL), "005_global_budgets": digestID(migrations.BudgetSQL), "006_resource_admission": digestID(migrations.ResourceSQL)}
				rows, err := tx.Query(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
				valid := err == nil
				seen := 0
				if err == nil {
					for rows.Next() {
						var version, hash string
						if rows.Scan(&version, &hash) != nil {
							valid = false
							break
						}
						seen++
						if expected[version] != hash {
							valid = false
						}
					}
					rows.Close()
					if rows.Err() != nil {
						valid = false
					}
				}
				add("schema_checksums", valid && seen == len(expected), "expected_6_immutable_migrations")
				var unknown int
				if e := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM model_calls WHERE status='unknown')+(SELECT count(*) FROM tool_invocations WHERE status='unknown')+(SELECT count(*) FROM resource_requests WHERE state='unknown' OR (state='held' AND deadline<=statement_timestamp()))").Scan(&unknown); e != nil {
					add("uncertain_execution", false, "not_observed")
				} else if unknown > 0 {
					add("uncertain_execution", false, "unknown_execution_requires_manual_reconciliation")
				} else {
					add("uncertain_execution", true, "no_recorded_unknown_execution")
				}
			}
		}
	}
	root := os.Getenv("MGAR_ARTIFACT_ROOT")
	if root == "" {
		add("cas_root", false, "not_configured")
	} else {
		cas, e := artifact.OpenReadOnly(root)
		add("cas_root", e == nil, "private_existing_directory_required")
		if e == nil {
			_ = cas.Close()
		}
	}
	checks := []struct {
		name     string
		args     []string
		pattern  string
		required string
	}{
		{"go", []string{"go", "version"}, `^go version (go[0-9]+\.[0-9]+\.[0-9]+) `, "go1.27.3"},
		{"python", []string{"python", "--version"}, `^Python (3\.12\.[0-9]+)$`, ""},
		{"docker", []string{"docker", "--host", "unix:///var/run/docker.sock", "version", "--format", "{{.Server.Version}}"}, `^([0-9]+\.[0-9]+\.[0-9]+)$`, ""},
	}
	for _, check := range checks {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		cmd := exec.CommandContext(bounded, check.args[0], check.args[1:]...)
		if check.name == "docker" {
			env := []string{}
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				switch key {
				case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
					continue
				}
				env = append(env, item)
			}
			cmd.Env = env
		}
		var boundedOutput doctorOutput
		cmd.Stdout = &boundedOutput
		cmd.Stderr = io.Discard
		err := cmd.Run()
		cancel()
		match := regexp.MustCompile(check.pattern).FindStringSubmatch(strings.TrimSpace(string(boundedOutput.data)))
		ok := err == nil && !boundedOutput.overflow && len(match) == 2
		detail := "unavailable_or_unsupported"
		if ok {
			detail = match[1]
			ok = check.required == "" || detail == check.required
		}
		add(check.name, ok, detail)
	}
	if r.Status != "ok" {
		_ = encode(out, r)
		return 1
	}
	return encode(out, r)
}
