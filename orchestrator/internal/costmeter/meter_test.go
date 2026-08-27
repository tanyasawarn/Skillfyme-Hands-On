package costmeter

import (
	"context"
	"testing"
)

func TestDecideBudgetAction_ZeroOrNegativeBudgetNeverFires(t *testing.T) {
	for _, budget := range []float64{0, -1, -100} {
		got := decideBudgetAction(budget, 1000, false, false, false)
		if got != budgetActionNone {
			t.Errorf("budget=%v: expected budgetActionNone (unlimited/disabled budget must never trigger an action), got %v", budget, got)
		}
	}
}

func TestDecideBudgetAction_Thresholds(t *testing.T) {
	const budget = 0.08 // matches DEFAULT_BUDGET_USD
	tests := []struct {
		name       string
		costUSD    float64
		alerted50  bool
		alerted80  bool
		stopped100 bool
		want       budgetAction
	}{
		{"well under 50%", 0.01, false, false, false, budgetActionNone},
		{"exactly 50%", 0.04, false, false, false, budgetActionInformational50},
		{"between 50 and 80%", 0.06, false, false, false, budgetActionInformational50},
		{"exactly 80%", 0.064, false, false, false, budgetActionWarn80},
		{"between 80 and 120%", 0.07, false, false, false, budgetActionWarn80},
		{"exactly 120%", 0.096, false, false, false, budgetActionHardStop120},
		{"well over 120%", 1.0, false, false, false, budgetActionHardStop120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBudgetAction(budget, tt.costUSD, tt.alerted50, tt.alerted80, tt.stopped100)
			if got != tt.want {
				t.Errorf("cost=$%.4f: expected action %v, got %v", tt.costUSD, tt.want, got)
			}
		})
	}
}

// TestDecideBudgetAction_FiresOnlyOncePerThreshold is a regression-style
// test for the once-per-crossing semantics evaluateBudget relies on: a
// budget stuck above 80% across dozens of 60s ticks must not re-fire the
// warn action every single tick forever -- only the flag transition
// (false -> true) should ever produce a non-None action.
func TestDecideBudgetAction_FiresOnlyOncePerThreshold(t *testing.T) {
	const budget = 0.08
	const overWarnThreshold = 0.07 // 87.5% of budget

	first := decideBudgetAction(budget, overWarnThreshold, false, false, false)
	if first != budgetActionWarn80 {
		t.Fatalf("expected first crossing to fire budgetActionWarn80, got %v", first)
	}
	// Second call simulates the caller having already set alerted80=true
	// after acting on the first result.
	second := decideBudgetAction(budget, overWarnThreshold, false, true, false)
	if second != budgetActionNone {
		t.Errorf("expected repeat ticks at the same cost to fire nothing once alerted80 is set, got %v", second)
	}
}

// TestDecideBudgetAction_HardStopFiresEvenIfLowerThresholdsWereSkipped
// covers a real edge case: a fast-accruing environment (e.g. a huge
// resource spec) could jump straight from under 50% to over 120% between
// two 60s ticks, skipping the 50%/80% log lines entirely. The hard-stop
// must still fire in that case -- budget enforcement's whole point is
// the 120% ceiling, and it must not depend on having observed the
// intermediate thresholds first.
func TestDecideBudgetAction_HardStopFiresEvenIfLowerThresholdsWereSkipped(t *testing.T) {
	got := decideBudgetAction(0.08, 1.0, false, false, false)
	if got != budgetActionHardStop120 {
		t.Errorf("expected hard-stop to fire regardless of alerted50/alerted80 state, got %v", got)
	}
}

func TestEvaluateBudget_HardStopInvokesDestroyFunc(t *testing.T) {
	var destroyedEnvID string
	destroyCalls := 0
	m := &Meter{
		budgetUSD: 0.08,
		destroyFunc: func(ctx context.Context, envID string) {
			destroyedEnvID = envID
			destroyCalls++
		},
	}
	tr := &tracked{attemptID: "attempt-1"}

	m.evaluateBudget(context.Background(), "env-1", tr, 1.0) // 1250% of budget

	if destroyCalls != 1 {
		t.Fatalf("expected destroyFunc to be called exactly once, got %d calls", destroyCalls)
	}
	if destroyedEnvID != "env-1" {
		t.Errorf("expected destroyFunc called with env-1, got %q", destroyedEnvID)
	}
	if !tr.stopped100 {
		t.Error("expected stopped100 flag to be set after hard-stop")
	}
}

