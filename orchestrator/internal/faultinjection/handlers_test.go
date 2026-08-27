package faultinjection

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// This file covers handlers.go's original 5 handlers, which had zero
// unit test coverage before this pass (only handlers_batch2.go and
// handlers_batch3.go were tested when first written; handlers.go was
// wired earlier and never followed up). Auditing this file's input
// handling for the Phase 2 security-validation pass surfaced a real bug
// (applyReadinessProbeTooAggressive's timeout_seconds was interpolated
// as a raw, unvalidated JSON number via %s) that tests would have
// caught immediately -- these tests close that coverage gap for all 5
// handlers, not just the one with the bug.

func TestApplyMemoryLimitTooLow(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "checkout:v1"}}},
			},
		},
	})

	result, err := applyMemoryLimitTooLow(context.Background(), clientset, testNamespace, map[string]string{
		"service": "checkout",
		"limit":   "96Mi",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("expected Applied=true, got %+v", result)
	}

	dep, _ := clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), "checkout", metav1.GetOptions{})
	got := dep.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if got.String() != "96Mi" {
		t.Errorf("expected memory limit 96Mi, got %s", got.String())
	}
}

func TestApplyMemoryLimitTooLow_InvalidQuantity(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if _, err := applyMemoryLimitTooLow(context.Background(), clientset, testNamespace, map[string]string{
		"service": "checkout",
		"limit":   "not-a-quantity",
	}); err == nil {
		t.Fatal("expected error for invalid limit quantity")
	}
}

func TestApplyMemoryLimitTooLow_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	result, err := applyMemoryLimitTooLow(context.Background(), clientset, testNamespace, map[string]string{
		"service": "missing",
		"limit":   "96Mi",
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if result.Applied {
		t.Fatalf("expected Applied=false, got %+v", result)
	}
}

func TestApplyWrongServiceSelector(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "checkout"}},
	})

	result, err := applyWrongServiceSelector(context.Background(), clientset, testNamespace, map[string]string{
		"service":              "checkout",
		"wrong_selector_value": "does-not-exist",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("expected Applied=true, got %+v", result)
	}

	svc, _ := clientset.CoreV1().Services(testNamespace).Get(context.Background(), "checkout", metav1.GetOptions{})
	if svc.Spec.Selector["app"] != "does-not-exist" {
		t.Errorf("expected selector app=does-not-exist, got %v", svc.Spec.Selector)
	}
}

func TestApplyWrongServiceSelector_NoSelectorToBreak(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
	})
	if _, err := applyWrongServiceSelector(context.Background(), clientset, testNamespace, map[string]string{
		"service":              "checkout",
		"wrong_selector_value": "x",
	}); err == nil {
		t.Fatal("expected error for a service with no selector")
	}
}

func TestApplyReadinessProbeTooAggressive(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "checkout:v1"}}},
			},
		},
	})

	result, err := applyReadinessProbeTooAggressive(context.Background(), clientset, testNamespace, map[string]string{
		"service":         "checkout",
		"timeout_seconds": "1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("expected Applied=true, got %+v", result)
	}

	dep, _ := clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), "checkout", metav1.GetOptions{})
	probe := dep.Spec.Template.Spec.Containers[0].ReadinessProbe
	if probe == nil || probe.TimeoutSeconds != 1 {
		t.Fatalf("expected readinessProbe.timeoutSeconds=1, got %+v", probe)
	}
}

// TestApplyReadinessProbeTooAggressive_RejectsNonIntegerTimeout is a
// security regression test: this handler used to interpolate
// timeout_seconds directly as a raw JSON number (%s, unvalidated) into
// the merge patch. A malformed value could corrupt the patch's JSON
// structure or inject arbitrary fields. Now validated via strconv.Atoi
// before use -- this test asserts malformed input is rejected with an
// error, never reaches the patch-construction step at all.
func TestApplyReadinessProbeTooAggressive_RejectsNonIntegerTimeout(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			},
		},
	})

	cases := []string{
		"not-a-number",
		`1, "maliciousField": "injected"`, // JSON-structure-corruption attempt
		"-5",
		"1.5",
	}
	for _, tc := range cases {
		_, err := applyReadinessProbeTooAggressive(context.Background(), clientset, testNamespace, map[string]string{
			"service":         "checkout",
			"timeout_seconds": tc,
		})
		if err == nil {
			t.Errorf("expected error for malformed timeout_seconds=%q, got nil", tc)
		}
	}

	// Confirm the deployment was never patched by any of the rejected attempts.
	dep, _ := clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), "checkout", metav1.GetOptions{})
	if dep.Spec.Template.Spec.Containers[0].ReadinessProbe != nil {
		t.Error("expected no readinessProbe to be set after all inputs were rejected")
	}
}

