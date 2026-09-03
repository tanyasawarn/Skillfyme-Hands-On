// Package cloudbudget implements Stage 2.3's budget enforcement chain and
// the concurrent-T3 launch cap (memory.md §5.3, §10.4, 2188/2192;
// PLAN_PHASE3_PROJECTS.md A7).
//
// The AWS half — per-account AWS Budgets at 50/80/100% of
// activity_spec.environment.cost_budget_usd, wired EventBridge -> SNS ->
// this orchestrator — is set up by accountpool.Manager at claim time
// (cloudaws.PutAccountBudget). This package is:
//
//   - HandleBreach: the endpoint the SNS notification lands on. At
//     50/80% it emits a warning event; at >=100% it revokes the
//     attempt's brokered credentials (credbroker stop) and force-
//     terminates the T3 environment.
//   - LaunchCap: a config'd max concurrent IN_USE accounts. Provision
//     checks Allow() before claiming; over the cap it returns
//     ResourceExhausted (Dev B surfaces "T3 capacity reached").
//
// No AWS dependency: the revoke + terminate hooks are injected funcs.
package cloudbudget

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

var log = logging.Component("cloudbudget")

// RevokeCredsFunc stops the STS broker for an attempt (credbroker
// Registry.StopFor). TerminateEnvFunc force-destroys the T3 environment
// (the T3 driver Destroy path). Both injected so this package has no
// dependency on credbroker / the orchestrator server.
type RevokeCredsFunc func(attemptID string)
type TerminateEnvFunc func(ctx context.Context, attemptID string) error

// BreachEventFunc emits a warning event at the 50/80 thresholds (Dev B's
// cost dashboard consumes it). Injected — no NATS import here.
type BreachEventFunc func(ctx context.Context, attemptID, accountID string, percent int, amountUSD, budgetUSD float64)

// Enforcer handles budget-breach notifications.
type Enforcer struct {
	db        *pgxpool.Pool
	revoke    RevokeCredsFunc
	terminate TerminateEnvFunc
	emit      BreachEventFunc

	mu    sync.Mutex
	acted map[string]bool // accountID -> a hard action already taken (idempotency)
}

func NewEnforcer(db *pgxpool.Pool, revoke RevokeCredsFunc, terminate TerminateEnvFunc, emit BreachEventFunc) *Enforcer {
	return &Enforcer{
		db:        db,
		revoke:    revoke,
		terminate: terminate,
		emit:      emit,
		acted:     map[string]bool{},
	}
}

// Breach is the payload extracted from the SNS notification.
type Breach struct {
	AccountID string
	Percent   int     // 50 | 80 | 100 (or higher if a later alarm fires first)
	AmountUSD float64 // actual spend
	BudgetUSD float64 // the ceiling
}

// HandleBreach applies the policy for one budget-threshold notification.
// < 100%: warn. >= 100%: revoke creds + force-terminate, once per account.
func (e *Enforcer) HandleBreach(ctx context.Context, b Breach) error {
	// Resolve the attempt currently holding this account.
	var attemptID *string
	err := e.db.QueryRow(ctx,
		`SELECT attempt_id FROM env.cloud_account WHERE aws_account_id = $1`, b.AccountID).Scan(&attemptID)
	if err != nil {
		return fmt.Errorf("cloudbudget: resolve attempt for account %s: %w", b.AccountID, err)
	}
	att := ""
	if attemptID != nil {
		att = *attemptID
	}

	if b.Percent < 100 {
		metrics.CloudBudgetActionTotal.WithLabelValues(strconv.Itoa(b.Percent), "warn").Inc()
		e.emit(ctx, att, b.AccountID, b.Percent, b.AmountUSD, b.BudgetUSD)
		log.Warn("T3 budget threshold crossed",
			"account_id", b.AccountID, "attempt_id", att, "percent", b.Percent,
			"amount_usd", b.AmountUSD, "budget_usd", b.BudgetUSD)
		return nil
	}

	// >= 100%: hard stop. Idempotent — a duplicate SNS delivery or a
	// later alarm (120%) must not double-act.
	e.mu.Lock()
	if e.acted[b.AccountID] {
		e.mu.Unlock()
		return nil
	}
	e.acted[b.AccountID] = true
	e.mu.Unlock()

	metrics.CloudBudgetActionTotal.WithLabelValues(strconv.Itoa(b.Percent), "revoke_and_terminate").Inc()
	log.Error("T3 budget breached — revoking credentials and force-terminating",
		"account_id", b.AccountID, "attempt_id", att, "percent", b.Percent, "amount_usd", b.AmountUSD)

	if att != "" {
		e.revoke(att) // stop the STS broker → outstanding creds expire within the hour
		if err := e.terminate(ctx, att); err != nil {
			log.Error("force-terminate after budget breach failed", "attempt_id", att, "err", err)
			return err
		}
	}
	return nil
}

// ResetActed clears the once-per-account guard (called when an account is
// released back to the pool, so a future claim can breach independently).
func (e *Enforcer) ResetActed(accountID string) {
	e.mu.Lock()
	delete(e.acted, accountID)
	e.mu.Unlock()
}

// --- launch cap ------------------------------------------------------

// LaunchCap bounds concurrent IN_USE T3 accounts. Provision consults it
// before Claim.
type LaunchCap struct {
	db  *pgxpool.Pool
	max int
}

func NewLaunchCap(db *pgxpool.Pool, max int) *LaunchCap {
	return &LaunchCap{db: db, max: max}
}

// Allow reports whether another T3 provision may proceed. false ⇒
// Provision returns gRPC ResourceExhausted.
func (c *LaunchCap) Allow(ctx context.Context) (bool, error) {
	if c.max <= 0 {
		return true, nil // 0 / unset = uncapped
	}
	var inUse int
	if err := c.db.QueryRow(ctx,
		`SELECT count(*) FROM env.cloud_account WHERE state = 'IN_USE'`).Scan(&inUse); err != nil {
		return false, fmt.Errorf("cloudbudget: launch-cap count: %w", err)
	}
	if inUse >= c.max {
		metrics.CloudLaunchCapRejectTotal.Inc()
		log.Warn("T3 launch cap hit", "in_use", inUse, "max", c.max)
		return false, nil
	}
	return true, nil
}
