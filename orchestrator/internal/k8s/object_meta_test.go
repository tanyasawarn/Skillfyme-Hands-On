package k8s

import (
	"testing"
)

// PLAN.md U13: ObjectMeta(name, ns) replaces the repeated
// metav1.ObjectMeta{Name: X, Namespace: ns} struct literal at 6 real
// call sites (applyResourceQuota, applyLimitRange,
// applyDefaultDenyNetworkPolicy, applyEgressProxyAllowlist,
// applyServiceAccount, createWorkspacePod's Service).
func TestObjectMeta_SetsNameAndNamespace(t *testing.T) {
	got := ObjectMeta("env-quota", "env-abc123")
	if got.Name != "env-quota" {
		t.Errorf("ObjectMeta().Name = %q, want %q", got.Name, "env-quota")
	}
	if got.Namespace != "env-abc123" {
		t.Errorf("ObjectMeta().Namespace = %q, want %q", got.Namespace, "env-abc123")
	}
}

func TestObjectMeta_SetsNoOtherFields(t *testing.T) {
	// Regression guard: this helper is deliberately a 2-field
	// constructor. createNamespace's Namespace object and
	// createWorkspacePod's own Pod both set Labels too and stay as
	// their own literals rather than being forced through this helper
	// -- confirm ObjectMeta() itself never sets Labels/Annotations, so
	// a future caller can't silently assume it does.
	got := ObjectMeta("x", "ns")
	if got.Labels != nil {
		t.Errorf("ObjectMeta() set Labels = %v, want nil", got.Labels)
	}
	if got.Annotations != nil {
		t.Errorf("ObjectMeta() set Annotations = %v, want nil", got.Annotations)
	}
}
