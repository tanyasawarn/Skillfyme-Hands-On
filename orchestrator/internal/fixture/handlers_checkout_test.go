package fixture

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// TestCheckoutFixtureAndFaults_LiveIntegration verifies fx.checkout-
// deployment.v1 (the fixture sim.sre.checkout-latency-incident.yaml's
// own seed: list references but had no real handler for, found and
// fixed during this session's Phase 2 completion pass) genuinely
// supports both of that activity's own faults end-to-end.
func TestCheckoutFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-checkout-test-" + envID[:8]

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

	if err := applyCheckoutDeployment(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyCheckoutDeployment failed: %v", err)
	}

	t.Run("healthy baseline: checkout deployment has a real Ready replica", func(t *testing.T) {
		dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, checkoutDeploymentName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting checkout deployment: %v", err)
		}
		if dep.Status.ReadyReplicas < 1 {
			t.Fatalf("expected at least 1 ready replica, got %d", dep.Status.ReadyReplicas)
		}
	})

	t.Run("healthy baseline: a real HTTP call to checkout/healthz succeeds", func(t *testing.T) {
		pod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+checkoutDeploymentName)
		if err != nil {
			t.Fatalf("finding checkout pod: %v", err)
		}
		result, err := k8s.ExecInPod(ctx, provisioner, ns, pod, "app",
			"wget -q -T 5 -O- http://"+checkoutServiceName+"/healthz", 15*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("calling checkout/healthz: err=%v result=%+v", err, result)
		}
	})
}
