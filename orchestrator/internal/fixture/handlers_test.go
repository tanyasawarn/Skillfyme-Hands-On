package fixture

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// testApplyPodCrashloop calls the handler's clientset-only core with the
// crash-loop wait disabled (a fake clientset never advances
// restartCount, so a non-zero wait would just burn the whole deadline).
func testApplyPodCrashloop(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	return applyPodCrashloopWithClientset(ctx, clientset, namespace, 0)
}

// TestApplyPodCrashloop_SatisfiesPodSecurityRestricted is a regression
// test for a real bug caught by a live Provision() call against this
// project's actual k3s cluster during this session: applyPodCrashloop's
// first version created a Pod with no SecurityContext at all, which the
// K8s API server's PodSecurity "restricted" admission controller
// (enforced on every T1 namespace) rejected outright -- a fake
// clientset's unit tests never caught this, since fake clientsets don't
// enforce admission control. This test asserts every field PSS
// "restricted" actually requires, so a future edit to this handler that
// drops one of them fails a unit test instead of only failing against a
// real cluster.
func TestApplyPodCrashloop_SatisfiesPodSecurityRestricted(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := testApplyPodCrashloop(context.Background(), clientset, testNamespace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pod, err := clientset.CoreV1().Pods(testNamespace).Get(context.Background(), "broken-app", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected pod to exist: %v", err)
	}

	if pod.Spec.SecurityContext == nil {
		t.Fatal("expected pod-level SecurityContext to be set")
	}
	if pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("PSS restricted requires runAsNonRoot=true")
	}
	if pod.Spec.SecurityContext.SeccompProfile == nil || pod.Spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("PSS restricted requires seccompProfile.type=RuntimeDefault")
	}

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	container := pod.Spec.Containers[0]
	if container.SecurityContext == nil {
		t.Fatal("expected container-level SecurityContext to be set")
	}
	if container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Error("PSS restricted requires allowPrivilegeEscalation=false")
	}
	if container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) == 0 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Error("PSS restricted requires capabilities.drop=[ALL]")
	}
}

func TestApplyPodCrashloop_CreatesAPodThatWillCrash(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := testApplyPodCrashloop(context.Background(), clientset, testNamespace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pod, err := clientset.CoreV1().Pods(testNamespace).Get(context.Background(), "broken-app", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected pod to exist: %v", err)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Error("expected RestartPolicyAlways so the pod actually enters a crash loop (repeated restarts), not a one-shot failure")
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Command == nil {
		t.Fatal("expected a command that exits non-zero to simulate the crash")
	}
}

func TestFixtureRegistry_AllFixturesRegistered(t *testing.T) {
	for _, id := range []string{"fx.k3s-ready.v1", "fx.pod-crashloop.v1", "fx.node-app-repo.v1", "fx.tekton-pipeline.v1", "fx.prometheus-minimal.v1", "fx.jaeger-minimal.v1", "fx.elk-minimal.v1", "fx.jenkins-basic.v1", "fx.helm-release.v1", "fx.ansible-target.v1", "fx.terraform-workspace.v1", "fx.gitea-repo.v1", "fx.dind-workspace.v1", "fx.istio-minimal.v1", "fx.checkout-deployment.v1", "fx.billing-platform.v1", "fx.rollout-workloads.v1", "fx.argocd-minimal.v1"} {
		if _, ok := registry[id]; !ok {
			t.Errorf("expected %s to be registered", id)
		}
	}
}
