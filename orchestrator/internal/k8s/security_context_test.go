package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// PLAN.md Phase 3's U12: these builders replace an identical
// PodSecurityContext struct literal duplicated in createWorkspacePod
// and internal/faultinjection/handlers_batch3.go's traffic-spike fault,
// and a near-identical (differing only in ReadOnlyRootFilesystem)
// SecurityContext struct literal in the same two places. Every field
// asserted here is load-bearing against this project's real PSS
// "restricted" admission controller -- confirmed live earlier this
// session: a pod/container missing any one of these is rejected
// outright by the actual cluster, not just a style preference.

func TestRestrictedPodSecurityContext_SatisfiesPSSRestricted(t *testing.T) {
	sc := RestrictedPodSecurityContext()

	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("PSS restricted requires runAsNonRoot=true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 1000 {
		t.Errorf("expected RunAsUser=1000, got %v", sc.RunAsUser)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("PSS restricted requires seccompProfile.type=RuntimeDefault")
	}
}

func TestRestrictedPodSecurityContext_ReturnsIndependentInstancesNotSharedPointers(t *testing.T) {
	// A real bug class this guards against: if RunAsNonRoot/RunAsUser
	// were package-level vars whose addresses got shared across calls,
	// two callers mutating "their own" SecurityContext (e.g. a future
	// caller that wants a different RunAsUser) would silently corrupt
	// each other's pod spec. Each call must produce a fully independent
	// object graph.
	a := RestrictedPodSecurityContext()
	b := RestrictedPodSecurityContext()
	if a.RunAsUser == b.RunAsUser {
		t.Error("expected independent *int64 pointers across calls, got the same pointer -- mutating one caller's SecurityContext would corrupt another's")
	}
	*a.RunAsUser = 9999
	if *b.RunAsUser == 9999 {
		t.Error("mutating one returned SecurityContext's RunAsUser affected a different call's result")
	}
}

func TestRestrictedContainerSecurityContext_SatisfiesPSSRestricted(t *testing.T) {
	sc := RestrictedContainerSecurityContext(false)

	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("PSS restricted requires allowPrivilegeEscalation=false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("PSS restricted requires capabilities.drop=[ALL]")
	}
}

func TestRestrictedContainerSecurityContext_ReadOnlyRootFilesystemIsCallerControlled(t *testing.T) {
	// The one real difference between this function's two call sites --
	// createWorkspacePod needs false (learner's /workspace and package
	// managers need a writable root fs), the traffic-spike fault Job has
	// no such requirement. Confirms the parameter actually controls the
	// output rather than being silently ignored.
	writable := RestrictedContainerSecurityContext(false)
	if writable.ReadOnlyRootFilesystem == nil || *writable.ReadOnlyRootFilesystem {
		t.Error("expected ReadOnlyRootFilesystem=false when requested")
	}

	readOnly := RestrictedContainerSecurityContext(true)
	if readOnly.ReadOnlyRootFilesystem == nil || !*readOnly.ReadOnlyRootFilesystem {
		t.Error("expected ReadOnlyRootFilesystem=true when requested")
	}
}

func TestRestrictedContainerSecurityContext_ReturnsIndependentInstances(t *testing.T) {
	a := RestrictedContainerSecurityContext(false)
	b := RestrictedContainerSecurityContext(true)
	if a.ReadOnlyRootFilesystem == b.ReadOnlyRootFilesystem {
		t.Error("expected independent *bool pointers across calls with different arguments")
	}
}
