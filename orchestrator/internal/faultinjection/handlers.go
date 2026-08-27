package faultinjection

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// First batch of real handlers -- the T1-compatible, single-resource-patch
// faults from content/faults/ (35 authored; this batch plus
// handlers_batch2.go bring wired coverage to 10). Each mirrors the
// content YAML's params_schema exactly; see content/faults/<id>.yaml
// for the human-facing description this handler makes real.
func init() {
	register("f.k8s.memory-limit-too-low", applyMemoryLimitTooLow)
	register("f.k8s.wrong-service-selector", applyWrongServiceSelector)
	register("f.k8s.readiness-probe-too-aggressive", applyReadinessProbeTooAggressive)
	register("f.k8s.configmap-key-renamed", applyConfigMapKeyRenamed)
	register("f.k8s.taint-blocks-scheduling", applyTaintBlocksScheduling)
}

// applyMemoryLimitTooLow: content/faults/f.k8s.memory-limit-too-low.yaml
// params: service (Deployment name), limit (e.g. "96Mi").
func applyMemoryLimitTooLow(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	service := params["service"]
	limit := params["limit"]
	if service == "" || limit == "" {
		return Result{}, fmt.Errorf("f.k8s.memory-limit-too-low requires params: service, limit")
	}

	limitQty, err := parseQuantity(limit)
	if err != nil {
		return Result{}, fmt.Errorf("invalid limit quantity %q: %w", limit, err)
	}

	deployments := clientset.AppsV1().Deployments(namespace)
	dep, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*appsv1.Deployment, error) {
		return deployments.Get(ctx, service, metav1.GetOptions{})
	}, "Deployment", "deployment", service)
	if err != nil {
		return notFoundOrErrResult, err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return Result{}, fmt.Errorf("deployment %s has no containers to patch", service)
	}
	containerName := dep.Spec.Template.Spec.Containers[0].Name

	patchBytes, patchType, err := patchFirstContainer(containerName, func(c *containerPatch) {
		c.Resources = &containerPatchResources{
			Limits: map[corev1.ResourceName]resource.Quantity{corev1.ResourceMemory: limitQty},
		}
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := deployments.Patch(ctx, service, patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patching deployment %s memory limit: %w", service, err)
	}

	// symptom_verified is a best-effort immediate check, not a wait --
	// OOMKilled takes real time under real load to manifest (doc's
	// mean_time_to_diagnose_minutes: 6 reflects that). Applied=true is
	// the honest claim here; SymptomVerified=false until a later health
	// re-check (not built in this pass -- see package doc) confirms it.
	return Result{Applied: true, SymptomVerified: false}, nil
}

// applyWrongServiceSelector: content/faults/f.k8s.wrong-service-selector.yaml
// params: service, wrong_selector_value.
func applyWrongServiceSelector(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	service := params["service"]
	wrongValue := params["wrong_selector_value"]
	if service == "" || wrongValue == "" {
		return Result{}, fmt.Errorf("f.k8s.wrong-service-selector requires params: service, wrong_selector_value")
	}

	services := clientset.CoreV1().Services(namespace)
	svc, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.Service, error) {
		return services.Get(ctx, service, metav1.GetOptions{})
	}, "Service", "service", service)
	if err != nil {
		return notFoundOrErrResult, err
	}
	if len(svc.Spec.Selector) == 0 {
		return Result{}, fmt.Errorf("service %s has no selector to break", service)
	}

	// Break every selector key's value -- guarantees zero matching pods
	// regardless of which label the service actually keyed on, without
	// the handler needing to know the app's specific label schema.
	broken := make(map[string]string, len(svc.Spec.Selector))
	for k := range svc.Spec.Selector {
		broken[k] = wrongValue
	}
	svc.Spec.Selector = broken

	if _, err := services.Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating service %s selector: %w", service, err)
	}

	// This fault's symptom (empty Endpoints) is verifiable, but NOT
	// instantaneous -- confirmed by a real live test where a single
	// immediate check read stale endpoints because the endpoint
	// controller hadn't reconciled yet (the earlier version of this
	// comment claimed "fast and deterministic," which the live test
	// disproved). Poll briefly instead of checking once.
	verified := pollNoEndpoints(ctx, clientset, namespace, service)
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// pollNoEndpoints retries the empty-endpoints check for a few seconds --
// long enough for real endpoint-controller reconciliation, short enough
// not to block the RPC caller noticeably. Returns false (not an error)
// on timeout: SymptomVerified=false is an honest "couldn't confirm
// within budget," not a claim the fault failed to apply.
func pollNoEndpoints(ctx context.Context, clientset kubernetes.Interface, namespace, service string) bool {
	const maxAttempts = 10
	const interval = 500 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		if noEndpoints(ctx, clientset, namespace, service) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
	return false
}

