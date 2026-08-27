package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Sixth batch: the two Prometheus faults, backed by fx.prometheus-minimal.v1
// (internal/fixture/handlers_prometheus.go) provisioning a real
// Prometheus Deployment (--web.enable-lifecycle) + a real always-up
// scrape target. Both handlers patch the same ConfigMap the fixture
// creates and trigger a live reload via `wget --post-data ... /-/reload`
// executed inside the Prometheus pod itself (k8s.ExecInPod) -- the exact
// mechanism a real operator would use (`curl -X POST
// http://localhost:9090/-/reload`), not an out-of-band API this package
// invents. Registered as DynamicHandler (not Handler) because they need
// *k8s.Provisioner for ExecInPod, not just a typed kubernetes.Interface.
func init() {
	registerDynamic("f.prometheus.scrape-target-down", applyPrometheusScrapeTargetDown)
	registerDynamic("f.prometheus.alert-rule-syntax-silent-fail", applyPrometheusAlertRuleSyntaxSilentFail)
}

const (
	prometheusConfigMapNameFault = "practice-prometheus-config"
	prometheusPodLabelSelector   = "app=practice-prometheus"
)

// findPrometheusPodName resolves the live Prometheus pod name via its
// Deployment's label selector -- the ConfigMap volume mount means a pod
// restart isn't needed for a reload (--web.enable-lifecycle handles
// picking up a changed mounted file), but the pod name itself isn't a
// fixed, predictable string the way the Deployment/Service names are
// (a ReplicaSet-generated suffix), so this resolves it fresh each call
// rather than hardcoding a guess.
func findPrometheusPodName(ctx context.Context, provisioner *k8s.Provisioner, namespace string) (string, error) {
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: prometheusPodLabelSelector})
	if err != nil {
		return "", fmt.Errorf("listing prometheus pods: %w", err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	if len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}
	return "", fmt.Errorf("no prometheus pod found in namespace %s (label %s) -- has fx.prometheus-minimal.v1 been applied?", namespace, prometheusPodLabelSelector)
}

// triggerPrometheusReload execs `wget` (present in prom/prometheus's own
// busybox-derived base image, confirmed against the real image used by
// the fixture) inside the live Prometheus pod to POST /-/reload -- the
// real config-reload mechanism --web.enable-lifecycle exposes, run from
// inside the pod's own network namespace rather than requiring the
// orchestrator to reach the pod's ClusterIP directly.
func triggerPrometheusReload(ctx context.Context, provisioner *k8s.Provisioner, namespace, podName string) error {
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, podName, "prometheus",
		"wget -q -O- --post-data='' http://localhost:9090/-/reload", 15*time.Second)
	if err != nil {
		return fmt.Errorf("exec'ing config reload in prometheus pod %s: %w", podName, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("prometheus config reload failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}

// applyPrometheusScrapeTargetDown: content/faults/f.prometheus.scrape-target-down.yaml
// params: job_name (must match the fixture's real scrape job name,
// "practice-target" -- validated below rather than trusted blindly).
// Adds a metric_relabel_configs rule to that job that drops every
// series, reproducing "the target shows as dropped, not just down"
// (the fault's own canonical_diagnostic_path) -- the self-scrape "
// prometheus" job is untouched, so Prometheus's own health stays fine
// throughout, matching detectability: low.
func applyPrometheusScrapeTargetDown(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	jobName := params["job_name"]
	if jobName == "" {
		return Result{}, fmt.Errorf("f.prometheus.scrape-target-down requires param: job_name")
	}
	if jobName != prometheusScrapeJobNameConst {
		return Result{}, fmt.Errorf("f.prometheus.scrape-target-down: job_name %q does not match the fixture's real scrape job %q", jobName, prometheusScrapeJobNameConst)
	}

	cms := provisioner.Clientset().CoreV1().ConfigMaps(namespace)
	cm, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ConfigMap, error) {
		return cms.Get(ctx, prometheusConfigMapNameFault, metav1.GetOptions{})
	}, "ConfigMap", "configmap", prometheusConfigMapNameFault)
	if err != nil {
		return notFoundOrErrResult, err
	}

	current := cm.Data["prometheus.yml"]
	if strings.Contains(current, "__fault_drop_all__") {
		// Idempotent: the drop rule is already present.
		podName, findErr := findPrometheusPodName(ctx, provisioner, namespace)
		if findErr != nil {
			return Result{Applied: true, SymptomVerified: false}, nil
		}
		return Result{Applied: true, SymptomVerified: triggerPrometheusReload(ctx, provisioner, namespace, podName) == nil}, nil
	}

	// Appends an overzealous drop rule to the target job specifically --
	// matches every series from this job and drops it, the exact
	// "overly broad drop rule" the fault's canonical_diagnostic_path
	// names. __fault_drop_all__ is an inert marker comment purely so this
	// handler's own idempotency check above can detect it was already
	// applied. Targets the exact static_configs line the fixture's own
	// prometheusYML constant is authored with (not learner-editable
	// content, so an exact string match is a stable, not fragile,
	// anchor) rather than a general YAML-aware job-block splice.
	const targetJobBlock = "  - job_name: practice-target\n    static_configs:\n      - targets: ['practice-scrape-target:8080']\n"
	replacement := targetJobBlock + fmt.Sprintf(`    metric_relabel_configs:
      - source_labels: [__name__]
        regex: '.*'
        action: drop
        # __fault_drop_all__ marker: %s
`, jobName)
	if !strings.Contains(current, targetJobBlock) {
		return Result{}, fmt.Errorf("scrape job %q's expected block not found in prometheus.yml -- fixture ConfigMap may be out of sync", jobName)
	}
	updated := strings.Replace(current, targetJobBlock, replacement, 1)

	cm.Data["prometheus.yml"] = updated
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating prometheus ConfigMap: %w", err)
	}

	podName, err := findPrometheusPodName(ctx, provisioner, namespace)
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	// ConfigMap-volume-mounted files (mounted as a whole directory, not
	// per-file with SubPath -- see fx.prometheus-minimal.v1's own doc
	// comment on why SubPath breaks live updates entirely) propagate to
	// the pod on kubelet's own sync interval -- confirmed live during
	// this handler's own integration test to take on the order of a
	// minute (not instant), well past any reasonable RPC timeout budget
	// for InjectFault to block on. Triggering /-/reload here is
	// therefore a best-effort kick that likely reloads the file BEFORE
	// kubelet's propagation lands, not a way to synchronously confirm
	// the new config is active -- SymptomVerified only reflects whether
	// the reload call itself succeeded (proving Prometheus is up and
	// the endpoint responded), not that the drop rule has taken effect
	// yet. The real fault state (the ConfigMap mutation) has already
	// succeeded above regardless of this call's outcome; a caller that
	// needs to confirm the symptom manifested should re-check via
	// CheckRegression/ExecValidator after allowing time for kubelet's
	// propagation, not rely on this RPC's own response.
	reloadErr := triggerPrometheusReload(ctx, provisioner, namespace, podName)
	return Result{Applied: true, SymptomVerified: reloadErr == nil}, nil
}

