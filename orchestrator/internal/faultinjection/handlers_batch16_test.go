package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Argo CD control plane is
// covered by TestArgoCDFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_argocd_test.go, real-infra-gated.

func TestApplyArgoCDOutOfSyncManualDrift_RequiresParams(t *testing.T) {
	_, err := applyArgoCDOutOfSyncManualDrift(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: application")
	}
}

func TestApplyArgoCDOutOfSyncManualDrift_RejectsMismatchedApplication(t *testing.T) {
	_, err := applyArgoCDOutOfSyncManualDrift(context.Background(), nil, testNamespace, map[string]string{
		"application": "not-the-real-app",
	})
	if err == nil {
		t.Fatal("expected an error for an application that doesn't match the fixture's real application")
	}
}

func TestArgoCDFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	const id = "f.gitops.argocd-out-of-sync-manual-drift"
	if _, ok := dynamicRegistry[id]; !ok {
		t.Errorf("expected %s to be registered in dynamicRegistry", id)
	}
	if _, ok := registry[id]; ok {
		t.Errorf("%s must not ALSO be registered in the typed registry", id)
	}
}
