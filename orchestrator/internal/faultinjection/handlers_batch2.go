package faultinjection

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Second batch of real handlers -- next 5 T1-compatible, single-resource
// faults from content/faults/ (30 authored, still unimplemented after
// the first batch in handlers.go; this batch brings wired coverage to
// 10/35). Same conventions as handlers.go: typed params, notFoundResult
// for a missing target, idempotent where the fault is a durable object
// mutation, best-effort SymptomVerified.
//
// f.k8s.hpa-metrics-unavailable is deliberately NOT in this batch --
// unlike the others, its fault target is cluster infrastructure (the
// metrics-server Deployment/APIService), not a single object the
// content author names via params, so it needs a different contract
// (which metrics-server deployment/namespace to degrade) that hasn't
// been decided yet. Left for a follow-up pass rather than guessing.
func init() {
	register("f.k8s.resourcequota-blocks-deploy", applyResourceQuotaBlocksDeploy)
	register("f.k8s.pvc-storageclass-missing", applyPVCStorageClassMissing)
	register("f.k8s.networkpolicy-overblocks-traffic", applyNetworkPolicyOverblocksTraffic)
	register("f.k8s.statefulset-ordinal-stuck", applyStatefulSetOrdinalStuck)
	register("f.k8s.rollout-stuck-bad-image-tag", applyRolloutStuckBadImageTag)
}

// applyResourceQuotaBlocksDeploy: content/faults/f.k8s.resourcequota-blocks-deploy.yaml
// params: namespace (target namespace -- distinct from the environment
// namespace param since the fault's own schema names it explicitly;
// honoured as an override when non-empty, else falls back to the
// environment namespace), quota_name, hard_cpu.
func applyResourceQuotaBlocksDeploy(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	quotaName := params["quota_name"]
	hardCPU := params["hard_cpu"]
	if quotaName == "" || hardCPU == "" {
		return Result{}, fmt.Errorf("f.k8s.resourcequota-blocks-deploy requires params: quota_name, hard_cpu")
	}
	ns := nonEmptyParam(params["namespace"], namespace)

	if _, err := parseQuantity(hardCPU); err != nil {
		return Result{}, fmt.Errorf("invalid hard_cpu quantity %q: %w", hardCPU, err)
	}

	quotas := clientset.CoreV1().ResourceQuotas(ns)
	rq, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ResourceQuota, error) {
		return quotas.Get(ctx, quotaName, metav1.GetOptions{})
	}, "ResourceQuota", "resourcequota", quotaName)
	if err != nil {
		return notFoundOrErrResult, err
	}

	if rq.Spec.Hard == nil {
		rq.Spec.Hard = corev1.ResourceList{}
	}
	rq.Spec.Hard[corev1.ResourceRequestsCPU] = resource.MustParse(hardCPU)
	rq.Spec.Hard[corev1.ResourceLimitsCPU] = resource.MustParse(hardCPU)

	if _, err := quotas.Update(ctx, rq, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating resourcequota %s: %w", quotaName, err)
	}

	// Verifiable immediately: the tightened hard limit is either stored
	// or it isn't -- admission-rejection of a future scale-up is the
	// downstream symptom (doc's own diagnostic path), not something this
	// handler can observe without the learner actually attempting one.
	verifyRq, err := quotas.Get(ctx, quotaName, metav1.GetOptions{})
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	stored := verifyRq.Spec.Hard[corev1.ResourceRequestsCPU]
	applied := stored.Cmp(resource.MustParse(hardCPU)) == 0
	return Result{Applied: true, SymptomVerified: applied}, nil
}

