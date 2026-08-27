// Package metrics is the orchestrator's Prometheus instrumentation
// surface (doc §11 "Observability and admin analytics"). It exists so
// the Phase 1 exit criteria (doc §13.1) are *measured*, not asserted:
//
//   - time-to-ready p95 ≤ 20s  -> ProvisionDuration histogram
//   - provision success ≥ 99%  -> ProvisionTotal{result=}
//   - cost/attempt < $0.08     -> AttemptCostUSD histogram + UsageMeterCostUSD
//   - zero orphan environments -> ReaperOrphansFound
//
// A leaf package: it imports only client_golang, so every other
// internal/ package (reaper, warmpool, idledetect, costmeter,
// orchestrator) can record into it without an import cycle -- the same
// shape as internal/destroyreason and internal/ttl.
//
// Every metric is registered on the default registry via promauto, so
// Handler() (promhttp.Handler) exposes them with no extra wiring.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "orchestrator"

var (
	// ProvisionDuration is the wall-clock time from a Provision RPC
	// arriving to the environment reaching READY (health gate passed).
	// The doc's headline exit criterion -- "time-to-ready p95 ≤ 20s" --
	// is histogram_quantile(0.95, ...) over this. Buckets are chosen
	// around the 20s SLO so the p95 estimate is accurate near the
	// threshold that matters.
	ProvisionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "provision_duration_seconds",
		Help:      "Time from Provision RPC to environment READY.",
		Buckets:   []float64{1, 2, 3, 5, 8, 12, 16, 20, 25, 30, 45, 60, 90},
	}, []string{"tier", "source"}) // source: "cold" | "warm"

	// ProvisionTotal counts Provision outcomes. provision success rate =
	// rate(...{result="success"}) / rate(...) -- exit criterion ≥ 99%.
	ProvisionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "provision_total",
		Help:      "Provision RPC outcomes.",
	}, []string{"tier", "result"}) // result: "success" | "failed"

	// WarmPoolDepth is the current number of pre-provisioned, claimable
	// environments per blueprint (doc §5.5). A gauge -- set from the
	// filler loop each tick.
	WarmPoolDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "warm_pool_depth",
		Help:      "Claimable warm environments per blueprint.",
	}, []string{"blueprint"})

	// WarmPoolClaimTotal counts warm-pool claim attempts by outcome.
	// hit / (hit+miss) is the warm-pool effectiveness ratio.
	WarmPoolClaimTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "warm_pool_claim_total",
		Help:      "Warm-pool claim attempts by outcome.",
	}, []string{"result"}) // result: "hit" | "miss"

	// ReaperDestroyedTotal counts environments force-destroyed by the
	// reaper, by the destroyreason that triggered the teardown.
	ReaperDestroyedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "reaper_destroyed_total",
		Help:      "Environments force-destroyed by the reaper, by reason.",
	}, []string{"reason"})

	// ReaperOrphansFound counts namespaces the orphan sweep found with
	// no reaper record -- doc §13.1's zero-orphan gate is
	// increase(orchestrator_reaper_orphans_found_total[1h]) == 0.
	ReaperOrphansFound = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "reaper_orphans_found_total",
		Help:      "Namespaces found by the orphan sweep with no reaper record.",
	})

	// IdleDestroyedTotal counts environments torn down by the two-signal
	// idle detector (doc §5.6).
	IdleDestroyedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "idle_destroyed_total",
		Help:      "Environments destroyed by the idle detector.",
	})

	// BudgetActionTotal counts budget-evaluator actions by tier
	// (doc §10.4: informational-50, warn-80, hard-stop-120).
	BudgetActionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "budget_action_total",
		Help:      "Budget evaluator actions taken, by threshold tier.",
	}, []string{"tier"}) // tier: "informational_50" | "warn_80" | "hard_stop_120"

	// UsageMeterCostUSD is the running accrued cost of the most recent
	// usage_meter emission for an environment (doc §8.4). A gauge keyed
	// by attempt so a dashboard can show live spend per attempt; the
	// authoritative history is still the env.usage_meter table.
	UsageMeterCostUSD = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "usage_meter_cost_usd",
		Help:      "Latest accrued cost (USD) for an environment's attempt.",
	}, []string{"attempt_id"})

	// AttemptCostUSD is the *final* metered cost of an attempt, recorded
	// once when its environment is torn down. This is the exit-criterion
	// distribution: histogram_quantile / avg over it is "cost per
	// attempt", which must sit under $0.08.
	AttemptCostUSD = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "attempt_cost_usd",
		Help:      "Final metered cost (USD) per attempt at environment teardown.",
		Buckets:   []float64{0.005, 0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.08, 0.10, 0.15, 0.25, 0.5},
	})

	// WSSessionsActive is the current number of live terminal WS
	// sessions across the gateway (doc §5.4).
	WSSessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ws_sessions_active",
		Help:      "Currently connected terminal WebSocket sessions.",
	})

	// WSConnectionsTotal counts terminal WS connections accepted by the
	// gateway. The gateway is stateless (doc §8.1) and cannot itself
	// distinguish a first connect from a reconnect, so this is the raw
	// accept count; reconnect rate is
	// (ws_connections_total - distinct attempts) or is tracked
	// client-side. A sharp rise relative to active attempts still
	// signals gateway/network instability affecting learners mid-lab
	// (doc §5.4 reconnect/scrollback).
	WSConnectionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ws_connections_total",
		Help:      "Terminal WebSocket connections accepted by the gateway.",
	})
)

// Handler returns the HTTP handler that serves the registered metrics in
// Prometheus text exposition format. main.go mounts it at /metrics.
func Handler() http.Handler {
	return promhttp.Handler()
}
