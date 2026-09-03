package cloudbudget

import (
	"context"
	"sync"
	"testing"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

type spyHooks struct {
	mu         sync.Mutex
	revoked    []string
	terminated []string
	warnings   []int
}

func (s *spyHooks) revoke(attemptID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = append(s.revoked, attemptID)
}
func (s *spyHooks) terminate(_ context.Context, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminated = append(s.terminated, attemptID)
	return nil
}
func (s *spyHooks) emit(_ context.Context, _, _ string, percent int, _, _ float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnings = append(s.warnings, percent)
}

func newTestEnforcer(t *testing.T) (*Enforcer, *spyHooks) {
	t.Helper()
	db := testsupport.NewPostgres(t)
	s := &spyHooks{}
	return NewEnforcer(db, s.revoke, s.terminate, s.emit), s
}

func seedInUse(t *testing.T, e *Enforcer, accountID, attemptID string) {
	t.Helper()
	_, err := e.db.Exec(context.Background(),
		`INSERT INTO env.cloud_account (aws_account_id, state, region, attempt_id, budget_usd)
		 VALUES ($1, 'IN_USE', 'us-east-1', $2, 10)
		 ON CONFLICT (aws_account_id) DO UPDATE SET state='IN_USE', attempt_id=$2, budget_usd=10`,
		accountID, attemptID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestHandleBreach_WarnThresholdEmitsOnly(t *testing.T) {
	e, s := newTestEnforcer(t)
	seedInUse(t, e, "111111111111", "aaaaaaaa-0000-0000-0000-000000000001")

	for _, pct := range []int{50, 80} {
		if err := e.HandleBreach(context.Background(), Breach{
			AccountID: "111111111111", Percent: pct, AmountUSD: float64(pct) / 10, BudgetUSD: 10,
		}); err != nil {
			t.Fatalf("HandleBreach %d: %v", pct, err)
		}
	}
	if len(s.warnings) != 2 || len(s.revoked) != 0 || len(s.terminated) != 0 {
		t.Fatalf("warn thresholds should not revoke/terminate: warnings=%v revoked=%v terminated=%v",
			s.warnings, s.revoked, s.terminated)
	}
}

func TestHandleBreach_HardStopRevokesAndTerminates(t *testing.T) {
	e, s := newTestEnforcer(t)
	seedInUse(t, e, "222222222222", "aaaaaaaa-0000-0000-0000-000000000002")

	if err := e.HandleBreach(context.Background(), Breach{
		AccountID: "222222222222", Percent: 100, AmountUSD: 10.5, BudgetUSD: 10,
	}); err != nil {
		t.Fatalf("HandleBreach: %v", err)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "aaaaaaaa-0000-0000-0000-000000000002" {
		t.Errorf("expected creds revoked for the attempt, got %v", s.revoked)
	}
	if len(s.terminated) != 1 {
		t.Errorf("expected env force-terminated, got %v", s.terminated)
	}
}

func TestHandleBreach_HardStopIsIdempotent(t *testing.T) {
	e, s := newTestEnforcer(t)
	seedInUse(t, e, "333333333333", "aaaaaaaa-0000-0000-0000-000000000003")

	for _, pct := range []int{100, 100, 120} { // duplicate delivery + a later alarm
		if err := e.HandleBreach(context.Background(), Breach{
			AccountID: "333333333333", Percent: pct, AmountUSD: 12, BudgetUSD: 10,
		}); err != nil {
			t.Fatalf("HandleBreach %d: %v", pct, err)
		}
	}
	if len(s.revoked) != 1 || len(s.terminated) != 1 {
		t.Fatalf("hard stop should act exactly once: revoked=%v terminated=%v", s.revoked, s.terminated)
	}
	// after release, the guard resets
	e.ResetActed("333333333333")
	if err := e.HandleBreach(context.Background(), Breach{
		AccountID: "333333333333", Percent: 100, AmountUSD: 12, BudgetUSD: 10,
	}); err != nil {
		t.Fatalf("HandleBreach after reset: %v", err)
	}
	if len(s.revoked) != 2 {
		t.Errorf("after ResetActed a fresh breach should act again, revoked=%v", s.revoked)
	}
}

func TestLaunchCap_AllowsUnderAndRejectsAtCap(t *testing.T) {
	e, _ := newTestEnforcer(t)
	cap := NewLaunchCap(e.db, 2)
	ctx := context.Background()

	// 0 IN_USE → allow
	if ok, err := cap.Allow(ctx); err != nil || !ok {
		t.Fatalf("expected allow at 0 in-use, ok=%v err=%v", ok, err)
	}
	seedInUse(t, e, "444444444444", "aaaaaaaa-0000-0000-0000-000000000004")
	seedInUse(t, e, "555555555555", "aaaaaaaa-0000-0000-0000-000000000005")
	// 2 IN_USE, cap 2 → reject
	if ok, err := cap.Allow(ctx); err != nil || ok {
		t.Fatalf("expected reject at cap, ok=%v err=%v", ok, err)
	}
}

func TestLaunchCap_ZeroMaxIsUncapped(t *testing.T) {
	e, _ := newTestEnforcer(t)
	cap := NewLaunchCap(e.db, 0)
	seedInUse(t, e, "666666666666", "aaaaaaaa-0000-0000-0000-000000000006")
	if ok, err := cap.Allow(context.Background()); err != nil || !ok {
		t.Fatalf("max=0 should be uncapped, ok=%v err=%v", ok, err)
	}
}