// applyPVCStorageClassMissing: content/faults/f.k8s.pvc-storageclass-missing.yaml
// params: pvc, wrong_storage_class.
//
// PVC.spec.storageClassName is immutable after creation (the API server
// rejects a Patch that changes it once bound/pending), so this handler
// deletes and recreates the claim with the same name/spec except the
// broken storageClassName -- the closest honest emulation of "the
// StorageClass this PVC referenced was renamed/removed out from under
// it" without needing a pre-seeded fixture that already has that state.
func applyPVCStorageClassMissing(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	pvcName := params["pvc"]
	wrongClass := params["wrong_storage_class"]
	if pvcName == "" || wrongClass == "" {
		return Result{}, fmt.Errorf("f.k8s.pvc-storageclass-missing requires params: pvc, wrong_storage_class")
	}

	pvcs := clientset.CoreV1().PersistentVolumeClaims(namespace)
	existing, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.PersistentVolumeClaim, error) {
		return pvcs.Get(ctx, pvcName, metav1.GetOptions{})
	}, "PersistentVolumeClaim", "pvc", pvcName)
	if err != nil {
		return notFoundOrErrResult, err
	}

	replacement := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        existing.Name,
			Namespace:   existing.Namespace,
			Labels:      existing.Labels,
			Annotations: existing.Annotations,
		},
		Spec: existing.Spec,
	}
	replacement.Spec.StorageClassName = &wrongClass
	replacement.Spec.VolumeName = "" // must be cleared -- can't rebind a specific PV on recreate

	if err := pvcs.Delete(ctx, pvcName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return Result{}, fmt.Errorf("deleting pvc %s to force recreate: %w", pvcName, err)
	}

	// A PVC carries its own finalizer (kubernetes.io/pvc-protection) --
	// Delete only marks it for deletion, it does NOT block until the
	// object is actually gone. This package's own unit test for this
	// handler only ever ran against a fake clientset (no finalizer/
	// deletion-timing semantics at all), so this race was never
	// live-verified until now: an immediate Create after Delete
	// genuinely fails with "object is being deleted" against a real
	// cluster.
	deleteDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deleteDeadline) {
		if _, err := pvcs.Get(ctx, pvcName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	created, err := pvcs.Create(ctx, replacement, metav1.CreateOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("recreating pvc %s with broken storageClassName: %w", pvcName, err)
	}

	verified := created.Spec.StorageClassName != nil && *created.Spec.StorageClassName == wrongClass
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyNetworkPolicyOverblocksTraffic: content/faults/f.k8s.networkpolicy-overblocks-traffic.yaml
// (v2) params: namespace, missing_dependency (an `app` label value the
// platform team's allow-list forgot).
//
// K8s NetworkPolicy has no "deny" primitive: every NetworkPolicy
// selecting a pod is purely additive (the effective rule set is the
// UNION of every matching policy's allow rules). A v1 attempt at this
// handler tried to layer an extra "blocking" policy on top of an
// existing allow-rule for the same target and, live-verified against a
// real netpol-enforcing cluster, that traffic still got through --
// adding a policy can never subtract a peer another policy already
// allows.
//
// The fault's own content YAML frames the real scenario correctly
// though: "a default-deny policy exists, and the accompanying allow-list
// forgot one legitimate dependency." On a fresh T1 baseline (default-deny
// only, zero per-dependency allows -- internal/k8s/provision.go's
// applyDefaultDenyNetworkPolicy) that's trivially true with nothing to
// inject, which isn't a useful fault to hand a learner. So this handler
// first establishes a realistic non-empty allow-list -- as a real
// platform team's allow-list would look after onboarding a couple of
// legitimate dependencies -- deliberately omitting missing_dependency,
// making "the allow-list forgot X" the actual, observable, fully
// reversible (delete this one policy) state.
func applyNetworkPolicyOverblocksTraffic(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	missingDependency := params["missing_dependency"]
	if missingDependency == "" {
		return Result{}, fmt.Errorf("f.k8s.networkpolicy-overblocks-traffic requires param: missing_dependency")
	}
	ns := nonEmptyParam(params["namespace"], namespace)

	// known_dependencies (optional): comma-separated app labels the
	// allow-list DOES cover, simulating the rest of a real platform
	// team's allow-list. Defaults to a couple of plausible peers distinct
	// from missing_dependency so the policy isn't trivially empty.
	knownDependencies := splitCSV(params["known_dependencies"])
	if len(knownDependencies) == 0 {
		knownDependencies = []string{"auth-service", "config-service"}
	}

	policyName := "fault-allowlist-forgot-" + missingDependency
	peers := make([]networkingv1.NetworkPolicyPeer, 0, len(knownDependencies))
	for _, dep := range knownDependencies {
		if dep == missingDependency {
			continue // never accidentally include the one being forgotten
		}
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": dep}},
		})
	}

	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: ns,
			Labels:    map[string]string{"fault-injection": "f.k8s.networkpolicy-overblocks-traffic"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{}, // applies to every pod in the namespace
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{To: peers},
			},
		},
	}

	// Label the missing_dependency's own pod(s) as network-isolated --
	// REQUIRED, not optional, for this fault to have any real effect at
	// all once ensureIntraNamespacePodTrafficAllowed (internal/fixture/
	// rbac.go) exists in the namespace, confirmed live as a real,
	// serious bug this session: that blanket allow-intra-namespace
	// policy is additive (K8s NetworkPolicy has no deny primitive), so
	// this fault's own narrower allow-list was silently a no-op in any
	// environment that also seeded a multi-pod fixture (Ansible,
	// Jenkins, DinD, Gitea, Istio, Terraform) -- the missing dependency
	// stayed reachable via the blanket rule regardless of what this
	// fault's policy said. Excluding the target pod from that blanket
	// rule (via PracticeEngineNetworkIsolatedLabel) is the only way a
	// narrower rule can actually bind, since NetworkPolicy has no rule
	// precedence to rely on instead.
	pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + missingDependency})
	if err != nil {
		return Result{}, fmt.Errorf("finding %s pods to isolate: %w", missingDependency, err)
	}
	for _, pod := range pods.Items {
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		if _, already := pod.Labels[fixtureNetworkIsolatedLabel]; already {
			continue
		}
		pod.Labels[fixtureNetworkIsolatedLabel] = "true"
		if _, err := clientset.CoreV1().Pods(ns).Update(ctx, &pod, metav1.UpdateOptions{}); err != nil {
			return Result{}, fmt.Errorf("isolating pod %s from the blanket intra-namespace allow: %w", pod.Name, err)
		}
	}

	policies := clientset.NetworkingV1().NetworkPolicies(ns)
	_, err = policies.Get(ctx, policyName, metav1.GetOptions{})
	if err == nil {
		// Idempotent: the incomplete allow-list is already in place.
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if !isNotFound(err) {
		return Result{}, fmt.Errorf("checking existing networkpolicy %s: %w", policyName, err)
	}

	created, err := policies.Create(ctx, policy, metav1.CreateOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("creating allow-list networkpolicy %s: %w", policyName, err)
	}

	return Result{Applied: true, SymptomVerified: created.Name == policyName}, nil
}

