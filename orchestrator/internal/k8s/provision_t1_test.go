package k8s

import "testing"

func TestRuntimeClassForT1_DisabledByDefault(t *testing.T) {
	if got := runtimeClassForT1(false); got != nil {
		t.Errorf("expected nil RuntimeClassName when gVisor is disabled, got %v", *got)
	}
}

func TestRuntimeClassForT1_EnabledSetsGvisor(t *testing.T) {
	got := runtimeClassForT1(true)
	if got == nil {
		t.Fatal("expected a non-nil RuntimeClassName when gVisor is enabled")
	}
	if *got != "gvisor" {
		t.Errorf("expected RuntimeClassName=gvisor, got %q", *got)
	}
}
