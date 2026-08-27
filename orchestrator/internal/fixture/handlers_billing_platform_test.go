package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// TestBillingPlatformFixtureAndFaults_LiveIntegration verifies
// fx.billing-platform.v1 and all 4 faults
// sim.k8s.platform-migration-incident.yaml applies against it, matching
// that activity's own validators exactly. Reproduces
// faultinjection.applyConfigMapKeyRenamed / applyPVCStorageClassMissing
// / applyNetworkPolicyOverblocksTraffic / applyResourceQuotaBlocksDeploy's
// own real mutations directly (import cycle -- same convention as every
// other live fault test in this package).
func TestBillingPlatformFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-billing-test-" + envID[:8]

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

	if err := applyBillingPlatform(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyBillingPlatform failed: %v", err)
	}

	t.Run("healthy baseline: billing-service has a running pod", func(t *testing.T) {
		dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, billingServiceName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting billing-service deployment: %v", err)
		}
		if dep.Status.ReadyReplicas < 1 {
			t.Fatalf("expected at least 1 ready replica, got %d", dep.Status.ReadyReplicas)
		}
	})

	t.Run("healthy baseline: billing-service can reach payment-service before the netpol fault", func(t *testing.T) {
		billingPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+billingServiceName)
		if err != nil {
			t.Fatalf("finding billing-service pod: %v", err)
		}
		result, err := k8s.ExecInPod(ctx, provisioner, ns, billingPod, "app",
			"wget -q -T 5 -O- http://"+paymentServiceName+"/ 2>&1; echo EXIT:$?", 15*time.Second)
		if err != nil {
			t.Fatalf("calling payment-service: %v", err)
		}
		if !strings.Contains(result.Stdout, "EXIT:0") {
			t.Fatalf("expected billing-service to reach payment-service before the fault, got: %s", result.Stdout)
		}
	})

	t.Run("f.k8s.configmap-key-renamed: renaming DB_HOST breaks new pods with a real CreateContainerConfigError", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, billingConfigMap, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting configmap: %v", err)
		}
		value := cm.Data["DB_HOST"]
		delete(cm.Data, "DB_HOST")
		cm.Data["DATABASE_HOST"] = value
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating configmap: %v", err)
		}

		// Force a fresh pod to observe the real CreateContainerConfigError
		// (the already-running pod keeps running on its already-resolved
		// env until it restarts).
		if err := clientset.CoreV1().Pods(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: "app=" + billingServiceName}); err != nil {
			t.Fatalf("deleting billing-service pod to force recreation: %v", err)
		}

		deadline := time.Now().Add(30 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + billingServiceName})
			if err == nil {
				for _, p := range pods.Items {
					for _, cs := range p.Status.ContainerStatuses {
						if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CreateContainerConfigError" {
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
			t.Fatal("REGRESSION: expected a real CreateContainerConfigError after renaming the ConfigMap key, but none appeared within 30s")
		}
	})

	t.Run("f.k8s.pvc-storageclass-missing: swapping storageClassName breaks binding", func(t *testing.T) {
		if err := clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, billingPVCName, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("deleting pvc: %v", err)
		}
		// A PVC has its own finalizer (kubernetes.io/pvc-protection) --
		// Delete only marks it for deletion, it doesn't block until the
		// object is actually gone, confirmed live as a real race in this
		// test's first version ("object is being deleted" on the
		// immediate recreate attempt below).
		deleteDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deleteDeadline) {
			if _, err := clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, billingPVCName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				break
			}
			time.Sleep(1 * time.Second)
		}
		wrongClass := "nonexistent-storage-class"
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: billingPVCName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &wrongClass,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		if _, err := clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
			t.Fatalf("recreating pvc with wrong class: %v", err)
		}

		time.Sleep(3 * time.Second)
		verify, err := clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, billingPVCName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("re-reading pvc: %v", err)
		}
		if verify.Status.Phase == corev1.ClaimBound {
			t.Fatal("REGRESSION: expected the PVC to stay Pending with a nonexistent storage class, but it bound")
		}
	})

	t.Run("f.k8s.networkpolicy-overblocks-traffic: an allow-list forgetting payment-service genuinely blocks it", func(t *testing.T) {
		peers := make([]networkingv1.NetworkPolicyPeer, 0, 2)
		for _, dep := range []string{authServiceName, configServiceName} {
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": dep}},
			})
		}
		policy := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "fault-allowlist-forgot-payment", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress:      []networkingv1.NetworkPolicyEgressRule{{To: peers}},
			},
		}
		if _, err := clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating allow-list policy: %v", err)
		}

		// REQUIRED, not optional -- see
		// faultinjection.applyNetworkPolicyOverblocksTraffic's own doc
		// comment: without excluding payment-service from
		// ensureIntraNamespacePodTrafficAllowed's blanket allow (already
		// applied to this namespace by applyRealT1NetworkBaseline
		// above), that blanket rule makes this fault's own narrower
		// policy a no-op, confirmed live as a real, serious bug this
		// session.
		paymentPods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + paymentServiceName})
		if err != nil {
			t.Fatalf("listing payment-service pods: %v", err)
		}
		for _, p := range paymentPods.Items {
			if p.Labels == nil {
				p.Labels = map[string]string{}
			}
			p.Labels["practiceengine.dev/network-isolated"] = "true"
			if _, err := clientset.CoreV1().Pods(ns).Update(ctx, &p, metav1.UpdateOptions{}); err != nil {
				t.Fatalf("isolating payment-service pod: %v", err)
			}
		}

		time.Sleep(5 * time.Second)

		billingPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+billingServiceName)
		if err != nil {
			t.Fatalf("finding billing-service pod: %v", err)
		}
		result, err := k8s.ExecInPod(ctx, provisioner, ns, billingPod, "app",
			"wget -q -T 5 -O- http://"+paymentServiceName+"/ 2>&1; echo EXIT:$?", 15*time.Second)
		if err != nil {
			t.Fatalf("calling payment-service after fault: %v", err)
		}
		if strings.Contains(result.Stdout, "EXIT:0") {
			t.Fatal("REGRESSION: expected billing-service to be blocked from payment-service after the allow-list gap, but it succeeded")
		}
		// Confirm known dependencies still work.
		result2, err := k8s.ExecInPod(ctx, provisioner, ns, billingPod, "app",
			"wget -q -T 5 -O- http://"+authServiceName+"/ 2>&1; echo EXIT:$?", 15*time.Second)
		if err != nil || !strings.Contains(result2.Stdout, "EXIT:0") {
			t.Fatalf("expected auth-service (on the allow-list) to still be reachable, got: %s", result2.Stdout)
		}
	})

	t.Run("f.k8s.resourcequota-blocks-deploy: a tightened quota genuinely blocks scale-up", func(t *testing.T) {
		rq, err := clientset.CoreV1().ResourceQuotas(ns).Get(ctx, billingQuotaName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting resourcequota: %v", err)
		}
		if rq.Spec.Hard == nil {
			rq.Spec.Hard = corev1.ResourceList{}
		}
		rq.Spec.Hard[corev1.ResourceLimitsCPU] = resource.MustParse("500m")
		rq.Spec.Hard[corev1.ResourceRequestsCPU] = resource.MustParse("500m")
		if _, err := clientset.CoreV1().ResourceQuotas(ns).Update(ctx, rq, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("tightening quota: %v", err)
		}

		dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, billingServiceName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting billing-service deployment: %v", err)
		}
		replicas := int32(3)
		dep.Spec.Replicas = &replicas
		if _, err := clientset.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("scaling billing-service: %v", err)
		}

		time.Sleep(5 * time.Second)
		verify, err := clientset.AppsV1().Deployments(ns).Get(ctx, billingServiceName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("re-reading deployment: %v", err)
		}
		if verify.Status.ReadyReplicas >= 3 {
			t.Fatal("REGRESSION: expected the tightened quota to block scale-up to 3 replicas, but it succeeded")
		}
	})
}