func TestApplyReadinessProbeTooAggressive_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	result, err := applyReadinessProbeTooAggressive(context.Background(), clientset, testNamespace, map[string]string{
		"service":         "missing",
		"timeout_seconds": "1",
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if result.Applied {
		t.Fatalf("expected Applied=false, got %+v", result)
	}
}

func TestApplyConfigMapKeyRenamed(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: testNamespace},
		Data:       map[string]string{"old_key": "value"},
	})

	result, err := applyConfigMapKeyRenamed(context.Background(), clientset, testNamespace, map[string]string{
		"configmap": "app-config",
		"old_key":   "old_key",
		"new_key":   "new_key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	cm, _ := clientset.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "app-config", metav1.GetOptions{})
	if _, stillHasOld := cm.Data["old_key"]; stillHasOld {
		t.Error("expected old_key to be removed")
	}
	if cm.Data["new_key"] != "value" {
		t.Errorf("expected new_key=value, got %v", cm.Data)
	}
}

func TestApplyConfigMapKeyRenamed_KeyDoesNotExist(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: testNamespace},
		Data:       map[string]string{},
	})
	if _, err := applyConfigMapKeyRenamed(context.Background(), clientset, testNamespace, map[string]string{
		"configmap": "app-config",
		"old_key":   "missing_key",
		"new_key":   "new_key",
	}); err == nil {
		t.Fatal("expected error when old_key doesn't exist")
	}
}

func TestApplyTaintBlocksScheduling(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
	})

	result, err := applyTaintBlocksScheduling(context.Background(), clientset, testNamespace, map[string]string{
		"node":         "node-1",
		"taint_key":    "workload",
		"taint_effect": "NoSchedule",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	node, _ := clientset.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	found := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "workload" && taint.Effect == corev1.TaintEffectNoSchedule {
			found = true
		}
	}
	if !found {
		t.Errorf("expected taint workload:NoSchedule to be applied, got %+v", node.Spec.Taints)
	}
}

func TestApplyTaintBlocksScheduling_Idempotent(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{{Key: "workload", Effect: corev1.TaintEffectNoSchedule}},
		},
	})

	result, err := applyTaintBlocksScheduling(context.Background(), clientset, testNamespace, map[string]string{
		"node":         "node-1",
		"taint_key":    "workload",
		"taint_effect": "NoSchedule",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected idempotent re-apply to report Applied=true SymptomVerified=true, got %+v", result)
	}

	node, _ := clientset.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if len(node.Spec.Taints) != 1 {
		t.Fatalf("expected still exactly 1 taint after idempotent re-apply, got %d", len(node.Spec.Taints))
	}
}

// TestApplyTaintBlocksScheduling_RejectsInvalidEffect is a security-
// adjacent input-validation test: taint_effect is validated against an
// explicit allowlist (NoSchedule/PreferNoSchedule/NoExecute) before
// being cast to corev1.TaintEffect and sent to the K8s API -- this
// confirms arbitrary strings are rejected rather than silently accepted
// as a taint effect the K8s API might interpret unpredictably.
func TestApplyTaintBlocksScheduling_RejectsInvalidEffect(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	if _, err := applyTaintBlocksScheduling(context.Background(), clientset, testNamespace, map[string]string{
		"node":         "node-1",
		"taint_key":    "workload",
		"taint_effect": "NotARealEffect",
	}); err == nil {
		t.Fatal("expected error for invalid taint_effect")
	}
}

func TestApplyTaintBlocksScheduling_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	result, err := applyTaintBlocksScheduling(context.Background(), clientset, testNamespace, map[string]string{
		"node":         "missing-node",
		"taint_key":    "workload",
		"taint_effect": "NoSchedule",
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if result.Applied {
		t.Fatalf("expected Applied=false, got %+v", result)
	}
}

func TestFaultRegistry_OriginalBatchFaultsRegistered(t *testing.T) {
	for _, id := range []string{
		"f.k8s.memory-limit-too-low",
		"f.k8s.wrong-service-selector",
		"f.k8s.readiness-probe-too-aggressive",
		"f.k8s.configmap-key-renamed",
		"f.k8s.taint-blocks-scheduling",
	} {
		if _, ok := registry[id]; !ok {
			t.Errorf("expected %s to be registered", id)
		}
	}
}