// fixtureNetworkIsolatedLabel mirrors internal/fixture.
// PracticeEngineNetworkIsolatedLabel's value -- duplicated here (not
// imported; faultinjection must not depend on fixture) following this
// codebase's existing cross-package pattern.
const fixtureNetworkIsolatedLabel = "practiceengine.dev/network-isolated"

// applyStatefulSetOrdinalStuck: content/faults/f.k8s.statefulset-ordinal-stuck.yaml
// params: statefulset, stuck_ordinal.
//
// Forces pod <statefulset>-<stuck_ordinal> to never become Ready by
// patching the StatefulSet's pod template with a readiness probe that
// always fails for that specific ordinal. StatefulSets don't support
// per-pod template overrides, so this handler injects a shell-based
// readiness probe that greps the pod's own hostname (which encodes the
// ordinal, e.g. "web-1") and exits 1 only when it matches stuck_ordinal
// -- every other ordinal keeps passing, which is exactly the fault's
// contract ("one misconfigured pod blocks every higher ordinal").
func applyStatefulSetOrdinalStuck(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	name := params["statefulset"]
	stuckOrdinalStr := params["stuck_ordinal"]
	if name == "" || stuckOrdinalStr == "" {
		return Result{}, fmt.Errorf("f.k8s.statefulset-ordinal-stuck requires params: statefulset, stuck_ordinal")
	}
	if _, err := strconv.Atoi(stuckOrdinalStr); err != nil {
		return Result{}, fmt.Errorf("invalid stuck_ordinal %q: must be an integer", stuckOrdinalStr)
	}

	statefulsets := clientset.AppsV1().StatefulSets(namespace)
	sts, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*appsv1.StatefulSet, error) {
		return statefulsets.Get(ctx, name, metav1.GetOptions{})
	}, "StatefulSet", "statefulset", name)
	if err != nil {
		return notFoundOrErrResult, err
	}
	if len(sts.Spec.Template.Spec.Containers) == 0 {
		return Result{}, fmt.Errorf("statefulset %s has no containers to patch", name)
	}
	containerName := sts.Spec.Template.Spec.Containers[0].Name

	// hostname == "<statefulset>-<ordinal>" is guaranteed by the
	// StatefulSet controller's own pod-naming contract, so this probe
	// needs no other pod identity source.
	probeCmd := fmt.Sprintf(
		`test "$(hostname)" != "%s-%s"`,
		name, stuckOrdinalStr,
	)
	patchBytes, patchType, err := patchFirstContainer(containerName, func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{
			Exec:                &corev1.ExecAction{Command: []string{"sh", "-c", probeCmd}},
			InitialDelaySeconds: int32Ptr(0),
			PeriodSeconds:       int32Ptr(5),
		}
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := statefulsets.Patch(ctx, name, patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patching statefulset %s readiness probe: %w", name, err)
	}

	return Result{Applied: true, SymptomVerified: false}, nil
}

