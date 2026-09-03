// Package cloudaws is the narrow AWS-operations boundary the Phase 3
// account-lifecycle packages (credbroker, cloudnuke, cloudbudget,
// accountpool, cloudcost) sit on top of.
//
// Same pattern as internal/k8s's Provisioner vs the fake used in tests:
// a real implementation (RealClient, wired in cmd/orchestrator once a
// Platform AWS account exists — Stage 1.1/1.3) talks to STS / Budgets /
// EventBridge / Cost Explorer / Organizations / Resource Explorer /
// Config and runs the containerised aws-nuke; FakeClient records calls
// and returns scripted results so every package here has real unit tests
// with no AWS dependency and no credentials.
//
// The interface is deliberately coarse — one method per lifecycle
// operation, not a thin wrapper per AWS API call — because the packages
// consuming it care about "claim baseline applied", "account nuked +
// verified", not about individual SDK calls.
package cloudaws

import (
	"context"
	"time"
)

// AssumeRoleResult is the short-lived credential set the STS broker
// writes into the workspace pod's shared emptyDir (§5.3, D9).
type AssumeRoleResult struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}

// NukeResult is the outcome of a nuke + mandatory verification pass
// (§5.3). Verified=false with a non-empty ResourcesRemaining means the
// account must be QUARANTINED, never returned to the pool.
type NukeResult struct {
	Verified           bool
	ResourcesRemaining int
	// BlindSpotHits lists resources found by the hardcoded
	// nuke-blind-spot service list that aws-nuke itself does not cover
	// (e.g. a Route53 hosted zone) — empty on a clean verify.
	BlindSpotHits []string
	Detail        string
}

// AccountCost is one account's cloud spend for a window, from Cost
// Explorer or a CUR read (§5.3, §10.4).
type AccountCost struct {
	AccountID   string
	AttemptID   string
	WindowStart time.Time
	WindowEnd   time.Time
	AmountUSD   float64
	Source      string // "ce" | "cur"
}

// BudgetThreshold is one alarm level for an account budget (§10.4:
// 50/80/100%).
type BudgetThreshold struct {
	Percent int
}

// Client is everything the Phase 3 lifecycle packages need from AWS.
type Client interface {
	// --- credbroker (Stage 2.1) ---

	// AssumeRoleWithWebIdentity exchanges a platform-IdP JWT for
	// short-lived creds scoped to LearnerSandboxRole in the given
	// account. sub of the JWT is the attempt id.
	AssumeRoleWithWebIdentity(ctx context.Context, accountID, roleName, webIdentityToken string, ttl time.Duration) (AssumeRoleResult, error)

	// --- cloudnuke (Stage 2.2) ---

	// RunNuke assumes PlatformNukeRole in the account and runs the
	// containerised aws-nuke, then the mandatory verification pass
	// (Config + Resource Explorer + the blind-spot list).
	RunNuke(ctx context.Context, accountID string) (NukeResult, error)

	// --- cloudbudget (Stage 2.3) ---

	// PutAccountBudget creates/updates an AWS Budget on the account with
	// alarms at the given thresholds of limitUSD, wired to the
	// EventBridge → SNS → orchestrator path.
	PutAccountBudget(ctx context.Context, accountID string, limitUSD float64, thresholds []BudgetThreshold) error
	// DeleteAccountBudget removes the budget on release.
	DeleteAccountBudget(ctx context.Context, accountID string) error

	// --- accountpool (Stage 2.4) ---

	// ApplyBaseline runs the infra/account-baseline Terraform module
	// (1.3) into a freshly-claimed account, returning the LearnerSandbox
	// role name it created.
	ApplyBaseline(ctx context.Context, accountID, attemptID, tenantID string) (roleName string, err error)
	// SetSkuExceptionTag writes the account tag the expensive-SKU SCP
	// (1.2) keys its exception on.
	SetSkuExceptionTag(ctx context.Context, accountID string, skuExceptions []string) error
	// ClearAccountTags removes claim-time tags on release.
	ClearAccountTags(ctx context.Context, accountID string) error

	// --- cloudcost (Stage 2.5) ---

	// GetCostSince returns per-account spend (grouped by the attempt_id
	// tag) for the window since `since`.
	GetCostSince(ctx context.Context, accountID string, since time.Time) ([]AccountCost, error)
	// ReconcileFromCUR reads the daily CUR-in-S3 export for the day and
	// returns the authoritative per-account cost for reconciliation.
	ReconcileFromCUR(ctx context.Context, day time.Time) ([]AccountCost, error)
}
