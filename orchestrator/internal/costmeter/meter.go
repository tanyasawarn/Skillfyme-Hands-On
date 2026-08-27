// Package costmeter implements doc §10.4's budget enforcement chain and
// §8.4's usage_meter emission: "environments report metered usage every
// 60s; budget breaches flow back as commands." M1.9.
package costmeter

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

// log is this package's structured logger (PHASE1_MVP_COMPLETION.md
// §4.2), component=costmeter. Budget lines carry env_id, attempt_id,
// reason plus the numeric cost/pct/budget so an alert can key on them.
var log = logging.Component("costmeter")

// hourlyRateUSD is doc §5.2's T1 cost estimate midpoint ($0.02-0.06/hr).
// A real implementation would read actual node cost-per-vCPU-hour from
// cloud billing data; Phase 1 uses a fixed estimate consistent with the
// doc's own "$0.04/hr" worked example in §5.5.
const hourlyRateUSD = 0.04

// DestroyFunc is called by the budget evaluator when a metered
// environment crosses the 120% hard-stop threshold (doc §5.6 Budget
// clock: "Immediate credential revoke, snapshot, destroy, notify").
// Injected rather than imported directly so costmeter has no dependency
// on the k8s or orchestrator packages (keeps the layering one-directional).
type DestroyFunc func(ctx context.Context, envID string)

// Meter tracks active environments and emits usage_meter rows every 60s.
type Meter struct {
	db          *pgxpool.Pool
	destroyFunc DestroyFunc
	budgetUSD   float64 // per-attempt budget ceiling; doc §10.4 evaluates 50/80/100/120% of this

	mu     sync.Mutex
	active map[string]*tracked // envID -> tracking state
	stopCh chan struct{}
}

type tracked struct {
	attemptID  string
	startedAt  time.Time
	alerted50  bool
	alerted80  bool
	stopped100 bool
}

func NewMeter(db *pgxpool.Pool, destroyFunc DestroyFunc, defaultBudgetUSD float64) *Meter {
	m := &Meter{
		db:          db,
		destroyFunc: destroyFunc,
		budgetUSD:   defaultBudgetUSD,
		active:      make(map[string]*tracked),
		stopCh:      make(chan struct{}),
	}
	go m.loop()
	return m
}

func (m *Meter) StartMetering(envID, attemptID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[envID] = &tracked{attemptID: attemptID, startedAt: time.Now()}
}

func (m *Meter) StopMetering(envID string) {
	m.mu.Lock()
	t, ok := m.active[envID]
	delete(m.active, envID)
	m.mu.Unlock()

	if !ok {
		return
	}
	// Record the attempt's final metered cost exactly once, at teardown
	// -- this histogram IS the doc §13.1 "cost per attempt < $0.08" exit
	// criterion. Also clear the live per-attempt gauge so a torn-down
	// attempt doesn't linger on the dashboard.
	finalCostUSD := time.Since(t.startedAt).Hours() * hourlyRateUSD
	metrics.AttemptCostUSD.Observe(finalCostUSD)
	metrics.UsageMeterCostUSD.DeleteLabelValues(t.attemptID)
}

func (m *Meter) Close() {
	close(m.stopCh)
}

// loop emits a usage_meter row every 60s per doc §8.4/§10.3, and runs
// the budget evaluator chain (doc §10.4: 50% informational, 80% warn,
// 100% block-new-starts [not implemented in Phase 1 -- see note below],
// 120% force-destroy). runImmediately=false: nothing has had time to
// accrue cost in the first instant of a fresh process.
func (m *Meter) loop() {
	loop.RunTicker(m.stopCh, 60*time.Second, m.tick, false)
}

