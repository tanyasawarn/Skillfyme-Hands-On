package faultinjection

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testNamespace = "env-test"

func TestApplyResourceQuotaBlocksDeploy(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-quota", Namespace: testNamespace},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU: resource.MustParse("4"),
			},
		},
	})

	result, err := applyResourceQuotaBlocksDeploy(context.Background(), clientset, testNamespace, map[string]string{
		"quota_name": "compute-quota",
		"hard_cpu":   "100m",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	rq, _ := clientset.CoreV1().ResourceQuotas(testNamespace).Get(context.Background(), "compute-quota", metav1.GetOptions{})
	got := rq.Spec.Hard[corev1.ResourceRequestsCPU]
	if got.Cmp(resource.MustParse("100m")) != 0 {
		t.Fatalf("expected hard cpu 100m, got %s", got.String())
	}
}

func TestApplyResourceQuotaBlocksDeploy_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	result, err := applyResourceQuotaBlocksDeploy(context.Background(), clientset, testNamespace, map[string]string{
		"quota_name": "missing-quota",
		"hard_cpu":   "100m",
	})
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	if result.Applied {
		t.Fatalf("expected Applied=false on not-found, got %+v", result)
	}
}

func TestApplyResourceQuotaBlocksDeploy_MissingParams(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if _, err := applyResourceQuotaBlocksDeploy(context.Background(), clientset, testNamespace, map[string]string{}); err == nil {
		t.Fatal("expected error for missing params")
	}
}

func TestApplyPVCStorageClassMissing(t *testing.T) {
	goodClass := "fast-ssd"
	clientset := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-pvc", Namespace: testNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &goodClass,
		},
	})

	result, err := applyPVCStorageClassMissing(context.Background(), clientset, testNamespace, map[string]string{
		"pvc":                 "data-pvc",
		"wrong_storage_class": "does-not-exist",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), "data-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pvc should still exist after recreate: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "does-not-exist" {
		t.Fatalf("expected storageClassName=does-not-exist, got %+v", pvc.Spec.StorageClassName)
	}
}

func TestApplyPVCStorageClassMissing_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	result, err := applyPVCStorageClassMissing(context.Background(), clientset, testNamespace, map[string]string{
		"pvc":                 "missing-pvc",
		"wrong_storage_class": "x",
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if result.Applied {
		t.Fatalf("expected Applied=false, got %+v", result)
	}
}

func TestApplyNetworkPolicyOverblocksTraffic(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	result, err := applyNetworkPolicyOverblocksTraffic(context.Background(), clientset, testNamespace, map[string]string{
		"missing_dependency": "billing-service",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	policies, err := clientset.NetworkingV1().NetworkPolicies(testNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing networkpolicies: %v", err)
	}
	if len(policies.Items) != 1 {
		t.Fatalf("expected exactly 1 networkpolicy created, got %d", len(policies.Items))
	}
	if policies.Items[0].Name != "fault-allowlist-forgot-billing-service" {
		t.Fatalf("unexpected policy name: %s", policies.Items[0].Name)
	}
}

func TestApplyNetworkPolicyOverblocksTraffic_Idempotent(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	params := map[string]string{"missing_dependency": "billing-service"}

	if _, err := applyNetworkPolicyOverblocksTraffic(context.Background(), clientset, testNamespace, params); err != nil {
		t.Fatalf("first apply: unexpected error: %v", err)
	}
	result, err := applyNetworkPolicyOverblocksTraffic(context.Background(), clientset, testNamespace, params)
	if err != nil {
		t.Fatalf("second apply: unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected idempotent re-apply to report Applied=true SymptomVerified=true, got %+v", result)
	}

	policies, _ := clientset.NetworkingV1().NetworkPolicies(testNamespace).List(context.Background(), metav1.ListOptions{})
	if len(policies.Items) != 1 {
		t.Fatalf("expected still exactly 1 networkpolicy after re-apply, got %d", len(policies.Items))
	}
}

func TestApplyStatefulSetOrdinalStuck(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "web:v1"}},
				},
			},
		},
	})

	result, err := applyStatefulSetOrdinalStuck(context.Background(), clientset, testNamespace, map[string]string{
		"statefulset":   "web",
		"stuck_ordinal": "1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("expected Applied=true, got %+v", result)
	}

	sts, _ := clientset.AppsV1().StatefulSets(testNamespace).Get(context.Background(), "web", metav1.GetOptions{})
	probe := sts.Spec.Template.Spec.Containers[0].ReadinessProbe
	if probe == nil || probe.Exec == nil {
		t.Fatal("expected an exec readiness probe to be patched in")
	}
}

func TestApplyStatefulSetOrdinalStuck_InvalidOrdinal(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if _, err := applyStatefulSetOrdinalStuck(context.Background(), clientset, testNamespace, map[string]string{
		"statefulset":   "web",
		"stuck_ordinal": "not-a-number",
	}); err == nil {
		t.Fatal("expected error for non-integer stuck_ordinal")
	}
}

func TestApplyRolloutStuckBadImageTag(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "registry.local:5000/checkout:v3"}},
				},
			},
		},
	})

	result, err := applyRolloutStuckBadImageTag(context.Background(), clientset, testNamespace, map[string]string{
		"deployment": "checkout",
		"bad_tag":    "v999-does-not-exist",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("expected Applied=true, got %+v", result)
	}

	dep, _ := clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), "checkout", metav1.GetOptions{})
	got := dep.Spec.Template.Spec.Containers[0].Image
	want := "registry.local:5000/checkout:v999-does-not-exist"
	if got != want {
		t.Fatalf("expected image %q, got %q", want, got)
	}
}

func TestImageRepo(t *testing.T) {
	cases := map[string]string{
		"nginx":                              "nginx",
		"nginx:1.25":                         "nginx",
		"registry.local:5000/app:v1":         "registry.local:5000/app",
		"registry.local:5000/app":            "registry.local:5000/app",
		"repo/app@sha256:deadbeef":           "repo/app",
		"gcr.io/proj/app:v1@sha256:deadbeef": "gcr.io/proj/app:v1",
	}
	for input, want := range cases {
		if got := imageRepo(input); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFaultRegistry_Batch2FaultsRegistered(t *testing.T) {
	for _, id := range []string{
		"f.k8s.resourcequota-blocks-deploy",
		"f.k8s.pvc-storageclass-missing",
		"f.k8s.networkpolicy-overblocks-traffic",
		"f.k8s.statefulset-ordinal-stuck",
		"f.k8s.rollout-stuck-bad-image-tag",
	} {
		if _, ok := registry[id]; !ok {
			t.Errorf("expected %s to be registered in faultinjection registry", id)
		}
	}
}
