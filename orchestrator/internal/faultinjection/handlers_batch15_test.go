package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Istio control plane is
// covered by TestIstioFixtureAndFaults_LiveIntegration in
// internal/fixture/handlers_istio_test.go, real-infra-gated.

func TestApplyIstioMTLSModeMismatch_RequiresParams(t *testing.T) {
	_, err := applyIstioMTLSModeMismatch(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: destinationrule")
	}
}

func TestApplyIstioMTLSModeMismatch_RejectsMismatchedDestinationRule(t *testing.T) {
	_, err := applyIstioMTLSModeMismatch(context.Background(), nil, testNamespace, map[string]string{
		"destinationrule": "not-the-real-dr",
	})
	if err == nil {
		t.Fatal("expected an error for a destinationrule that doesn't match the fixture's real destinationrule")
	}
}

func TestApplyIstioVirtualServiceWeightSumInvalid_RequiresParams(t *testing.T) {
	_, err := applyIstioVirtualServiceWeightSumInvalid(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: virtualservice")
	}
}

func TestApplyIstioVirtualServiceWeightSumInvalid_RejectsMismatchedVirtualService(t *testing.T) {
	_, err := applyIstioVirtualServiceWeightSumInvalid(context.Background(), nil, testNamespace, map[string]string{
		"virtualservice": "not-the-real-vs",
	})
	if err == nil {
		t.Fatal("expected an error for a virtualservice that doesn't match the fixture's real virtualservice")
	}
}

func TestIstioFaults_AreRegisteredAsDynamicHandlers(t *testing.T) {
	for _, id := range []string{
		"f.istio.mtls-mode-mismatch",
		"f.istio.virtualservice-weight-sum-invalid",
	} {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("expected %s to be registered in dynamicRegistry", id)
		}
		if _, ok := registry[id]; ok {
			t.Errorf("%s must not ALSO be registered in the typed registry", id)
		}
	}
}

func TestRequiresT2_IdentifiesExactlyTheThreeNonAWST2Faults(t *testing.T) {
	for _, id := range []string{
		"f.istio.mtls-mode-mismatch",
		"f.istio.virtualservice-weight-sum-invalid",
		"f.gitops.argocd-out-of-sync-manual-drift",
	} {
		if !RequiresT2(id) {
			t.Errorf("expected RequiresT2(%s) = true", id)
		}
	}
	for _, id := range []string{
		"f.cloud.iam-overpermissive-role",
		"f.iam.missing-ecr-pull",
		"f.tf.state-lock-orphan",
	} {
		if RequiresT2(id) {
			t.Errorf("expected RequiresT2(%s) = false", id)
		}
	}
}
