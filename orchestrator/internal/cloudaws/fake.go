package cloudaws

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeClient is an in-memory Client for tests and for running the
// orchestrator without a Platform AWS account. Every call is recorded
// (see Calls); results are scripted via the exported knobs.
//
// Concurrency-safe — the accountpool warm-fill loop and claim path can
// hit it from multiple goroutines in a test.
type FakeClient struct {
	mu sync.Mutex

	// Scripted outcomes.
	AssumeRoleFn func(accountID, roleName, sub string) (AssumeRoleResult, error)
	NukeFn       func(accountID string) (NukeResult, error)
	BaselineFn   func(accountID, attemptID string) (string, error)
	CostFn       func(accountID string, since time.Time) ([]AccountCost, error)
	CURFn        func(day time.Time) ([]AccountCost, error)

	// Call log, most-recent-last.
	Calls []string
}

func NewFakeClient() *FakeClient { return &FakeClient{} }

func (f *FakeClient) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
}

// CallCount returns how many recorded calls contain substr.
func (f *FakeClient) CallCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.Calls {
		if len(substr) == 0 || contains(c, substr) {
			n++
		}
	}
	return n
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (f *FakeClient) AssumeRoleWithWebIdentity(_ context.Context, accountID, roleName, webIdentityToken string, ttl time.Duration) (AssumeRoleResult, error) {
	f.record("AssumeRoleWithWebIdentity account=%s role=%s ttl=%s", accountID, roleName, ttl)
	if f.AssumeRoleFn != nil {
		return f.AssumeRoleFn(accountID, roleName, webIdentityToken)
	}
	return AssumeRoleResult{
		AccessKeyID:     "ASIAFAKE" + accountID,
		SecretAccessKey: "fake-secret",
		SessionToken:    "fake-session-" + webIdentityToken,
		Expiration:      time.Now().Add(ttl),
	}, nil
}

func (f *FakeClient) RunNuke(_ context.Context, accountID string) (NukeResult, error) {
	f.record("RunNuke account=%s", accountID)
	if f.NukeFn != nil {
		return f.NukeFn(accountID)
	}
	return NukeResult{Verified: true, ResourcesRemaining: 0}, nil
}

func (f *FakeClient) PutAccountBudget(_ context.Context, accountID string, limitUSD float64, thresholds []BudgetThreshold) error {
	f.record("PutAccountBudget account=%s limit=%.2f thresholds=%d", accountID, limitUSD, len(thresholds))
	return nil
}

func (f *FakeClient) DeleteAccountBudget(_ context.Context, accountID string) error {
	f.record("DeleteAccountBudget account=%s", accountID)
	return nil
}

func (f *FakeClient) ApplyBaseline(_ context.Context, accountID, attemptID, tenantID string) (string, error) {
	f.record("ApplyBaseline account=%s attempt=%s tenant=%s", accountID, attemptID, tenantID)
	if f.BaselineFn != nil {
		return f.BaselineFn(accountID, attemptID)
	}
	return "LearnerSandboxRole", nil
}

func (f *FakeClient) SetSkuExceptionTag(_ context.Context, accountID string, skuExceptions []string) error {
	f.record("SetSkuExceptionTag account=%s exceptions=%d", accountID, len(skuExceptions))
	return nil
}

func (f *FakeClient) ClearAccountTags(_ context.Context, accountID string) error {
	f.record("ClearAccountTags account=%s", accountID)
	return nil
}

func (f *FakeClient) GetCostSince(_ context.Context, accountID string, since time.Time) ([]AccountCost, error) {
	f.record("GetCostSince account=%s since=%s", accountID, since.Format(time.RFC3339))
	if f.CostFn != nil {
		return f.CostFn(accountID, since)
	}
	return nil, nil
}

func (f *FakeClient) ReconcileFromCUR(_ context.Context, day time.Time) ([]AccountCost, error) {
	f.record("ReconcileFromCUR day=%s", day.Format("2006-01-02"))
	if f.CURFn != nil {
		return f.CURFn(day)
	}
	return nil, nil
}
