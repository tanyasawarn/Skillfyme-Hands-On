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
// As of the ₹100/user cost decision, T2 runs under the **Sysbox**
// runtime (RuntimeClassName from ProvisionerConfig.T2RuntimeClass,
// default "sysbox-runc") on the SAME shared node pool as T1 -- no
// dedicated Kata metal pool, no microVM. So these tests assert the
// Sysbox pod shape, not the old Kata one: a configurable RuntimeClass,
// NO tier2 nodeSelector/toleration, root-in-userns (not privileged) by
// default, privileged only when the blueprint declares an eBPF
// capability. See applyT2PodShape's doc comment and
// docs/t2-cost-optimization-100.md.
//
// The Kata shape is preserved in git history + documented as the
// scale-up path in infra/practice-cluster/t2-nodepool-kata/.

func baseT1PodSpec() *corev1.PodSpec {
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	var runAsUser int64 = 1000
	return &corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: &runAsNonRoot,
			RunAsUser:    &runAsUser,
		},
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

// sysboxProvisioner returns a Provisioner whose config uses the default
// Sysbox runtime class, for the pure applyT2PodShape tests.
func sysboxProvisioner() *Provisioner {
	return &Provisioner{cfg: ProvisionerConfig{T2RuntimeClass: T2RuntimeClassDefault}}
}

func TestApplyT2PodShape_SetsConfiguredRuntimeClass(t *testing.T) {
	podSpec := baseT1PodSpec()
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{})

	if podSpec.RuntimeClassName == nil || *podSpec.RuntimeClassName != "sysbox-runc" {
		t.Errorf("expected RuntimeClassName=sysbox-runc (the default T2RuntimeClass), got %v", podSpec.RuntimeClassName)
	}
}

func TestApplyT2PodShape_RespectsCustomRuntimeClass(t *testing.T) {
	podSpec := baseT1PodSpec()
	p := &Provisioner{cfg: ProvisionerConfig{T2RuntimeClass: "kata"}}
	p.applyT2PodShape(podSpec, ProvisionRequest{})

	if podSpec.RuntimeClassName == nil || *podSpec.RuntimeClassName != "kata" {
		t.Errorf("expected RuntimeClassName=kata (operator override via ORCHESTRATOR_T2_RUNTIME_CLASS), got %v", podSpec.RuntimeClassName)
	}
}

func TestApplyT2PodShape_EmptyRuntimeClassLeavesItUnset(t *testing.T) {
	// Local dev with no Sysbox: honest degradation to a plain container
	// (same stance runtimeClassForT1 takes when gVisor is off), NOT a
	// hardcoded class that makes every Provision fail to schedule.
	podSpec := baseT1PodSpec()
	p := &Provisioner{cfg: ProvisionerConfig{T2RuntimeClass: ""}}
	p.applyT2PodShape(podSpec, ProvisionRequest{})

	if podSpec.RuntimeClassName != nil {
		t.Errorf("expected RuntimeClassName unset when T2RuntimeClass is empty, got %v", *podSpec.RuntimeClassName)
	}
}

func TestApplyT2PodShape_DoesNotSetTier2NodeSelectorOrToleration(t *testing.T) {
	// Sysbox runs on the SAME shared node pool as T1. There is no
	// practiceengine.dev/tier2 metal pool to pin to.
	podSpec := baseT1PodSpec()
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{})

	if _, ok := podSpec.NodeSelector["practiceengine.dev/tier2"]; ok {
		t.Errorf("Sysbox T2 must NOT set a practiceengine.dev/tier2 nodeSelector; got %v", podSpec.NodeSelector)
	}
	for _, tol := range podSpec.Tolerations {
		if tol.Key == "workload" && tol.Value == "learner-t2" {
			t.Errorf("Sysbox T2 must NOT add a learner-t2 toleration; got %+v", podSpec.Tolerations)
		}
	}
}

func TestApplyT2PodShape_RunsAsRootInUserNS_NotPrivilegedByDefault(t *testing.T) {
	podSpec := baseT1PodSpec()
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{})

	// Pod-level: root allowed (Sysbox remaps it to an unprivileged host uid).
	if podSpec.SecurityContext == nil || podSpec.SecurityContext.RunAsNonRoot == nil || *podSpec.SecurityContext.RunAsNonRoot {
		t.Errorf("expected pod SecurityContext.RunAsNonRoot=false for Sysbox, got %+v", podSpec.SecurityContext)
	}
	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser != 0 {
		t.Errorf("expected pod SecurityContext.RunAsUser=0 for Sysbox, got %+v", podSpec.SecurityContext)
	}

	sc := podSpec.Containers[0].SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Errorf("expected shell container RunAsUser=0, got %+v", sc)
	}
	// Crucially: NOT privileged by default. That is the whole point of
	// Sysbox -- DinD/systemd/nested-k3s without a capability grant.
	if sc.Privileged != nil && *sc.Privileged {
		t.Error("Sysbox T2 shell container must NOT be privileged by default (DinD/systemd work via user-namespace isolation)")
	}
}

func TestApplyT2PodShape_PrivilegedOnlyForEbpfWorkloads(t *testing.T) {
	podSpec := baseT1PodSpec()
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{PrivilegedWorkload: true})

	sc := podSpec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Errorf("expected shell container Privileged=true when PrivilegedWorkload is set (eBPF-capability blueprint), got %+v", sc)
	}
}

func TestApplyT2PodShape_UsesRequestedResourcesOverDefault(t *testing.T) {
	podSpec := baseT1PodSpec()
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{
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
	sysboxProvisioner().applyT2PodShape(podSpec, ProvisionRequest{})

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
	// Still `privileged`, but for a Sysbox reason now: the T2 pod runs
	// its shell container as root-in-userns (RunAsNonRoot=false,
	// RunAsUser=0) and, for eBPF blueprints, privileged: true. PSS
	// `restricted` and `baseline` both forbid RunAsUser=0 / privileged,
	// so the namespace must be `privileged` or admission rejects the pod
	// before scheduling. Sysbox -- not PSS -- is the isolation boundary
	// here (user-namespace remapping), the same way Kata's VM was the
	// boundary in the previous design.
	if got := pssLevelFor(TierT2IsolatedMicroVM); got != "privileged" {
		t.Errorf("expected T2 PSS level=privileged (Sysbox shell runs as root-in-userns; PSS restricted/baseline would reject it), got %q", got)
	}
}