// applyRolloutStuckBadImageTag: content/faults/f.k8s.rollout-stuck-bad-image-tag.yaml
// params: deployment, bad_tag.
func applyRolloutStuckBadImageTag(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	name := params["deployment"]
	badTag := params["bad_tag"]
	if name == "" || badTag == "" {
		return Result{}, fmt.Errorf("f.k8s.rollout-stuck-bad-image-tag requires params: deployment, bad_tag")
	}

	deployments := clientset.AppsV1().Deployments(namespace)
	dep, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*appsv1.Deployment, error) {
		return deployments.Get(ctx, name, metav1.GetOptions{})
	}, "Deployment", "deployment", name)
	if err != nil {
		return notFoundOrErrResult, err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return Result{}, fmt.Errorf("deployment %s has no containers to patch", name)
	}
	container := dep.Spec.Template.Spec.Containers[0]
	repo := imageRepo(container.Image)
	brokenImage := fmt.Sprintf("%s:%s", repo, badTag)

	patchBytes, patchType, err := patchFirstContainer(container.Name, func(c *containerPatch) {
		c.Image = brokenImage
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := deployments.Patch(ctx, name, patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patching deployment %s image tag: %w", name, err)
	}

	// Verifiable immediately: the new ReplicaSet's pod template either
	// carries the broken tag or it doesn't -- ImagePullBackOff itself
	// takes real time to manifest (kubelet retry backoff), same
	// "Applied=true, SymptomVerified=false until a later health
	// re-check" stance as the other not-instantaneous faults above.
	verifyDep, err := deployments.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	applied := len(verifyDep.Spec.Template.Spec.Containers) > 0 && verifyDep.Spec.Template.Spec.Containers[0].Image == brokenImage
	return Result{Applied: applied, SymptomVerified: false}, nil
}

// imageRepo strips a trailing ":tag" or "@digest" from an image
// reference, leaving the repo path to re-tag with bad_tag. Falls back to
// the whole string unchanged if neither separator is present (e.g. a
// bare "nginx" with an implicit ":latest").
func imageRepo(image string) string {
	if at := lastIndex(image, '@'); at >= 0 {
		return image[:at]
	}
	// A ':' inside a registry host:port prefix (e.g. "localhost:5000/app")
	// must not be mistaken for the tag separator -- only consider a ':'
	// after the last '/' as the tag delimiter.
	slash := lastIndex(image, '/')
	tagSep := lastIndex(image, ':')
	if tagSep > slash {
		return image[:tagSep]
	}
	return image
}

func lastIndex(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// nonEmptyParam returns override when non-empty, else fallback -- used
// by faults whose params_schema names its own "namespace" param
// (content-authoring choice, independent of the environment's actual
// namespace) so a fault can target a non-default namespace when the
// content author explicitly says so, while still defaulting sanely.
func nonEmptyParam(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// splitCSV splits a comma-separated param value into trimmed,
// non-empty entries. Returns nil for an empty input.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
