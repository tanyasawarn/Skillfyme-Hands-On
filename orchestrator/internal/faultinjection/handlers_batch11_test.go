package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against real SSH targets is covered by
// TestAnsibleFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_ansible_test.go, real-infra-gated.
func TestApplyAnsibleInventoryHostUnreachable_RequiresInventoryHost(t *testing.T) {
	_, err := applyAnsibleInventoryHostUnreachable(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing inventory_host")
	}
}

func TestApplyAnsibleInventoryHostUnreachable_RejectsMismatchedHost(t *testing.T) {
	_, err := applyAnsibleInventoryHostUnreachable(context.Background(), nil, testNamespace, map[string]string{"inventory_host": "not-a-real-host"})
	if err == nil {
		t.Fatal("expected an error for an inventory_host that doesn't match the fixture's real blockable host")
	}
}

func TestAnsibleFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.ansible.inventory-host-unreachable"]; !ok {
		t.Fatal("expected f.ansible.inventory-host-unreachable to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.ansible.inventory-host-unreachable"]; ok {
		t.Fatal("f.ansible.inventory-host-unreachable must not ALSO be registered in the typed registry")
	}
}
