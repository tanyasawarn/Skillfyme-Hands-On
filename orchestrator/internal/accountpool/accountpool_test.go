package accountpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

// recorderPublisher captures the ACCOUNT_* events the manager emits.
type recorderPublisher struct {
	mu        sync.Mutex
	claimed   []string
	nuked     []string
	quarantis []string
}

func (r *recorderPublisher) PublishAccountClaimed(_ context.Context, attemptID, accountID, region string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimed = append(r.claimed, accountID+"/"+attemptID+"/"+region)
}
func (r *recorderPublisher) PublishAccountNuked(_ context.Context, attemptID, accountID string, verified bool, rr int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := "false"
	if verified {
		v = "true"
	}
	r.nuked = append(r.nuked, accountID+"/verified="+v)
}
func (r *recorderPublisher) PublishAccountQuarantined(_ context.Context, attemptID, accountID, reason string, rr int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.quarantis = append(r.quarantis, accountID+"/"+reason)
}

func newTestManager(t *testing.T) (*Manager, *cloudaws.FakeClient, *recorderPublisher) {
	t.Helper()
	db := testsupport.NewPostgres(t) // skips if Docker unavailable
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	fake := cloudaws.NewFakeClient()
	pub := &recorderPublisher{}
	return NewManager(db, rdb, fake, pub), fake, pub
}