// TestEvaluateBudget_HardStopOnlyDestroysOnce is the concrete
// production-shaped version of the once-per-crossing regression above:
// evaluateBudget runs on every 60s tick for every active environment,
// so an environment sitting at 200% of budget across many ticks (e.g.
// destroyFunc itself is slow, or fails and the environment is still
// "active" for a few more ticks) must never trigger destroyFunc a
// second time.
func TestEvaluateBudget_HardStopOnlyDestroysOnce(t *testing.T) {
	destroyCalls := 0
	m := &Meter{
		budgetUSD: 0.08,
		destroyFunc: func(ctx context.Context, envID string) {
			destroyCalls++
		},
	}
	tr := &tracked{attemptID: "attempt-1"}

	m.evaluateBudget(context.Background(), "env-1", tr, 1.0)
	m.evaluateBudget(context.Background(), "env-1", tr, 1.0)
	m.evaluateBudget(context.Background(), "env-1", tr, 1.0)

	if destroyCalls != 1 {
		t.Errorf("expected exactly 1 destroyFunc call across 3 ticks at the same over-budget cost, got %d", destroyCalls)
	}
}

func TestEvaluateBudget_NilDestroyFuncDoesNotPanic(t *testing.T) {
	m := &Meter{budgetUSD: 0.08, destroyFunc: nil}
	tr := &tracked{attemptID: "attempt-1"}
	// Must not panic even though destroyFunc is nil -- a Meter
	// constructed without one (e.g. in a future test or a deployment
	// that deliberately disables hard-stop) should degrade to
	// log-only, not crash the metering loop for every other active
	// environment.
	m.evaluateBudget(context.Background(), "env-1", tr, 1.0)
	if !tr.stopped100 {
		t.Error("expected stopped100 to still be set even with a nil destroyFunc")
	}
}

func TestEvaluateBudget_WarnDoesNotInvokeDestroyFunc(t *testing.T) {
	destroyCalls := 0
	m := &Meter{
		budgetUSD:   0.08,
		destroyFunc: func(ctx context.Context, envID string) { destroyCalls++ },
	}
	tr := &tracked{attemptID: "attempt-1"}

	m.evaluateBudget(context.Background(), "env-1", tr, 0.07) // 87.5%, warn tier only

	if destroyCalls != 0 {
		t.Errorf("SECURITY/CORRECTNESS REGRESSION: expected the 80%% warn tier to never force-destroy a learner's environment, got %d destroyFunc calls", destroyCalls)
	}
	if !tr.alerted80 {
		t.Error("expected alerted80 to be set")
	}
	if tr.stopped100 {
		t.Error("expected stopped100 to remain false at the warn tier")
	}
}

func TestStartMetering_TracksNewEnvironment(t *testing.T) {
	m := &Meter{active: make(map[string]*tracked)}
	m.StartMetering("env-1", "attempt-1")

	m.mu.Lock()
	defer m.mu.Unlock()
	tr, ok := m.active["env-1"]
	if !ok {
		t.Fatal("expected env-1 to be tracked after StartMetering")
	}
	if tr.attemptID != "attempt-1" {
		t.Errorf("expected attemptID=attempt-1, got %q", tr.attemptID)
	}
}

func TestStopMetering_RemovesEnvironment(t *testing.T) {
	m := &Meter{active: make(map[string]*tracked)}
	m.StartMetering("env-1", "attempt-1")
	m.StopMetering("env-1")

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.active["env-1"]; ok {
		t.Error("expected env-1 to no longer be tracked after StopMetering -- a destroyed environment must stop accruing cost/budget evaluations forever, not just pause them")
	}
}

func TestStopMetering_UnknownEnvironmentIsNoop(t *testing.T) {
	m := &Meter{active: make(map[string]*tracked)}
	// Must not panic on an envID that was never started (e.g. a
	// double-Destroy race, or a Destroy call for an environment this
	// meter process never saw StartMetering for).
	m.StopMetering("never-started")
}
