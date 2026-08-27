// Package testsupport provides shared, real-infra-backed fixtures for
// the orchestrator's integration tests. It exists because PLAN.md's
// Phase 1 completion gate requires actual behavioral coverage of the
// Postgres-coupled critical services (reaper, warm pool) that the
// codebase's long-standing "no pgx fake anywhere" rule (see
// internal/audit/audit_test.go's doc comment) otherwise leaves untested
// below the pure-logic line.
//
// The rule isn't being repealed -- pure decision logic still gets plain
// unit tests with no DB. This package is the deliberate exception for
// the loop-orchestration paths (sweep/fill) whose whole behavior IS the
// sequence of SQL statements they run: those are tested against a real,
// ephemeral Postgres via testcontainers-go, gated behind a Docker
// availability check with t.Skipf (the same "skip, loudly, when the
// real dependency isn't here" convention every other real-infra test in
// this repo uses -- see internal/k8s/provision_t2_live_test.go).
package testsupport

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NewPostgres starts a throwaway Postgres 16 container, applies the
// env/billing schema definitions plus every orchestrator migration
// under orchestrator/db/migrations, and returns a connected pool. The
// container and pool are torn down automatically via t.Cleanup.
//
// It calls t.Skipf (not t.Fatalf) when Docker is unreachable, so a
// developer or CI runner without a Docker daemon still gets a green run
// for everything else -- matching this repo's existing real-infra test
// gating.
func NewPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if os.Getenv("ORCHESTRATOR_SKIP_TESTCONTAINERS") != "" {
		t.Skip("skipping: ORCHESTRATOR_SKIP_TESTCONTAINERS is set")
	}

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx,
		"postgres:16",
		tcpostgres.WithDatabase("practice_engine_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		// A missing/unreachable Docker daemon surfaces here as a dial
		// error. Skip rather than fail -- same rationale as the k8s
		// live tests' t.Skipf on an unreachable cluster.
		t.Skipf("skipping: could not start Postgres test container (Docker not available?): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// Give Postgres a moment past the log line to actually accept
	// connections on the mapped port.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pool.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres never became pingable: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	applySchema(t, ctx, pool)
	applyMigrations(t, ctx, pool)

	return pool
}

// applySchema creates the bounded-context schemas the orchestrator's
// migrations assume already exist. In the real stack these come from
// practice-core/db/migrations/0000_schemas.sql (Dev B owns that file);
// here we only need the two Dev A schemas plus the extension the
// migrations' DEFAULT gen_random_uuid() calls depend on.
func applySchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,
		`CREATE SCHEMA IF NOT EXISTS env`,
		`CREATE SCHEMA IF NOT EXISTS billing`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("applySchema %q: %v", s, err)
		}
	}
}

// applyMigrations runs every *.sql file under orchestrator/db/migrations
// in lexical order -- the same order the local dev stack applies them.
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	dir := migrationsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading migrations dir %s: %v", dir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("applying migration %s: %v", name, err)
		}
	}
}

// migrationsDir resolves orchestrator/db/migrations relative to this
// source file, so tests work regardless of the package they're invoked
// from.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = .../orchestrator/internal/testsupport/postgres.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "db", "migrations")
}
