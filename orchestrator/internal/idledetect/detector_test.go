package idledetect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/destroyreason"
)

// newTestDetector builds a Detector with no real k8s clients and a
// caller-supplied CPU reader + destroy recorder. It bypasses New() (which
// would install the metrics-server reader) precisely so tick()'s
// two-signal logic can be driven deterministically.
func newTestDetector(read cpuReader) (*Detector, *destroyRecorder) {
	rec := &destroyRecorder{}
	d := &Detector{
		destroyFn: rec.fn,
		readCPU:   read,
		tracked:   make(map[string]*envState),
	}
	return d, rec
}

type destroyRecorder struct {
	mu    sync.Mutex
	calls []struct {
		envID  string
		reason string
	}
}

func (r *destroyRecorder) fn(_ context.Context, envID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		envID  string
		reason string
	}{envID, reason})
}

func (r *destroyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

const testIdleTimeout = 15 * time.Minute

// makeSilent forces an already-tracked env's clock to look like it has
// been silent for `silentFor`.
func (d *Detector) makeSilent(envID string, silentFor time.Duration) {
	d.tracked[envID].lastActivity = time.Now().Add(-silentFor)
}

func constCPU(pct float64) cpuReader {
	return func(context.Context, string, int64) (float64, error) { return pct, nil }
}

func TestTick_DoesNotDestroyWhileSilentButCPUHigh(t *testing.T) {
	// This is the doc's headline guarantee: "a learner running a long
	// terraform apply has no stdin but high CPU. Killing them mid-apply
	// is the worst possible experience."
	d, rec := newTestDetector(constCPU(42.0)) // well above the 5% threshold
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", 2*time.Hour) // extremely silent

	for i := 0; i < 20; i++ {
		d.tick(context.Background())
	}

	if rec.count() != 0 {
		t.Fatalf("high-CPU env must never be destroyed on silence alone; got %d destroy calls", rec.count())
	}
	if _, stillTracked := d.tracked["env-a"]; !stillTracked {
		t.Error("env should still be tracked")
	}
	if d.tracked["env-a"].cpuLowSince != nil {
		t.Error("cpuLowSince must stay nil while CPU is above threshold")
	}
}

func TestTick_DoesNotDestroyWhileCPULowButNotSilentLongEnough(t *testing.T) {
	d, rec := newTestDetector(constCPU(0.5))
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", testIdleTimeout-2*time.Minute) // not past the idle timeout yet

	d.tick(context.Background())

	if rec.count() != 0 {
		t.Fatalf("must not destroy before the silence window elapses; got %d", rec.count())
	}
	if d.tracked["env-a"].cpuLowSince != nil {
		t.Error("cpuLowSince should not be set until the silence gate has been passed")
	}
}

func TestTick_DestroysOnlyAfterBothSilenceAndSustainedLowCPU(t *testing.T) {
	d, rec := newTestDetector(constCPU(1.0)) // below 5%
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", testIdleTimeout+time.Minute) // past the idle timeout

	// First tick: passes the silence gate, arms cpuLowSince, does NOT destroy.
	d.tick(context.Background())
	if rec.count() != 0 {
		t.Fatalf("first low-CPU tick must only arm the timer, not destroy; got %d", rec.count())
	}
	if d.tracked["env-a"].cpuLowSince == nil {
		t.Fatal("cpuLowSince should be armed after the first silent+low-CPU tick")
	}

	// Backdate cpuLowSince so the 5-minute low-CPU window has now elapsed.
	past := time.Now().Add(-(cpuLowDuration + time.Second))
	d.tracked["env-a"].cpuLowSince = &past

	d.tick(context.Background())
	if rec.count() != 1 {
		t.Fatalf("expected exactly one destroy once BOTH signals held for their windows; got %d", rec.count())
	}
	if rec.calls[0].reason != destroyreason.Idle {
		t.Errorf("expected destroy reason %q, got %q", destroyreason.Idle, rec.calls[0].reason)
	}
	if _, stillTracked := d.tracked["env-a"]; stillTracked {
		t.Error("env must be untracked after an idle destroy")
	}
}

func TestTick_CPURisingResetsTheLowCPUTimer(t *testing.T) {
	// Simulate: idle+low, timer armed, then a background job spikes CPU
	// again -> the low-CPU window must restart from zero, not carry over.
	cpu := &mutableCPU{pct: 1.0}
	d, rec := newTestDetector(cpu.read)
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", testIdleTimeout+time.Minute)

	d.tick(context.Background()) // arms cpuLowSince
	armed := d.tracked["env-a"].cpuLowSince
	if armed == nil {
		t.Fatal("expected cpuLowSince armed")
	}

	cpu.set(30.0) // CPU spikes back up
	d.tick(context.Background())
	if d.tracked["env-a"].cpuLowSince != nil {
		t.Fatal("a CPU spike above threshold must clear cpuLowSince")
	}
	if rec.count() != 0 {
		t.Fatalf("no destroy should happen while CPU is high again; got %d", rec.count())
	}

	// CPU drops again -- timer must start fresh, so even backdating the
	// *old* armed pointer is irrelevant; a single subsequent tick only
	// re-arms, doesn't destroy.
	cpu.set(1.0)
	d.tick(context.Background())
	if rec.count() != 0 {
		t.Fatalf("timer must restart from zero after the spike, not destroy immediately; got %d", rec.count())
	}
}

func TestTick_MetricsReadErrorNeverDestroys(t *testing.T) {
	// Doc's own caution: "don't destroy on a signal you couldn't
	// actually read."
	read := func(context.Context, string, int64) (float64, error) {
		return 0, errors.New("metrics-server not ready")
	}
	d, rec := newTestDetector(read)
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", 3*time.Hour)

	for i := 0; i < 10; i++ {
		d.tick(context.Background())
	}
	if rec.count() != 0 {
		t.Fatalf("a metrics read error must suppress destroy entirely; got %d", rec.count())
	}
}

func TestTick_T3MinWarningFiresExactlyOnce(t *testing.T) {
	d, _ := newTestDetector(constCPU(50.0))
	d.Track("env-a", testIdleTimeout, 2000)
	// 2 minutes short of the timeout -> inside the T-3min warning band.
	d.makeSilent("env-a", testIdleTimeout-2*time.Minute)

	d.tick(context.Background())
	if !d.tracked["env-a"].warnedAt3Min {
		t.Fatal("expected the T-3min warning flag to be set")
	}
	// Subsequent ticks in the band must not reset or re-fire it; the
	// flag staying true is the observable "fired once" property.
	d.tick(context.Background())
	if !d.tracked["env-a"].warnedAt3Min {
		t.Error("warning flag must remain set (fire-once semantics)")
	}
}

func TestRecordActivity_ResetsIdleClock(t *testing.T) {
	d, _ := newTestDetector(constCPU(1.0))
	d.Track("env-a", testIdleTimeout, 2000)
	d.makeSilent("env-a", 3*time.Hour)

	before := d.tracked["env-a"].lastActivity
	d.RecordActivity("env-a")
	if !d.tracked["env-a"].lastActivity.After(before) {
		t.Error("RecordActivity must push lastActivity forward")
	}
	// Unknown env -> no panic, no-op.
	d.RecordActivity("env-does-not-exist")
}

func TestUntrack_RemovesEnv(t *testing.T) {
	d, _ := newTestDetector(constCPU(1.0))
	d.Track("env-a", testIdleTimeout, 2000)
	d.Untrack("env-a")
	if _, ok := d.tracked["env-a"]; ok {
		t.Error("Untrack must remove the env from the tracked map")
	}
}

func TestCPUPercentFromMetrics(t *testing.T) {
	mk := func(milli ...int64) *metricsv1beta1.PodMetrics {
		pm := &metricsv1beta1.PodMetrics{}
		for _, m := range milli {
			pm.Containers = append(pm.Containers, metricsv1beta1.ContainerMetrics{
				Usage: corev1.ResourceList{
					corev1.ResourceCPU: *resource.NewMilliQuantity(m, resource.DecimalSI),
				},
			})
		}
		return pm
	}

	// 500m used of a 2000m limit -> 25%.
	if got := cpuPercentFromMetrics(mk(500), 2000); got != 25.0 {
		t.Errorf("expected 25%%, got %v", got)
	}
	// Multiple containers sum.
	if got := cpuPercentFromMetrics(mk(100, 400), 1000); got != 50.0 {
		t.Errorf("expected 50%%, got %v", got)
	}
	// Zero limit must not divide by zero -- returns 0.
	if got := cpuPercentFromMetrics(mk(500), 0); got != 0 {
		t.Errorf("expected 0 for a zero CPU limit, got %v", got)
	}
}

// mutableCPU is a cpuReader whose returned value can be changed between
// ticks.
type mutableCPU struct {
	mu  sync.Mutex
	pct float64
}

func (m *mutableCPU) read(context.Context, string, int64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pct, nil
}

func (m *mutableCPU) set(p float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pct = p
}
