package reaper

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/destroyreason"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

// These tests exercise the reaper's actual mechanism -- the sequence of
// SQL it runs against env.environment_reaper -- against a real ephemeral
// Postgres (testsupport.NewPostgres), because that sequence, not any
// extractable pure function, is where the "keeps your cloud bill finite"
// guarantee lives. They skip cleanly when Docker is unavailable.

// recordingDestroyer is a fake DestroyFunc that records calls and can be
// told to fail for a given env id, so the retry-on-failure path is
// observable without a real Destroyer/k8s.
type recordingDestroyer struct {
	mu      sync.Mutex
	calls   []call
	failFor map[string]error
}

type call struct {
	envID  string
	reason string
}

func newRecordingDestroyer() *recordingDestroyer {
	return &recordingDestroyer{failFor: map[string]error{}}
}

func (r *recordingDestroyer) fn(_ context.Context, envID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call{envID, reason})
	if err, ok := r.failFor[envID]; ok {
		return err
	}
	return nil
}

func (r *recordingDestroyer) callsFor(envID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c.envID == envID {
			n++
		}
	}
	return n
}

func (r *recordingDestroyer) totalCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// seedEnvironment inserts an env.environment row (the FK target for
// env.environment_reaper) and returns its id.
func seedEnvironment(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO env.environment (id, attempt_id, namespace, status)
		VALUES ($1, $2, $3, 'READY')
	`, id, uuid.NewString(), "env-"+id)
	if err != nil {
		t.Fatalf("seedEnvironment: %v", err)
	}
	return id
}

func reaperRowExists(t *testing.T, pool *pgxpool.Pool, envID string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM env.environment_reaper WHERE environment_id = $1)`, envID).Scan(&exists)
	if err != nil {
		t.Fatalf("reaperRowExists: %v", err)
	}
	return exists
}

func TestRegister_InsertsAndIsIdempotent(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	r := &Reaper{db: pool}
	envID := seedEnvironment(t, pool)
	ctx := context.Background()

	if err := r.Register(ctx, envID, "env-"+envID, time.Hour); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if !reaperRowExists(t, pool, envID) {
		t.Fatal("expected a reaper row after Register")
	}

	// Second Register with a different TTL must upsert the deadline, not
	// error on the PK conflict.
	var firstDeadline time.Time
	if err := pool.QueryRow(ctx, `SELECT hard_deadline FROM env.environment_reaper WHERE environment_id = $1`, envID).Scan(&firstDeadline); err != nil {
		t.Fatalf("read first deadline: %v", err)
	}
	if err := r.Register(ctx, envID, "env-"+envID, 3*time.Hour); err != nil {
		t.Fatalf("second Register (upsert): %v", err)
	}
	var secondDeadline time.Time
	if err := pool.QueryRow(ctx, `SELECT hard_deadline FROM env.environment_reaper WHERE environment_id = $1`, envID).Scan(&secondDeadline); err != nil {
		t.Fatalf("read second deadline: %v", err)
	}
	if !secondDeadline.After(firstDeadline) {
		t.Errorf("expected upserted deadline %v to be later than original %v", secondDeadline, firstDeadline)
	}
}

func TestUnregister_RemovesRow(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	r := &Reaper{db: pool}
	envID := seedEnvironment(t, pool)
	ctx := context.Background()

	if err := r.Register(ctx, envID, "env-"+envID, time.Hour); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Unregister(ctx, envID); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if reaperRowExists(t, pool, envID) {
		t.Fatal("expected reaper row gone after Unregister")
	}
	// Unregister of an unknown id must be a no-op, not an error.
	if err := r.Unregister(ctx, uuid.NewString()); err != nil {
		t.Errorf("Unregister of unknown id should be a no-op: %v", err)
	}
}

func TestSweep_DestroysOnlyPastDeadlineAndClearsRow(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	dest := newRecordingDestroyer()
	r := &Reaper{db: pool, destroyFn: dest.fn}
	ctx := context.Background()

	overdue := seedEnvironment(t, pool)
	future := seedEnvironment(t, pool)

	// overdue: deadline in the past
	if _, err := pool.Exec(ctx, `
		INSERT INTO env.environment_reaper (environment_id, namespace, hard_deadline)
		VALUES ($1, $2, now() - interval '5 minutes')
	`, overdue, "env-"+overdue); err != nil {
		t.Fatalf("insert overdue: %v", err)
	}
	// future: deadline well ahead
	if err := r.Register(ctx, future, "env-"+future, time.Hour); err != nil {
		t.Fatalf("Register future: %v", err)
	}

	r.sweep(ctx)

	if got := dest.callsFor(overdue); got != 1 {
		t.Errorf("overdue env: expected exactly 1 destroy call, got %d", got)
	}
	if got := dest.callsFor(future); got != 0 {
		t.Errorf("future env: expected 0 destroy calls, got %d", got)
	}
	if reaperRowExists(t, pool, overdue) {
		t.Error("expected overdue env's reaper row to be cleared after a successful destroy")
	}
	if !reaperRowExists(t, pool, future) {
		t.Error("expected future env's reaper row to remain")
	}
	// Reason must be the shared constant, not an ad-hoc string.
	if dest.calls[0].reason != destroyreason.Reaper {
		t.Errorf("expected destroy reason %q, got %q", destroyreason.Reaper, dest.calls[0].reason)
	}
}