func noEndpoints(ctx context.Context, clientset kubernetes.Interface, namespace, service string) bool {
	ep, err := clientset.CoreV1().Endpoints(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, subset := range ep.Subsets {
		if len(subset.Addresses) > 0 {
			return false // still has live endpoints -- selector break hasn't propagated (or didn't take) yet
		}
	}
	return true
}

// applyReadinessProbeTooAggressive: content/faults/f.k8s.readiness-probe-too-aggressive.yaml
// params: service, timeout_seconds.
func applyReadinessProbeTooAggressive(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	service := params["service"]
	timeoutSecondsStr := params["timeout_seconds"]
	if service == "" || timeoutSecondsStr == "" {
		return Result{}, fmt.Errorf("f.k8s.readiness-probe-too-aggressive requires params: service, timeout_seconds")
	}
	// timeout_seconds must be a real integer regardless of how the patch
	// is built -- readinessProbe.timeoutSeconds is a K8s int32 field, so
	// a non-numeric value would fail at the API server, not silently
	// coerce. (PLAN.md Phase 3's U11 note: this used to also be the ONE
	// field in this package that couldn't use the %q-everywhere
	// injection-safety convention, since it needed to be embedded as a
	// raw JSON number in a hand-built fmt.Sprintf string -- patchFirstContainer's
	// real struct marshaling below has since made that whole class of
	// concern moot for every field in this package, not just this one.)
	timeoutSeconds, err := strconv.Atoi(timeoutSecondsStr)
	if err != nil || timeoutSeconds < 0 {
		return Result{}, fmt.Errorf("invalid timeout_seconds %q: must be a non-negative integer", timeoutSecondsStr)
	}

	deployments := clientset.AppsV1().Deployments(namespace)
	dep, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*appsv1.Deployment, error) {
		return deployments.Get(ctx, service, metav1.GetOptions{})
	}, "Deployment", "deployment", service)
	if err != nil {
		return notFoundOrErrResult, err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return Result{}, fmt.Errorf("deployment %s has no containers to patch", service)
	}
	containerName := dep.Spec.Template.Spec.Containers[0].Name

	patchBytes, patchType, err := patchFirstContainer(containerName, func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{
			TimeoutSeconds:      int32Ptr(int32(timeoutSeconds)),
			InitialDelaySeconds: int32Ptr(0),
		}
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := deployments.Patch(ctx, service, patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patching deployment %s readiness probe: %w", service, err)
	}

	return Result{Applied: true, SymptomVerified: false}, nil
}

// applyConfigMapKeyRenamed: content/faults/f.k8s.configmap-key-renamed.yaml
// params: configmap, old_key, new_key.
func applyConfigMapKeyRenamed(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	name := params["configmap"]
	oldKey := params["old_key"]
	newKey := params["new_key"]
	if name == "" || oldKey == "" || newKey == "" {
		return Result{}, fmt.Errorf("f.k8s.configmap-key-renamed requires params: configmap, old_key, new_key")
	}

	configMaps := clientset.CoreV1().ConfigMaps(namespace)
	cm, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ConfigMap, error) {
		return configMaps.Get(ctx, name, metav1.GetOptions{})
	}, "ConfigMap", "configmap", name)
	if err != nil {
		return notFoundOrErrResult, err
	}
	value, ok := cm.Data[oldKey]
	if !ok {
		return Result{}, fmt.Errorf("configmap %s has no key %q to rename", name, oldKey)
	}

	delete(cm.Data, oldKey)
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[newKey] = value

	if _, err := configMaps.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating configmap %s: %w", name, err)
	}

	// Verifiable immediately: the old key is either gone or it isn't --
	// no propagation delay like the probe/memory faults above.
	verifyCm, err := configMaps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	_, stillHasOldKey := verifyCm.Data[oldKey]
	return Result{Applied: true, SymptomVerified: !stillHasOldKey}, nil
}

// applyTaintBlocksScheduling: content/faults/f.k8s.taint-blocks-scheduling.yaml
// params: node, taint_key, taint_effect.
func applyTaintBlocksScheduling(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	nodeName := params["node"]
	taintKey := params["taint_key"]
	taintEffect := params["taint_effect"]
	if nodeName == "" || taintKey == "" || taintEffect == "" {
		return Result{}, fmt.Errorf("f.k8s.taint-blocks-scheduling requires params: node, taint_key, taint_effect")
	}

	effect := corev1.TaintEffect(taintEffect)
	switch effect {
	case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return Result{}, fmt.Errorf("invalid taint_effect %q: must be NoSchedule, PreferNoSchedule, or NoExecute", taintEffect)
	}

	nodes := clientset.CoreV1().Nodes()
	node, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.Node, error) {
		return nodes.Get(ctx, nodeName, metav1.GetOptions{})
	}, "Node", "node", nodeName)
	if err != nil {
		return notFoundOrErrResult, err
	}

	for _, t := range node.Spec.Taints {
		if t.Key == taintKey && t.Effect == effect {
			// Idempotent per doc §3.4's "parameterised, idempotent,
			// reversible" fault contract -- re-applying an already-applied
			// taint is a successful no-op, not an error.
			return Result{Applied: true, SymptomVerified: true}, nil
		}
	}
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: taintKey, Effect: effect})

	if _, err := nodes.Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating node %s taints: %w", nodeName, err)
	}

	return Result{Applied: true, SymptomVerified: true}, nil
}
