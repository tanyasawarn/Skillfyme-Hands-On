package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func TestHelmFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-helm-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyHelmRelease(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyHelmRelease failed: %v", err)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+helmRunnerDeployment)
	if err != nil {
		t.Fatalf("finding helm runner pod: %v", err)
	}

	t.Run("healthy baseline: real helm install produces the chart's default ConfigMap value", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, helmReleaseName+"-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("expected a real ConfigMap %s-config, got: %v", helmReleaseName, err)
		}
		if cm.Data["featureFlag"] != "off" {
			t.Fatalf("expected the chart's own default (off), got: %q", cm.Data["featureFlag"])
		}
	})

	t.Run("healthy override: a correct --set key path genuinely changes the rendered value", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "helm",
			"helm upgrade "+helmReleaseName+" /work/chart -n "+ns+" --reset-values --set config.featureFlag=on", 30*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("helm upgrade failed: err=%v result=%+v", err, result)
		}
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, helmReleaseName+"-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading ConfigMap after correct override: %v", err)
		}
		if cm.Data["featureFlag"] != "on" {
			t.Fatalf("expected the correct override to apply (on), got: %q", cm.Data["featureFlag"])
		}
	})

	t.Run("f.helm.values-override-not-applied: a typo'd key path is silently ignored", func(t *testing.T) {
		upgradeCmd := "helm upgrade " + helmReleaseName + " /work/chart -n " + ns + " --reset-values --set config.featurFlag=off"
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "helm", upgradeCmd, 30*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("helm upgrade (with typo'd key) failed: err=%v result=%+v", err, result)
		}

		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, helmReleaseName+"-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading ConfigMap after fault: %v", err)
		}
		if cm.Data["featureFlag"] != "off" {
			t.Fatalf("CORRECTNESS REGRESSION: expected the chart's own default (off) since the --set key path was mistyped, got: %q -- either the typo'd key accidentally matched something, or Helm's real behavior changed", cm.Data["featureFlag"])
		}

		valuesResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "helm",
			"helm get values "+helmReleaseName+" -n "+ns, 15*time.Second)
		if err != nil {
			t.Fatalf("getting recorded values: %v", err)
		}
		if !strings.Contains(valuesResult.Stdout, "featurFlag") {
			t.Fatalf("expected 'helm get values' to show the typo'd key was recorded as user-supplied (matching the fault's own canonical_diagnostic_path), got:\n%s", valuesResult.Stdout)
		}
	})
}
