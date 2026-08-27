package fixture

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestRolloutWorkloadsFixtureAndFaults_LiveIntegration verifies
// fx.rollout-workloads.v1 and both faults
// sim.k8s.rollout-stuck-incident.yaml applies against it. Reproduces
// faultinjection.applyRolloutStuckBadImageTag /
// applyStatefulSetOrdinalStuck's own real mutations directly (import
// cycle -- same convention as every other live fault test in this
// package).
func TestRolloutWorkloadsFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-rollout-test-" + envID[:8]

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

	if err := applyRolloutWorkloads(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyRolloutWorkloads failed: %v", err)
	}

	t.Run("healthy baseline: web-frontend has 2 ready replicas", func(t *testing.T) {
		dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, rolloutDeploymentName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting web-frontend deployment: %v", err)
		}
		if dep.Status.ReadyReplicas < 2 {
			t.Fatalf("expected 2 ready replicas, got %d", dep.Status.ReadyReplicas)
		}
	})

	t.Run("healthy baseline: cache-cluster has 3 ready replicas", func(t *testing.T) {
		sts, err := clientset.AppsV1().StatefulSets(ns).Get(ctx, rolloutStatefulSetName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting cache-cluster statefulset: %v", err)
		}
		if sts.Status.ReadyReplicas < 3 {
			t.Fatalf("expected 3 ready replicas, got %d", sts.Status.ReadyReplicas)
		}
	})

	t.Run("f.k8s.rollout-stuck-bad-image-tag: a bad tag genuinely stalls the rollout", func(t *testing.T) {
		dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, rolloutDeploymentName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting deployment: %v", err)
		}
		dep.Spec.Template.Spec.Containers[0].Image = "docker.io/library/alpine:v99-does-not-exist"
		if _, err := clientset.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("patching bad image tag: %v", err)
		}

		deadline := time.Now().Add(30 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + rolloutDeploymentName})
			if err == nil {
				for _, p := range pods.Items {
					for _, cs := range p.Status.ContainerStatuses {
						if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "ImagePullBackOff" || cs.State.Waiting.Reason == "ErrImagePull") {
							found = true
						}
					}
				}
			}
			if found {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !found {
			t.Fatal("REGRESSION: expected a real ImagePullBackOff/ErrImagePull after the bad tag, but none appeared within 30s")
		}
	})

	t.Run("f.k8s.statefulset-ordinal-stuck: a broken readiness probe on ordinal 1 genuinely blocks the rollout", func(t *testing.T) {
		sts, err := clientset.AppsV1().StatefulSets(ns).Get(ctx, rolloutStatefulSetName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting statefulset: %v", err)
		}
		sts.Spec.Template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{"sh", "-c", `test "$(hostname)" != "` + rolloutStatefulSetName + `-1"`}},
			},
			InitialDelaySeconds: 0,
			PeriodSeconds:       5,
		}
		if _, err := clientset.AppsV1().StatefulSets(ns).Update(ctx, sts, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("patching readiness probe: %v", err)
		}

		// A StatefulSet's default RollingUpdate strategy does NOT
		// immediately recreate already-Running pods just because the
		// template changed -- matches the real production handler's own
		// documented stance (Result.SymptomVerified: false, "verifiable
		// only after a later health re-check"). Deleting the target
		// ordinal's pod directly is what actually forces it to restart
		// under the new (broken) template, the same as a real rolling
		// update would eventually reach it.
		oldPod, err := clientset.CoreV1().Pods(ns).Get(ctx, rolloutStatefulSetName+"-1", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting cache-cluster-1 before delete: %v", err)
		}
		oldUID := oldPod.UID

		if err := clientset.CoreV1().Pods(ns).Delete(ctx, rolloutStatefulSetName+"-1", metav1.DeleteOptions{}); err != nil {
			t.Fatalf("deleting cache-cluster-1 to force recreation under the new probe: %v", err)
		}

		// Poll instead of a single fixed sleep -- confirmed live as a
		// real bug in this test's own first version: Delete is
		// asynchronous, so a Get immediately afterward can still return
		// the OLD pod object (with its stale, still-True Ready
		// condition from before termination even started), causing a
		// false-negative REGRESSION failure. Wait for a genuinely NEW
		// pod (different UID) before evaluating readiness at all.
		deadline := time.Now().Add(60 * time.Second)
		var lastPod *corev1.Pod
		sawNewPod := false
		for time.Now().Before(deadline) {
			pod, err := clientset.CoreV1().Pods(ns).Get(ctx, rolloutStatefulSetName+"-1", metav1.GetOptions{})
			if err != nil {
				time.Sleep(1 * time.Second)
				continue
			}
			if pod.UID == oldUID {
				time.Sleep(1 * time.Second)
				continue
			}
			sawNewPod = true
			lastPod = pod
			// A pod that's Initialized but genuinely still failing
			// readiness (not just "not yet checked") is the real signal
			// to stop polling early -- otherwise wait out the full
			// deadline in case it flips ready outside the probe's own
			// period.
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					t.Fatal("REGRESSION: expected cache-cluster-1 to genuinely fail readiness, but it reported Ready")
				}
			}
			time.Sleep(3 * time.Second)
		}
		if !sawNewPod || lastPod == nil {
			t.Fatal("never observed a genuinely NEW cache-cluster-1 pod after deletion")
		}
	})
}
