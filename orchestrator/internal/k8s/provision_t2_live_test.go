package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TestProvisionT2_Lifecycle is real-infra-gated (same skip convention as
// internal/orchestrator/ownership_rpc_test.go). As of the ₹100/user cost
// decision, T2 runs under the **Sysbox** runtime ("sysbox-runc"
// RuntimeClass) on the SAME shared node pool as T1 -- no dedicated Kata
// metal pool, no microVM. The test has TWO valid outcomes, decided by
// whether the target cluster actually has the sysbox-runc RuntimeClass
// registered (i.e. infra/practice-cluster/sysbox/ has been applied):
//
//   - NO sysbox-runc RuntimeClass -> assert the honest failure a real
//     Provision(T2) hits: admission rejects the pod because the
//     RuntimeClass isn't registered. This is the state of a local dev
//     cluster or a fresh EKS cluster before the Sysbox DaemonSet lands.
//     (If ORCHESTRATOR_T2_RUNTIME_CLASS were set to "" the pod would
//     instead schedule as a plain container -- but the orchestrator's
//     default is "sysbox-runc", which is what this test exercises.)
//
//   - sysbox-runc RuntimeClass present -> assert the FULL positive
//     lifecycle: Provision(T2) succeeds, the workspace pod schedules
//     with runtimeClassName=sysbox-runc, runs as root-in-userns and is
//     NOT privileged, the namespace carries PSS `privileged`, the
//     ResourceQuota matches DefaultT2Resources, and Destroy removes the
//     namespace with nothing left behind.
//
// The end-to-end capability checks (DinD / multi-node k3s / systemd /
// eBPF) that Phase 2 requirement A also demands live in
// scripts/t2-lifecycle-check.sh, which drives the real orchestrator RPCs
// + kubectl exec against a running Session Broker. This test covers the
// provisioning/teardown half against the real K8s API.
func TestProvisionT2_Lifecycle(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	const runtimeClass = T2RuntimeClassDefault // "sysbox-runc"
	sysboxCapable := clusterHasRuntimeClass(ctx, clientset, runtimeClass)

	const envID = "t2-live-test"
	ns := namespaceName(envID)
	provisioner := NewProvisioner(clientset, restConfig, ProvisionerConfig{T2RuntimeClass: runtimeClass})
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})

	// If a previous run's namespace is still terminating, wait it out --
	// otherwise Provision fails on the quota create ("namespace is being
	// terminated"), which looks like a different bug than it is.
	waitNamespaceGone(ctx, clientset, ns, 90*time.Second)

	provErr := provisioner.Provision(ctx, ProvisionRequest{
		AttemptID: "t2-live-test-attempt",
		EnvID:     envID,
		Tier:      TierT2IsolatedMicroVM,
		Image:     "registry:5000/practiceengine/linux-tools:v1",
	})

	if !sysboxCapable {
		// --- negative outcome: Sysbox not installed on this cluster ---
		if provErr == nil {
			t.Fatal("expected Provision(T2) to fail (RuntimeClass sysbox-runc not registered) -- if this now succeeds, Sysbox may have been installed but clusterHasRuntimeClass didn't see it; investigate both.")
		}
		if !strings.Contains(provErr.Error(), `RuntimeClass "`+runtimeClass+`" not found`) {
			t.Fatalf("expected the specific real failure 'RuntimeClass %q not found', got a different error (investigate before assuming this is the same known gap): %v", runtimeClass, provErr)
		}
		t.Logf("Sysbox not installed; Provision(T2) failed at admission as expected: %v", provErr)
		return
	}

	// --- positive outcome: Sysbox present, assert full lifecycle ---
	if provErr != nil {
		t.Fatalf("Provision(T2) failed on a Sysbox-capable cluster: %v", provErr)
	}

	pod := waitPodScheduled(ctx, t, clientset, ns, WorkspacePodName, 120*time.Second)
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != runtimeClass {
		t.Fatalf("workspace pod runtimeClassName = %v, want %q", pod.Spec.RuntimeClassName, runtimeClass)
	}
	// Sysbox: root-in-userns, NOT privileged (that is the whole point).
	if sc := pod.Spec.Containers[0].SecurityContext; sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Fatalf("workspace container SecurityContext.RunAsUser = %+v, want 0 (root-in-userns)", sc)
	}
	if sc := pod.Spec.Containers[0].SecurityContext; sc != nil && sc.Privileged != nil && *sc.Privileged {
		t.Fatal("workspace container is privileged -- Sysbox T2 must be unprivileged unless the blueprint declares an eBPF capability")
	}
	// No tier2 metal pinning.
	if _, ok := pod.Spec.NodeSelector["practiceengine.dev/tier2"]; ok {
		t.Fatalf("workspace pod has a practiceengine.dev/tier2 nodeSelector -- Sysbox T2 shares the T1 pool, got %v", pod.Spec.NodeSelector)
	}

	// Namespace must be PSS privileged (createNamespace's T2 branch --
	// required so admission permits RunAsUser=0).
	nsObj, err := clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting namespace %q: %v", ns, err)
	}
	if got := nsObj.Labels["pod-security.kubernetes.io/enforce"]; got != "privileged" {
		t.Fatalf("namespace PSS enforce = %q, want \"privileged\"", got)
	}

	// ResourceQuota must match DefaultT2Resources.
	quota, err := clientset.CoreV1().ResourceQuotas(ns).Get(ctx, "env-quota", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting ResourceQuota env-quota: %v", err)
	}
	if cpu := quota.Spec.Hard[corev1.ResourceRequestsCPU]; cpu.String() != DefaultT2Resources.CPU {
		t.Errorf("T2 ResourceQuota requests.cpu = %s, want %s (DefaultT2Resources.CPU)", cpu.String(), DefaultT2Resources.CPU)
	}
	if mem := quota.Spec.Hard[corev1.ResourceRequestsMemory]; mem.String() != DefaultT2Resources.Memory {
		t.Errorf("T2 ResourceQuota requests.memory = %s, want %s (DefaultT2Resources.Memory)", mem.String(), DefaultT2Resources.Memory)
	}

	// Destroy: namespace gone, nothing left behind.
	if err := provisioner.Destroy(ctx, envID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	waitNamespaceGone(ctx, clientset, ns, 120*time.Second)
	if _, err := clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
		t.Fatal("namespace still present after Destroy + wait")
	}
	pvs, err := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, pv := range pvs.Items {
			if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == ns {
				t.Errorf("PersistentVolume %q still bound to destroyed namespace %q", pv.Name, ns)
			}
		}
	}
	t.Log("T2 lifecycle verified: Provision -> Sysbox pod (root-in-userns, unprivileged) -> PSS privileged ns -> quota matches DefaultT2Resources -> Destroy -> namespace gone, no PV leaked")
}

func clusterHasRuntimeClass(ctx context.Context, cs *kubernetes.Clientset, name string) bool {
	_, err := cs.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

func waitNamespaceGone(ctx context.Context, cs *kubernetes.Clientset, ns string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func waitPodScheduled(ctx context.Context, t *testing.T, cs *kubernetes.Clientset, ns, name string, timeout time.Duration) *corev1.Pod {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *corev1.Pod
	for time.Now().Before(deadline) {
		pod, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			last = pod
			if pod.Spec.NodeName != "" {
				return pod
			}
		}
		time.Sleep(2 * time.Second)
	}
	if last != nil {
		t.Fatalf("pod %s/%s never scheduled within %s (phase=%s)", ns, name, timeout, last.Status.Phase)
	}
	t.Fatalf("pod %s/%s never appeared within %s", ns, name, timeout)
	return nil
}
