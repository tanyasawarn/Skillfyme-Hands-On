// Package costmeter implements doc §10.4's budget enforcement chain and
// §8.4's usage_meter emission: "environments report metered usage every
// 60s; budget breaches flow back as commands." M1.9.
package costmeter

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	defer m.mu.Unlock()
	delete(m.active, envID)
}

func (m *Meter) Close() {
	close(m.stopCh)
}

// loop emits a usage_meter row every 60s per doc §8.4/§10.3, and runs
// the budget evaluator chain (doc §10.4: 50% informational, 80% warn,
// 100% block-new-starts [not implemented in Phase 1 -- see note below],
// 120% force-destroy).
func (m *Meter) loop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.tick()
		}
	}
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
			log.Printf("[costmeter] failed to emit usage_meter row for env=%s: %v", envID, err)
		}

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

// Doc §10.4 budget evaluator chain. 100% ("block NEW environment starts
// in scope") is Practice Core's eligibility-check responsibility (it
// already reads usage/budget data per PLAN.md's integration point #3:
// "Dev A owns emission, Dev B owns the evaluator/UI") -- this Phase 1
// meter emits the informational/warn log lines and owns only the 120%
// hard-stop, which is the one budget action that must happen at the
// infrastructure layer regardless of whether Practice Core's evaluator
// is reachable.
func (m *Meter) evaluateBudget(ctx context.Context, envID string, t *tracked, costUSD float64) {
	if m.budgetUSD <= 0 {
		return
	}
	pct := costUSD / m.budgetUSD

	switch {
	case pct >= 1.2 && !t.stopped100:
		log.Printf("[costmeter] BUDGET HARD-STOP env=%s attempt=%s cost=$%.4f (%.0f%% of $%.2f budget) -- force-destroying", envID, t.attemptID, costUSD, pct*100, m.budgetUSD)
		t.stopped100 = true
		if m.destroyFunc != nil {
			m.destroyFunc(ctx, envID)
		}
	case pct >= 0.8 && !t.alerted80:
		log.Printf("[costmeter] budget warn env=%s attempt=%s cost=$%.4f (%.0f%% of $%.2f budget)", envID, t.attemptID, costUSD, pct*100, m.budgetUSD)
		t.alerted80 = true
	case pct >= 0.5 && !t.alerted50:
		log.Printf("[costmeter] budget informational env=%s attempt=%s cost=$%.4f (%.0f%% of $%.2f budget)", envID, t.attemptID, costUSD, pct*100, m.budgetUSD)
		t.alerted50 = true
	}
}