func TestSweep_FailedDestroyLeavesRowRegisteredForRetry(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	dest := newRecordingDestroyer()
	r := &Reaper{db: pool, destroyFn: dest.fn}
	ctx := context.Background()

	envID := seedEnvironment(t, pool)
	dest.failFor[envID] = errors.New("k8s API down")

	if _, err := pool.Exec(ctx, `
		INSERT INTO env.environment_reaper (environment_id, namespace, hard_deadline)
		VALUES ($1, $2, now() - interval '1 minute')
	`, envID, "env-"+envID); err != nil {
		t.Fatalf("insert overdue: %v", err)
	}

	r.sweep(ctx)
	if !reaperRowExists(t, pool, envID) {
		t.Fatal("a failed destroy must NOT clear the reaper row -- the row is how the next sweep retries")
	}

	// Next sweep: destroy now succeeds, row is cleared.
	delete(dest.failFor, envID)
	r.sweep(ctx)
	if dest.callsFor(envID) != 2 {
		t.Errorf("expected a second destroy attempt on the retry sweep, got %d total", dest.callsFor(envID))
	}
	if reaperRowExists(t, pool, envID) {
		t.Error("expected the row cleared once the retry destroy succeeded")
	}
}

func TestSweep_NoOverdueRowsIsANoOp(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	dest := newRecordingDestroyer()
	r := &Reaper{db: pool, destroyFn: dest.fn}
	ctx := context.Background()

	envID := seedEnvironment(t, pool)
	if err := r.Register(ctx, envID, "env-"+envID, time.Hour); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r.sweep(ctx)
	if dest.totalCalls() != 0 {
		t.Errorf("expected no destroy calls when nothing is overdue, got %d", dest.totalCalls())
	}
}

func TestOrphanSweep_DestroysNamespacesWithNoReaperRecord(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	dest := newRecordingDestroyer()
	r := &Reaper{db: pool, destroyFn: dest.fn}
	ctx := context.Background()

	known := seedEnvironment(t, pool)
	if err := r.Register(ctx, known, "env-"+known, time.Hour); err != nil {
		t.Fatalf("Register known: %v", err)
	}

	orphanNS := "env-" + uuid.NewString()
	lister := func(context.Context) ([]string, error) {
		return []string{"env-" + known, orphanNS}, nil
	}

	r.OrphanSweep(ctx, lister)

	// The known namespace has a reaper record -> must be left alone.
	if got := dest.callsFor("env-" + known); got != 0 {
		t.Errorf("known namespace: expected 0 destroy calls, got %d", got)
	}
	// The orphan namespace has no record -> force-destroyed, with the
	// env id parsed back out of the "env-" prefix.
	wantEnvID := orphanNS[len("env-"):]
	if got := dest.callsFor(wantEnvID); got != 1 {
		t.Errorf("orphan namespace: expected exactly 1 destroy call for env id %q, got %d", wantEnvID, got)
	}
}

func TestOrphanSweep_ListerErrorIsHandledGracefully(t *testing.T) {
	pool := testsupport.NewPostgres(t)
	dest := newRecordingDestroyer()
	r := &Reaper{db: pool, destroyFn: dest.fn}

	lister := func(context.Context) ([]string, error) {
		return nil, errors.New("k8s list failed")
	}
	// Must not panic and must not destroy anything on a list failure.
	r.OrphanSweep(context.Background(), lister)
	if dest.totalCalls() != 0 {
		t.Errorf("expected no destroy calls when the namespace lister errors, got %d", dest.totalCalls())
	}
}

func TestEnvIDFromNamespace(t *testing.T) {
	cases := map[string]string{
		"env-abc-123":    "abc-123",
		"env-":           "env-", // too short to strip -> returned as-is
		"weird":          "weird",
		"env-env-nested": "env-nested",
	}
	for in, want := range cases {
		if got := envIDFromNamespace(in); got != want {
			t.Errorf("envIDFromNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}