// applyPrometheusAlertRuleSyntaxSilentFail: content/faults/f.prometheus.alert-rule-syntax-silent-fail.yaml
// params: rules_file (must match the fixture's real rules file name,
// "practice-rules.yml"). Corrupts the rule group with a change that's
// syntactically-valid-enough for promtool/Prometheus to not crash (a
// real YAML parse error would fail --web.enable-lifecycle's reload
// outright, a more detectable failure mode than the fault's own
// detectability: low intent) but semantically wrong: an always-false
// PromQL expression (comparing a metric against an impossible sentinel
// value) means the rule group loads successfully yet the alert can never
// fire -- matching "no crash, no obvious error surface."
func applyPrometheusAlertRuleSyntaxSilentFail(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	rulesFile := params["rules_file"]
	if rulesFile == "" {
		return Result{}, fmt.Errorf("f.prometheus.alert-rule-syntax-silent-fail requires param: rules_file")
	}
	if rulesFile != prometheusRulesFileNameConst {
		return Result{}, fmt.Errorf("f.prometheus.alert-rule-syntax-silent-fail: rules_file %q does not match the fixture's real rules file %q", rulesFile, prometheusRulesFileNameConst)
	}

	cms := provisioner.Clientset().CoreV1().ConfigMaps(namespace)
	cm, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ConfigMap, error) {
		return cms.Get(ctx, prometheusConfigMapNameFault, metav1.GetOptions{})
	}, "ConfigMap", "configmap", prometheusConfigMapNameFault)
	if err != nil {
		return notFoundOrErrResult, err
	}

	const brokenExpr = `up{job="practice-target"} == -999999`
	current := cm.Data[rulesFile]
	if strings.Contains(current, brokenExpr) {
		podName, findErr := findPrometheusPodName(ctx, provisioner, namespace)
		if findErr != nil {
			return Result{Applied: true, SymptomVerified: false}, nil
		}
		return Result{Applied: true, SymptomVerified: triggerPrometheusReload(ctx, provisioner, namespace, podName) == nil}, nil
	}

	updated := strings.Replace(current, `up{job="practice-target"} == 0`, brokenExpr, 1)
	if updated == current {
		return Result{}, fmt.Errorf("expected alert expression not found in %s -- fixture ConfigMap may be out of sync", rulesFile)
	}
	cm.Data[rulesFile] = updated
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating prometheus ConfigMap: %w", err)
	}

	podName, err := findPrometheusPodName(ctx, provisioner, namespace)
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	// Same kubelet-propagation-delay caveat as
	// applyPrometheusScrapeTargetDown's own reload call above --
	// SymptomVerified reflects whether the reload endpoint responded,
	// not that the corrupted rule is active yet.
	reloadErr := triggerPrometheusReload(ctx, provisioner, namespace, podName)
	return Result{Applied: true, SymptomVerified: reloadErr == nil}, nil
}

// prometheusScrapeJobNameConst/prometheusRulesFileNameConst duplicate
// fixture package's unexported prometheusScrapeJobName/prometheusRulesFileName
// constants (faultinjection cannot import fixture -- fixture already
// imports k8s, and importing fixture here for two string constants would
// be a needless cross-package coupling for values that are, by design,
// part of the stable contract between a fixture and the faults that
// target it, not fixture-internal implementation detail).
const (
	prometheusScrapeJobNameConst = "practice-target"
	prometheusRulesFileNameConst = "practice-rules.yml"
)