func (m *Meter) tick() {
	m.mu.Lock()
	snapshot := make(map[string]*tracked, len(m.active))
	for k, v := range m.active {
		snapshot[k] = v
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for envID, t := range snapshot {
		elapsedHours := time.Since(t.startedAt).Hours()
		costUSD := elapsedHours * hourlyRateUSD

		if err := m.emitUsageRow(ctx, envID, t.attemptID, costUSD); err != nil {
			log.Error("failed to emit usage_meter row",
				logging.KeyEnvID, envID, logging.KeyAttemptID, t.attemptID, logging.KeyError, err)
		}
		metrics.UsageMeterCostUSD.WithLabelValues(t.attemptID).Set(costUSD)

		m.evaluateBudget(ctx, envID, t, costUSD)
	}
}

// Doc §8.4 usage_meter table: environment_id, attempt_id, window
// bounds, cost fields. Phase 1 emits cloud_cost_usd only (T1 has no
// separate ai_cost_usd yet -- that's the AI Gateway's meter, Phase 4).
func (m *Meter) emitUsageRow(ctx context.Context, envID, attemptID string, costUSD float64) error {
	windowEnd := time.Now()
	windowStart := windowEnd.Add(-60 * time.Second)
	_, err := m.db.Exec(ctx, `
		INSERT INTO env.usage_meter (environment_id, attempt_id, window_start, window_end, cloud_cost_usd, total_cost_usd)
		VALUES ($1, $2, $3, $4, $5, $5)
	`, envID, attemptID, windowStart, windowEnd, costUSD)
	return err
}

// budgetAction is evaluateBudget's decision, pulled out as a pure
// function of (percent-of-budget, already-fired flags) so the threshold
// logic itself -- the actual "is this a bug" surface for a budget
// evaluator -- is unit-testable without a real *pgxpool.Pool or
// DestroyFunc. evaluateBudget stays the thin side-effecting wrapper
// (logging + mutating the tracked flags + invoking destroyFunc).
type budgetAction int

const (
	budgetActionNone budgetAction = iota
	budgetActionInformational50
	budgetActionWarn80
	budgetActionHardStop120
)

// decideBudgetAction mirrors doc §10.4's threshold chain: 50%
// informational, 80% warn, 120% hard-stop force-destroy. 100% ("block
// NEW environment starts") is deliberately absent from this switch --
// it's Practice Core's eligibility-check responsibility (it already
// reads usage/budget data per PLAN.md's integration point #3: "Dev A
// owns emission, Dev B owns the evaluator/UI"), not something this
// per-environment ticker can enforce (blocking new starts isn't an
// action on an already-running environment). Each threshold only fires
// once per environment (the alreadyFired flags), matching evaluateBudget's
// once-per-crossing semantics -- a budget staying above 80% across many
// ticks must not re-log a warning every 60s forever.
//
// Each case also requires every LOWER tier's flag to already be unset --
// not just this tier's own flag -- catching a real bug found by this
// package's own tests: a fast-accruing environment can jump straight
// from under 50% to over 80% between two 60s ticks, firing warn80 and
// setting alerted80=true while alerted50 stays false (its threshold was
// never independently crossed and observed). Without the `!alerted80`
// guard on the informational-50 case, every following tick would then
// fall through to "pct >= 0.5 && !alerted50" and re-fire the
// informational log line forever, even though the environment has long
// since passed that tier and a human already saw the more urgent warn.
func decideBudgetAction(budgetUSD, costUSD float64, alerted50, alerted80, stopped100 bool) budgetAction {
	if budgetUSD <= 0 {
		return budgetActionNone
	}
	pct := costUSD / budgetUSD
	switch {
	case pct >= 1.2 && !stopped100:
		return budgetActionHardStop120
	case pct >= 0.8 && !alerted80:
		return budgetActionWarn80
	case pct >= 0.5 && !alerted50 && !alerted80:
		return budgetActionInformational50
	default:
		return budgetActionNone
	}
}

func (m *Meter) evaluateBudget(ctx context.Context, envID string, t *tracked, costUSD float64) {
	pct := costUSD / m.budgetUSD
	switch decideBudgetAction(m.budgetUSD, costUSD, t.alerted50, t.alerted80, t.stopped100) {
	case budgetActionHardStop120:
		log.Warn("budget hard-stop, force-destroying",
			logging.KeyEnvID, envID, logging.KeyAttemptID, t.attemptID, logging.KeyReason, "budget",
			"cost_usd", costUSD, "pct_of_budget", pct*100, "budget_usd", m.budgetUSD)
		metrics.BudgetActionTotal.WithLabelValues("hard_stop_120").Inc()
		t.stopped100 = true
		if m.destroyFunc != nil {
			m.destroyFunc(ctx, envID)
		}
	case budgetActionWarn80:
		log.Warn("budget warning at 80%",
			logging.KeyEnvID, envID, logging.KeyAttemptID, t.attemptID,
			"cost_usd", costUSD, "pct_of_budget", pct*100, "budget_usd", m.budgetUSD)
		metrics.BudgetActionTotal.WithLabelValues("warn_80").Inc()
		t.alerted80 = true
	case budgetActionInformational50:
		log.Info("budget informational at 50%",
			logging.KeyEnvID, envID, logging.KeyAttemptID, t.attemptID,
			"cost_usd", costUSD, "pct_of_budget", pct*100, "budget_usd", m.budgetUSD)
		metrics.BudgetActionTotal.WithLabelValues("informational_50").Inc()
		t.alerted50 = true
	}
}
