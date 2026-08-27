package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// applyT2PodShape and the tier-branch logic in applyResourceQuota/
// applyLimitRange/createNamespace are pure in-memory transforms (no K8s
// API calls) -- tested directly here so the actual pod/quota shape T2
// produces is verified without needing a live cluster for these
// specific assertions.
//
// A real, live-cluster-gated test of the FULL Provision(T2) path exists
// separately in provision_t2_live_test.go -- this file's own earlier
// doc comment claimed "no live K8s cluster reachable" as the reason
// end-to-end T2 verification wasn't attempted; that claim was stale by
// the time it was checked this session (a live dev cluster has been
// reachable and in continuous use throughout). See that file for the
// real, live-verified outcome: RuntimeClass "kata" not found (no
// Kata-capable node exists in this environment) -- the actual,
// concrete, environment-specific gap, not an untested code path.

func baseT1PodSpec() *corev1.PodSpec {
	allowPrivilegeEscalation := false
	return &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name: "shell",
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allowPrivilegeEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
}

func TestApplyT2PodShape_SetsKataRuntimeClass(t *testing.T) {
	podSpec := baseT1PodSpec()
	applyT2PodShape(podSpec, ProvisionRequest{})

	if podSpec.RuntimeClassName == nil || *podSpec.RuntimeClassName != "kata" {
		t.Errorf("expected RuntimeClassName=kata, got %v", podSpec.RuntimeClassName)
	}
}

func TestApplyT2PodShape_SetsNodeSelectorAndToleration(t *testing.T) {
	podSpec := baseT1PodSpec()
	applyT2PodShape(podSpec, ProvisionRequest{})

	if podSpec.NodeSelector["practiceengine.dev/tier2"] != "true" {
		t.Errorf("expected nodeSelector practiceengine.dev/tier2=true, got %v", podSpec.NodeSelector)
	}

	found := false
	for _, tol := range podSpec.Tolerations {
		if tol.Key == "workload" && tol.Value == "learner-t2" && tol.Effect == corev1.TaintEffectNoSchedule {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a toleration for workload=learner-t2:NoSchedule, got %+v", podSpec.Tolerations)
	}
}

func TestApplyT2PodShape_SetsPrivilegedSecurityContext(t *testing.T) {
	podSpec := baseT1PodSpec()
	applyT2PodShape(podSpec, ProvisionRequest{})

	sc := podSpec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Errorf("expected shell container Privileged=true (required for DinD/systemd/eBPF per doc §5.1), got %+v", sc)
	}
}

func TestApplyT2PodShape_UsesRequestedResourcesOverDefault(t *testing.T) {
	podSpec := baseT1PodSpec()
	applyT2PodShape(podSpec, ProvisionRequest{
		Resources: ResourceSpec{CPU: "6", Memory: "12Gi"},
	})

	limits := podSpec.Containers[0].Resources.Limits
	if limits.Cpu().String() != "6" {
		t.Errorf("expected CPU limit 6, got %s", limits.Cpu().String())
	}
	if limits.Memory().String() != "12Gi" {
		t.Errorf("expected memory limit 12Gi, got %s", limits.Memory().String())
	}
}

func TestApplyT2PodShape_DefaultsResourcesWhenUnset(t *testing.T) {
	podSpec := baseT1PodSpec()
	applyT2PodShape(podSpec, ProvisionRequest{})

	limits := podSpec.Containers[0].Resources.Limits
	if limits.Cpu().String() != "8" {
		t.Errorf("expected default T2 CPU limit 8 (matching applyLimitRange's T2 ceiling), got %s", limits.Cpu().String())
	}
	if limits.Memory().String() != "16Gi" {
		t.Errorf("expected default T2 memory limit 16Gi (matching applyLimitRange's T2 ceiling), got %s", limits.Memory().String())
	}
}

func TestApplyResourceQuota_T2HasHigherCeilingsThanT1(t *testing.T) {
	t1Quota := resourceQuotaFor(TierT1SharedContainer)
	t2Quota := resourceQuotaFor(TierT2IsolatedMicroVM)

	t1CPU := t1Quota[corev1.ResourceRequestsCPU]
	t2CPU := t2Quota[corev1.ResourceRequestsCPU]
	if t2CPU.Cmp(t1CPU) <= 0 {
		t.Errorf("expected T2 CPU quota (%s) > T1 CPU quota (%s)", t2CPU.String(), t1CPU.String())
	}

	t1Mem := t1Quota[corev1.ResourceRequestsMemory]
	t2Mem := t2Quota[corev1.ResourceRequestsMemory]
	if t2Mem.Cmp(t1Mem) <= 0 {
		t.Errorf("expected T2 memory quota (%s) > T1 memory quota (%s)", t2Mem.String(), t1Mem.String())
	}

	t1Pods := t1Quota[corev1.ResourcePods]
	t2Pods := t2Quota[corev1.ResourcePods]
	if t2Pods.Cmp(t1Pods) <= 0 {
		t.Errorf("expected T2 pod quota (%s) > T1 pod quota (%s) -- T2 must fit a nested k3s control plane", t2Pods.String(), t1Pods.String())
	}
}

func TestApplyLimitRange_T2HasHigherMaxThanT1(t *testing.T) {
	t1Max := limitRangeMaxFor(TierT1SharedContainer)
	t2Max := limitRangeMaxFor(TierT2IsolatedMicroVM)

	t1CPU := t1Max[corev1.ResourceCPU]
	t2CPU := t2Max[corev1.ResourceCPU]
	if t2CPU.Cmp(t1CPU) <= 0 {
		t.Errorf("expected T2 LimitRange CPU max (%s) > T1 (%s)", t2CPU.String(), t1CPU.String())
	}
}

func TestPSSLevelFor_T1RemainsRestricted(t *testing.T) {
	if got := pssLevelFor(TierT1SharedContainer); got != "restricted" {
		t.Errorf("expected T1 PSS level=restricted, got %q", got)
	}
}

func TestPSSLevelFor_T2IsPrivileged(t *testing.T) {
	// Not a security regression: T2's isolation boundary is Kata's
	// hardware virtualisation (own kernel), not PSS's container-capability
	// restrictions -- see createNamespace's doc comment. This test exists
	// specifically to catch the bug this pass found and fixed: a T2
	// namespace stuck at PSS `restricted` rejects every T2 pod outright
	// (Privileged: true is forbidden under `restricted`), before the
	// scheduler even considers RuntimeClass/node placement.
	if got := pssLevelFor(TierT2IsolatedMicroVM); got != "privileged" {
		t.Errorf("expected T2 PSS level=privileged (required so PSS admission doesn't reject the privileged DinD container before scheduling), got %q", got)
	}
}
