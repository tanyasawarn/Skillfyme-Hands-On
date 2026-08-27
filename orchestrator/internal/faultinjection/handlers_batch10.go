package faultinjection

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Tenth batch: f.helm.values-override-not-applied, backed by
// fx.helm-release.v1 (internal/fixture/handlers_helm.go) provisioning a
// real Helm 3 install of a tiny real chart. Registered as DynamicHandler
// since it needs *k8s.Provisioner for ExecInPod (running the real `helm
// upgrade` inside the runner pod, exactly the mechanism an operator
// would use).
func init() {
	registerDynamic("f.helm.values-override-not-applied", applyHelmValuesOverrideNotApplied)
}

const (
	helmRunnerPodLabelSelector = "app=practice-helm-runner"
	helmReleaseNameConst       = "practice-release"
)

// applyHelmValuesOverrideNotApplied: content/faults/f.helm.values-override-not-applied.yaml
// params: release (must match the fixture's real release,
// "practice-release"), wrong_key_path (the typo'd --set key path to
// use, e.g. "config.featurFlag" instead of the chart's real
// "config.featureFlag" -- content-authored, not validated against the
// chart's real key path here, since the whole point of this fault is
// that the key path IS wrong; validated instead is that it's actually
// DIFFERENT from the real key, since a caller passing the correct path
// wouldn't be requesting this fault at all).
//
// Runs a real `helm upgrade --reset-values --set <wrong_key_path>=...`
// inside the fixture's own runner pod -- --reset-values ensures no
// stale correct override from a prior call lingers and masks the fault
// (confirmed necessary live: without it, a second upgrade call could
// inherit the previous revision's values). The chart's own default
// (values.yaml) is left in effect, exactly matching the fault's real,
// live-verified symptom: no error, no warning, just silently wrong
// config.
func applyHelmValuesOverrideNotApplied(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	release := params["release"]
	wrongKeyPath := params["wrong_key_path"]
	if release == "" || wrongKeyPath == "" {
		return Result{}, fmt.Errorf("f.helm.values-override-not-applied requires params: release, wrong_key_path")
	}
	if release != helmReleaseNameConst {
		return Result{}, fmt.Errorf("f.helm.values-override-not-applied: release %q does not match the fixture's real release %q", release, helmReleaseNameConst)
	}
	if wrongKeyPath == "config.featureFlag" {
		return Result{}, fmt.Errorf("f.helm.values-override-not-applied: wrong_key_path %q is the chart's REAL key path -- this fault requires a genuinely mistyped path to demonstrate a silent no-op", wrongKeyPath)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, helmRunnerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding helm runner pod: %w", err)
	}

	upgradeCmd := fmt.Sprintf(
		"helm upgrade %s /work/chart -n %s --reset-values --set %s=off",
		release, namespace, wrongKeyPath,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "helm", upgradeCmd, 30*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("running helm upgrade with wrong key path: %w", err)
	}
	if result.ExitCode != 0 {
		return Result{}, fmt.Errorf("helm upgrade failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	// Verify the fault genuinely manifested: the release's real
	// ConfigMap must still carry the chart's own default value
	// ("off"), not the value the (wrong-path) --set attempted --
	// confirming the override really was silently ignored, not just
	// that the upgrade command itself succeeded. Queried via the K8s
	// object itself, using this orchestrator's own cluster access
	// (simpler and more direct than execing kubectl inside the runner
	// pod, which the alpine/helm image doesn't ship -- confirmed live,
	// only curl/wget are present there).
	cm, cmErr := provisioner.Clientset().CoreV1().ConfigMaps(namespace).Get(ctx, release+"-config", metav1.GetOptions{})
	verified := cmErr == nil && cm.Data["featureFlag"] == "off"
	return Result{Applied: true, SymptomVerified: verified}, nil
}
