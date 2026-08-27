package k8s

import "testing"

// PLAN.md K16: createNamespace's label map and ListManagedNamespaces's
// selector string used to be built independently, with zero
// compile-time link -- this pins that they now derive from the exact
// same key/value pair.
func TestManagedNamespaceLabelSelector_MatchesKeyAndValue(t *testing.T) {
	want := ManagedNamespaceLabelKey + "=" + ManagedNamespaceLabelValue
	if ManagedNamespaceLabelSelector != want {
		t.Errorf("ManagedNamespaceLabelSelector = %q, want %q", ManagedNamespaceLabelSelector, want)
	}
}

func TestManagedNamespaceLabel_MatchesDocumentedValue(t *testing.T) {
	// Doc §5.6: reaper's orphan sweep finds namespaces "labelled
	// practiceengine.dev/managed=true".
	if ManagedNamespaceLabelKey != "practiceengine.dev/managed" {
		t.Errorf("ManagedNamespaceLabelKey = %q, want %q", ManagedNamespaceLabelKey, "practiceengine.dev/managed")
	}
	if ManagedNamespaceLabelValue != "true" {
		t.Errorf("ManagedNamespaceLabelValue = %q, want %q", ManagedNamespaceLabelValue, "true")
	}
}
