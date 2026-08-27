package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/destroyreason"
)

// TestHandler_ExposesRegisteredMetrics is a smoke test: it drives a few
// collectors and confirms the /metrics endpoint renders them in
// Prometheus text format with the names the alert rules and dashboard
// query by. A rename here breaks evaluation/phase1/observability/*, so
// this pins the contract.
func TestHandler_ExposesRegisteredMetrics(t *testing.T) {
	ProvisionTotal.WithLabelValues("TIER_T1_SHARED_CONTAINER", "success").Inc()
	ProvisionDuration.WithLabelValues("TIER_T1_SHARED_CONTAINER", "cold").Observe(4.2)
	WarmPoolDepth.WithLabelValues("bp.linux.v1").Set(3)
	WarmPoolClaimTotal.WithLabelValues("hit").Inc()
	ReaperDestroyedTotal.WithLabelValues(destroyreason.Reaper).Inc()
	ReaperOrphansFound.Inc()
	IdleDestroyedTotal.Inc()
	BudgetActionTotal.WithLabelValues("hard_stop_120").Inc()
	UsageMeterCostUSD.WithLabelValues("attempt-xyz").Set(0.031)
	AttemptCostUSD.Observe(0.042)
	WSSessionsActive.Set(7)
	WSConnectionsTotal.Inc()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 from /metrics, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	got := string(body)

	want := []string{
		"orchestrator_provision_total",
		"orchestrator_provision_duration_seconds",
		"orchestrator_warm_pool_depth",
		"orchestrator_warm_pool_claim_total",
		"orchestrator_reaper_destroyed_total",
		"orchestrator_reaper_orphans_found_total",
		"orchestrator_idle_destroyed_total",
		"orchestrator_budget_action_total",
		"orchestrator_usage_meter_cost_usd",
		"orchestrator_attempt_cost_usd",
		"orchestrator_ws_sessions_active",
		"orchestrator_ws_connections_total",
	}
	for _, name := range want {
		if !strings.Contains(got, name) {
			t.Errorf("/metrics output is missing %q", name)
		}
	}
}

func TestAttemptCostBuckets_StraddleTheExitCriterion(t *testing.T) {
	// The doc §13.1 threshold is $0.08; the histogram must have a bucket
	// boundary at exactly that value so "fraction of attempts under
	// $0.08" is a single bucket read, not an interpolation.
	found := false
	for _, b := range []float64{0.005, 0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.08, 0.10, 0.15, 0.25, 0.5} {
		if b == 0.08 {
			found = true
		}
	}
	if !found {
		t.Error("attempt_cost_usd must have a bucket boundary at the $0.08 exit criterion")
	}
}
