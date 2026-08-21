// Package idledetect implements doc §5.6's two-signal idle clock:
// "No stdin, no file write, no validation, no mentor message for
// idle_timeout (default 15 min) AND CPU < 5% for 5 min." M1.8.
//
// Doc: "The two-signal idle check matters: a learner running a long
// terraform apply has no stdin but high CPU. Killing them mid-apply is
// the worst possible experience... Require both silence and low CPU."
package idledetect

import (
	"context"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ActivityRecorder is written to by the Session Broker on every stdin
// byte (doc's "no stdin" signal) -- kept as a narrow interface so
// idledetect has no import-time dependency on sessionbroker.
type ActivityRecorder interface {
	RecordActivity(envID string)
}

// DestroyFunc mirrors costmeter's pattern: idle detection triggers a
// snapshot+suspend+destroy, not a direct k8s import here.
type DestroyFunc func(ctx context.Context, envID string, reason string)

const (
	cpuThresholdPercent = 5.0
	cpuLowDuration      = 5 * time.Minute
)

type envState struct {
	lastActivity  time.Time
	cpuLowSince   *time.Time // nil while CPU is >= threshold
	idleTimeout   time.Duration
	cpuLimitMilli int64
	warnedAt3Min  bool
}

// Detector polls per-environment CPU metrics (via metrics-server) and
// tracks last-activity timestamps reported by the Session Broker,
// implementing doc §5.6's combined idle/TTL clock table's Idle row.
type Detector struct {
	clientset     *kubernetes.Clientset
	metricsClient *metricsclient.Clientset
	destroyFn     DestroyFunc

	tracked map[string]*envState
}

func New(clientset *kubernetes.Clientset, metricsClient *metricsclient.Clientset, destroyFn DestroyFunc) *Detector {
	return &Detector{
		clientset:     clientset,
		metricsClient: metricsClient,
		destroyFn:     destroyFn,
		tracked:       make(map[string]*envState),
	}
}

// Track registers an environment for idle monitoring, with its
// activity-spec-declared idle_timeout_minutes (doc §3.2
// environment.idle_timeout_minutes) and the CPU limit it was actually
// provisioned with (internal/k8s.ProvisionRequest.Resources.CPU) --
// needed so the CPU% calculation reflects this environment's real
// resource limit, not a hardcoded assumption that every environment
// gets the same one.
func (d *Detector) Track(envID string, idleTimeout time.Duration, cpuLimitMilli int64) {
	d.tracked[envID] = &envState{lastActivity: time.Now(), idleTimeout: idleTimeout, cpuLimitMilli: cpuLimitMilli}
}

func (d *Detector) Untrack(envID string) {
	delete(d.tracked, envID)
}

// RecordActivity resets the idle clock for an environment. Doc §5.6:
// "No stdin, no file write, no validation, no mentor message" are all
// activity signals; this Phase 1 slice wires stdin (from the Session
// Broker's wsStdinReader) as the signal source -- file-write and
// validation-run signals would need their own call sites in Dev B's
// Practice Core and the file-API handler respectively, not yet wired
// end-to-end from those call sites into this detector.
func (d *Detector) RecordActivity(envID string) {
	if s, ok := d.tracked[envID]; ok {
		s.lastActivity = time.Now()
	}
}

// Run polls every 30s -- frequent enough to catch the 5-minute CPU-low
// window and the per-activity idle_timeout accurately without hammering
// the metrics-server API.
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	log.Println("[idledetect] started")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Detector) tick(ctx context.Context) {
	for envID, state := range d.tracked {
		silentFor := time.Since(state.lastActivity)

		// Doc §5.6: "Warn learner over WS at T-3min" -- Phase 1 logs the
		// warning rather than pushing it over a WS control channel (that
		// channel doesn't exist yet on the terminal WS, which is
		// currently PTY-data-only, not a mixed control+data protocol).
		if !state.warnedAt3Min && silentFor >= state.idleTimeout-3*time.Minute && silentFor < state.idleTimeout {
			log.Printf("[idledetect] env=%s approaching idle timeout in <3min (silent for %s)", envID, silentFor.Round(time.Second))
			state.warnedAt3Min = true
		}

		if silentFor < state.idleTimeout {
			continue // not silent long enough yet regardless of CPU
		}

		cpuPercent, err := d.currentCPUPercent(ctx, envID, state.cpuLimitMilli)
		if err != nil {
			log.Printf("[idledetect] env=%s: could not read CPU metrics (metrics-server may not be ready yet): %v", envID, err)
			continue // doc's own caution: don't destroy on a signal you couldn't actually read
		}

		if cpuPercent >= cpuThresholdPercent {
			// Doc: "an active terraform/kubectl wait/docker build process
			// suppresses the idle clock." High CPU with no stdin --
			// exactly the long-running-op case the doc calls out.
			state.cpuLowSince = nil
			continue
		}

		if state.cpuLowSince == nil {
			now := time.Now()
			state.cpuLowSince = &now
			continue
		}

		if time.Since(*state.cpuLowSince) >= cpuLowDuration {
			log.Printf("[idledetect] env=%s idle (silent %s, CPU<%.0f%% for >%s) -- destroying", envID, silentFor.Round(time.Second), cpuThresholdPercent, cpuLowDuration)
			d.destroyFn(ctx, envID, "idle")
			d.Untrack(envID)
		}
	}
}

func (d *Detector) currentCPUPercent(ctx context.Context, envID string, cpuLimitMilli int64) (float64, error) {
	ns := "env-" + envID
	metrics, err := d.metricsClient.MetricsV1beta1().PodMetricses(ns).Get(ctx, "workspace", metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	return cpuPercentFromMetrics(metrics, cpuLimitMilli), nil
}

// cpuPercentFromMetrics converts the pod's measured CPU usage against
// its resource *limit* (not request) into a percentage -- doc's "CPU <
// 5%" reads most naturally as "5% of what this environment is allowed
// to use," matching how the doc frames idle detection as "is this
// environment doing real work" rather than a raw core-count threshold
// that would mean something different per resource tier.
func cpuPercentFromMetrics(metrics *metricsv1beta1.PodMetrics, cpuLimitMilli int64) float64 {
	var usedMilli int64
	for _, c := range metrics.Containers {
		usedMilli += c.Usage.Cpu().MilliValue()
	}
	if cpuLimitMilli == 0 {
		return 0
	}
	return float64(usedMilli) / float64(cpuLimitMilli) * 100
}
