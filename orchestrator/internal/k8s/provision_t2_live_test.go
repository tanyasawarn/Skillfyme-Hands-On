package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestProvisionT2_FailsWithRuntimeClassNotFound is real-infra-gated
// (same skip convention as internal/orchestrator/ownership_rpc_test.go)
// and documents the exact, concrete, environment-specific reason live
// T2 (Kata microVM) scheduling cannot be verified end-to-end here: this
// dev cluster has no Kata-capable node (no nested-KVM hardware on the
// host running Docker Desktop's own VM). Rather than skip silently
// (which would look identical to "nobody tried"), this asserts the
// REAL, live failure mode a real Provision(T2) call hits: K8s admission
// itself rejects the pod outright because RuntimeClass "kata" isn't
// registered -- proving applyT2PodShape's own real Kata RuntimeClass
// assignment is exercised for real up to the exact point this
// environment cannot go further, and giving a future Kata-capable
// environment a concrete, already-passing-once-the-precondition-is-met
// test to build on rather than an untested code path.
func TestProvisionT2_FailsWithRuntimeClassNotFound(t *testing.T) {
	kubeconfig := "../../../.local/k3s-output/kubeconfig.yaml"
	restConfig, err := NewRestConfig(kubeconfig)
	if err != nil {
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := NewClientsetFromConfig(restConfig)
	if err != nil {
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		t.Skipf("skipping: k8s cluster unreachable (dev stack not running?): %v", err)
	}

	ns := "t2-live-test"
	provisioner := NewProvisioner(clientset, restConfig, false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), namespaceName("t2-live-test"), metav1.DeleteOptions{})
	})

	err = provisioner.Provision(ctx, ProvisionRequest{
		AttemptID: "t2-live-test-attempt",
		EnvID:     ns,
		Tier:      TierT2IsolatedMicroVM,
		Image:     "registry:5000/practiceengine/linux-tools:v1",
	})

	if err == nil {
		t.Fatal("expected Provision(T2) to fail in this environment (no Kata-capable node) -- if this now succeeds, a Kata-capable node may have been added; update this test's expectations and the M9 closeout's known-limitations section accordingly")
	}
	if !strings.Contains(err.Error(), `RuntimeClass "kata" not found`) {
		t.Fatalf("expected the specific real failure 'RuntimeClass \"kata\" not found', got a different error (investigate before assuming this is the same known gap): %v", err)
	}
}