// seedAvailable inserts an AVAILABLE account row + syncs Redis.
func seedAvailable(t *testing.T, m *Manager, accountID, region string) {
	t.Helper()
	ctx := context.Background()
	_, err := m.db.Exec(ctx,
		`INSERT INTO env.cloud_account (aws_account_id, state, region) VALUES ($1, 'AVAILABLE', $2)
		 ON CONFLICT (aws_account_id) DO UPDATE SET state='AVAILABLE', region=$2,
		   attempt_id=NULL, budget_usd=NULL, quarantine_reason=NULL`,
		accountID, region)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.SyncRedisFromDB(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func stateOf(t *testing.T, m *Manager, accountID string) string {
	t.Helper()
	var s string
	if err := m.db.QueryRow(context.Background(),
		`SELECT state FROM env.cloud_account WHERE aws_account_id=$1`, accountID).Scan(&s); err != nil {
		t.Fatalf("stateOf: %v", err)
	}
	return s
}

func TestClaim_HappyPath_MovesToInUseAndEmitsClaimed(t *testing.T) {
	m, fake, pub := newTestManager(t)
	seedAvailable(t, m, "111111111111", "us-east-1")
	ctx := context.Background()

	res, err := m.Claim(ctx, ClaimInput{
		AttemptID: "aaaaaaaa-0000-0000-0000-000000000001",
		TenantID:  "ten-1",
		Region:    "us-east-1",
		BudgetUSD: 25,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.AccountID != "111111111111" || res.RoleName != "LearnerSandboxRole" {
		t.Fatalf("unexpected ClaimResult: %+v", res)
	}
	if got := stateOf(t, m, "111111111111"); got != "IN_USE" {
		t.Errorf("state = %s, want IN_USE", got)
	}
	// claim path calls budget → baseline → sku tag, in order
	if fake.CallCount("PutAccountBudget") != 1 || fake.CallCount("ApplyBaseline") != 1 || fake.CallCount("SetSkuExceptionTag") != 1 {
		t.Errorf("claim AWS calls: %v", fake.Calls)
	}
	if len(pub.claimed) != 1 {
		t.Errorf("expected 1 ACCOUNT_CLAIMED, got %v", pub.claimed)
	}
}

func TestClaim_EmptyPool_ReturnsErrNoAccountAvailable(t *testing.T) {
	m, _, _ := newTestManager(t)
	_, err := m.Claim(context.Background(), ClaimInput{Region: "eu-west-1", AttemptID: "x", BudgetUSD: 1})
	if !errors.Is(err, ErrNoAccountAvailable) {
		t.Fatalf("want ErrNoAccountAvailable, got %v", err)
	}
}

func TestClaim_Concurrent_NeverDoubleVends(t *testing.T) {
	m, _, _ := newTestManager(t)
	seedAvailable(t, m, "222222222222", "us-east-1")

	const n = 8
	var wg sync.WaitGroup
	got := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Claim(context.Background(), ClaimInput{
				Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-00000000000a", BudgetUSD: 1,
			})
			got[i] = err == nil
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, ok := range got {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly 1 successful claim, got %d", wins)
	}
}

func TestRelease_CleanVerify_ReturnsToPool(t *testing.T) {
	m, fake, pub := newTestManager(t)
	seedAvailable(t, m, "333333333333", "us-east-1")
	ctx := context.Background()
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-000000000001", BudgetUSD: 10}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{Verified: true, ResourcesRemaining: 0}, nil
	}
	if err := m.Release(ctx, "333333333333"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := stateOf(t, m, "333333333333"); got != "AVAILABLE" {
		t.Errorf("state = %s, want AVAILABLE", got)
	}
	// re-claimable
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-000000000002", BudgetUSD: 10}); err != nil {
		t.Errorf("re-claim after release failed: %v", err)
	}
	if len(pub.nuked) == 0 || pub.nuked[len(pub.nuked)-1] != "333333333333/verified=true" {
		t.Errorf("expected ACCOUNT_NUKED verified=true, got %v", pub.nuked)
	}
}

func TestRelease_VerificationNonEmpty_Quarantines(t *testing.T) {
	m, fake, pub := newTestManager(t)
	seedAvailable(t, m, "444444444444", "us-east-1")
	ctx := context.Background()
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-000000000001", BudgetUSD: 10}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{
			Verified: false, ResourcesRemaining: 3,
			BlindSpotHits: []string{"route53:hostedzone/Z123"},
			Detail:        "3 resources still present",
		}, nil
	}
	if err := m.Release(ctx, "444444444444"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := stateOf(t, m, "444444444444"); got != "QUARANTINED" {
		t.Errorf("state = %s, want QUARANTINED", got)
	}
	// NEVER returned to the pool
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-0000000000ff", BudgetUSD: 1}); !errors.Is(err, ErrNoAccountAvailable) {
		t.Errorf("a quarantined account must not be claimable; got err=%v", err)
	}
	if len(pub.quarantis) != 1 || pub.quarantis[0] != "444444444444/verification_nonempty" {
		t.Errorf("expected ACCOUNT_QUARANTINED verification_nonempty, got %v", pub.quarantis)
	}
}

func TestRelease_NukeError_Quarantines(t *testing.T) {
	m, fake, pub := newTestManager(t)
	seedAvailable(t, m, "555555555555", "us-east-1")
	ctx := context.Background()
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-000000000001", BudgetUSD: 10}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{}, errors.New("aws-nuke exited 1")
	}
	if err := m.Release(ctx, "555555555555"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := stateOf(t, m, "555555555555"); got != "QUARANTINED" {
		t.Errorf("state = %s, want QUARANTINED", got)
	}
	if len(pub.quarantis) != 1 || pub.quarantis[0] != "555555555555/nuke_error" {
		t.Errorf("expected ACCOUNT_QUARANTINED nuke_error, got %v", pub.quarantis)
	}
}

func TestRelease_Idempotent(t *testing.T) {
	m, _, _ := newTestManager(t)
	seedAvailable(t, m, "666666666666", "us-east-1")
	ctx := context.Background()
	if _, err := m.Claim(ctx, ClaimInput{Region: "us-east-1", AttemptID: "aaaaaaaa-0000-0000-0000-000000000001", BudgetUSD: 10}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release(ctx, "666666666666"); err != nil {
		t.Fatalf("Release 1: %v", err)
	}
	// second Release on an already-AVAILABLE account is a no-op
	if err := m.Release(ctx, "666666666666"); err != nil {
		t.Fatalf("Release 2 (idempotent) errored: %v", err)
	}
	if got := stateOf(t, m, "666666666666"); got != "AVAILABLE" {
		t.Errorf("state = %s, want AVAILABLE", got)
	}
}

func TestFiller_RedrivesStuckNuking(t *testing.T) {
	m, fake, _ := newTestManager(t)
	ctx := context.Background()
	// an account wedged in NUKING with an old updated_at
	_, err := m.db.Exec(ctx,
		`INSERT INTO env.cloud_account (aws_account_id, state, region, updated_at)
		 VALUES ('777777777777', 'NUKING', 'us-east-1', now() - interval '20 minutes')`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{Verified: true}, nil
	}
	f := NewFiller(m, []FillTarget{{Region: "us-east-1", Count: 2}})
	f.tick()

	if got := stateOf(t, m, "777777777777"); got != "AVAILABLE" {
		t.Errorf("filler should have re-driven the stuck NUKING account to AVAILABLE, got %s", got)
	}
	_ = time.Now
}
