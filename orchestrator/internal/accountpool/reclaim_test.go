package accountpool

import (
	"context"
	"testing"
)

// ReclaimForAttempt is the Restore-path counterpart of Claim: a resumed
// T3 project attempt wants its ORIGINAL sandbox account back if it's
// still pooled (baseline already applied), else any AVAILABLE one.

func TestReclaimForAttempt_PreferredStillAvailable_ReusedWithoutBaseline(t *testing.T) {
	m, fake, _ := newTestManager(t)
	seedAvailable(t, m, "111111111111", "us-east-1")
	ctx := context.Background()

	acct, role, err := m.ReclaimForAttempt(ctx,
		"aaaaaaaa-0000-0000-0000-000000000001", "ten-1", "us-east-1", "111111111111", 20)
	if err != nil {
		t.Fatalf("ReclaimForAttempt: %v", err)
	}
	if acct != "111111111111" {
		t.Fatalf("reclaimed account = %s, want the preferred 111111111111", acct)
	}
	if role != "LearnerSandboxRole" {
		t.Errorf("role = %s, want LearnerSandboxRole", role)
	}
	if got := stateOf(t, m, "111111111111"); got != "IN_USE" {
		t.Errorf("state = %s, want IN_USE", got)
	}
	// Reuse path re-arms the budget alarm but SKIPS ApplyBaseline (it's
	// already applied on this account -- that's the whole point of
	// preferring it).
	if fake.CallCount("PutAccountBudget") != 1 {
		t.Errorf("expected 1 PutAccountBudget on reclaim, got calls: %v", fake.Calls)
	}
	if fake.CallCount("ApplyBaseline") != 0 {
		t.Errorf("reclaim of a still-pooled account must NOT re-run ApplyBaseline, got calls: %v", fake.Calls)
	}
}

func TestReclaimForAttempt_PreferredGone_FallsBackToFreshClaim(t *testing.T) {
	m, fake, _ := newTestManager(t)
	// Only a DIFFERENT account is available; the preferred one isn't in
	// the pool at all.
	seedAvailable(t, m, "222222222222", "us-east-1")
	ctx := context.Background()

	acct, role, err := m.ReclaimForAttempt(ctx,
		"aaaaaaaa-0000-0000-0000-000000000002", "ten-1", "us-east-1", "999999999999", 15)
	if err != nil {
		t.Fatalf("ReclaimForAttempt fallback: %v", err)
	}
	if acct != "222222222222" {
		t.Fatalf("fallback account = %s, want 222222222222", acct)
	}
	if role != "LearnerSandboxRole" {
		t.Errorf("role = %s", role)
	}
	// Fallback goes through the full Claim path -> baseline IS applied on
	// the fresh account.
	if fake.CallCount("ApplyBaseline") != 1 {
		t.Errorf("fallback fresh claim should ApplyBaseline once, got: %v", fake.Calls)
	}
}

func TestReclaimForAttempt_RetriedRestore_ReturnsSameAccount(t *testing.T) {
	m, _, _ := newTestManager(t)
	seedAvailable(t, m, "111111111111", "us-east-1")
	ctx := context.Background()
	attempt := "aaaaaaaa-0000-0000-0000-000000000003"

	first, _, err := m.ReclaimForAttempt(ctx, attempt, "ten-1", "us-east-1", "111111111111", 20)
	if err != nil {
		t.Fatalf("first reclaim: %v", err)
	}
	// Second call (retried Restore): the account is now IN_USE by this
	// same attempt -- must be handed back, not rejected or double-claimed.
	second, _, err := m.ReclaimForAttempt(ctx, attempt, "ten-1", "us-east-1", "111111111111", 20)
	if err != nil {
		t.Fatalf("retried reclaim: %v", err)
	}
	if first != second {
		t.Errorf("retried Restore returned a different account: %s vs %s", first, second)
	}
}

func TestReclaimForAttempt_NoPreference_EmptyPool_Errors(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()
	if _, _, err := m.ReclaimForAttempt(ctx, "x", "ten", "eu-west-1", "", 5); err == nil {
		t.Fatal("expected an error reclaiming with no preference and an empty pool")
	}
}
