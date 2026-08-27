package warmpool

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestManager returns a Manager backed by an in-process miniredis, so
// Claim/Add/Size are exercised against a real Redis command surface
// (SPOP/SADD/SCARD semantics, including the atomicity Claim relies on)
// without a network dependency.
func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &Manager{rdb: rdb}, mr
}

func TestClaim_HitRemovesFromPool(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	if err := m.Add(ctx, "bp.linux.v1", "env-1"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := m.Claim(ctx, "bp.linux.v1")
	if !ok || got != "env-1" {
		t.Fatalf("expected to claim env-1, got %q ok=%v", got, ok)
	}
	// The claimed env must no longer be in the pool.
	if size, _ := m.Size(ctx, "bp.linux.v1"); size != 0 {
		t.Errorf("expected pool empty after claim, size=%d", size)
	}
	// A second claim on the now-empty pool misses.
	if _, ok := m.Claim(ctx, "bp.linux.v1"); ok {
		t.Error("expected a miss claiming from an empty pool")
	}
}

func TestClaim_MissOnUnknownBlueprint(t *testing.T) {
	m, _ := newTestManager(t)
	if _, ok := m.Claim(context.Background(), "bp.never.seen"); ok {
		t.Error("expected a miss for a blueprint with no pool")
	}
}

func TestClaim_ConcurrentClaimsNeverReturnSameEnv(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	const n = 50
	for i := 0; i < n; i++ {
		if err := m.Add(ctx, "bp.linux.v1", envID(i)); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < n*2; i++ { // more claimers than envs
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, ok := m.Claim(ctx, "bp.linux.v1"); ok {
				mu.Lock()
				seen[id]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != n {
		t.Errorf("expected %d distinct envs claimed, got %d", n, len(seen))
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("env %s was handed out %d times -- SPOP atomicity violated", id, c)
		}
	}
}

func TestSize_ReportsDepth(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = m.Add(ctx, "bp.linux.v1", envID(i))
	}
	if size, err := m.Size(ctx, "bp.linux.v1"); err != nil || size != 3 {
		t.Fatalf("expected size 3, got %d err=%v", size, err)
	}
}

func TestFillOnce_FillsExactlyTheDeficit(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	// Pool already has 1; target is 4 -> deficit 3.
	_ = m.Add(ctx, "bp.linux.v1", "pre-existing")

	var filled int
	f := &Filler{
		pool:    m,
		targets: []Target{{BlueprintID: "bp.linux.v1", Image: "img", Count: 4}},
	}
	f.fillOneFn = func(_ context.Context, target Target) error {
		filled++
		// Simulate the real fillOne's end effect: env lands in the pool.
		return m.Add(context.Background(), target.BlueprintID, envID(filled))
	}

	f.fillOnce(ctx)

	if filled != 3 {
		t.Errorf("expected 3 fill calls to close a deficit of 3, got %d", filled)
	}
	if size, _ := m.Size(ctx, "bp.linux.v1"); size != 4 {
		t.Errorf("expected pool at target depth 4, got %d", size)
	}
}

func TestFillOnce_NoFillWhenAtOrAboveTarget(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = m.Add(ctx, "bp.linux.v1", envID(i))
	}

	var filled int
	f := &Filler{
		pool:    m,
		targets: []Target{{BlueprintID: "bp.linux.v1", Count: 3}}, // already over target
	}
	f.fillOneFn = func(context.Context, Target) error { filled++; return nil }

	f.fillOnce(ctx)
	if filled != 0 {
		t.Errorf("expected no fills when pool is already at/above target, got %d", filled)
	}
}

func TestFillOnce_StopsHammeringAFailingBlueprintThisTick(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	var attempts int
	f := &Filler{
		pool: m,
		targets: []Target{
			{BlueprintID: "bp.broken.v1", Count: 10}, // big deficit
		},
	}
	f.fillOneFn = func(context.Context, Target) error {
		attempts++
		return errors.New("provision failed")
	}

	f.fillOnce(ctx)

	// Deficit is 10, but the first failure must break the inner loop for
	// this blueprint this tick.
	if attempts != 1 {
		t.Errorf("expected exactly 1 attempt before backing off a failing blueprint, got %d", attempts)
	}
}

func TestFillOnce_OneFailingBlueprintDoesNotBlockOthers(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	var goodFills int
	f := &Filler{
		pool: m,
		targets: []Target{
			{BlueprintID: "bp.broken.v1", Count: 5},
			{BlueprintID: "bp.good.v1", Count: 2},
		},
	}
	f.fillOneFn = func(_ context.Context, target Target) error {
		if target.BlueprintID == "bp.broken.v1" {
			return errors.New("boom")
		}
		goodFills++
		return m.Add(context.Background(), target.BlueprintID, envID(goodFills))
	}

	f.fillOnce(ctx)

	if goodFills != 2 {
		t.Errorf("a failing blueprint must not prevent a healthy one from filling; good fills=%d", goodFills)
	}
}

func TestRun_NoTargetsReturnsImmediately(t *testing.T) {
	f := &Filler{targets: nil}
	done := make(chan struct{})
	go func() { f.Run(context.Background()); close(done) }()
	select {
	case <-done:
	default:
		// Give the goroutine a beat; Run must not block on a ticker when
		// there are no targets.
	}
	<-done // must return without ctx cancellation
}

func TestConstants_WarmPoolTTLShorterThanEnvironmentDefault(t *testing.T) {
	// The whole point of a separate warm-pool TTL: an unclaimed warm env
	// is pure standing cost and must turn over faster than a real
	// attempt's environment.
	if warmPoolTTL >= 90*60*1e9 { // 90 minutes in ns, ttl.EnvironmentDefault
		t.Errorf("warmPoolTTL (%s) must be well under the 90m environment default", warmPoolTTL)
	}
}

func envID(i int) string { return "env-" + string(rune('a'+i%26)) + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
