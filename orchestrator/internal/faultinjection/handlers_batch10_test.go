package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Helm release is covered
// by TestHelmFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_helm_test.go, real-infra-gated.
func TestApplyHelmValuesOverrideNotApplied_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"release": "practice-release"},
		{"wrong_key_path": "config.featurFlag"},
	}
	for _, params := range cases {
		_, err := applyHelmValuesOverrideNotApplied(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyHelmValuesOverrideNotApplied_RejectsMismatchedRelease(t *testing.T) {
	_, err := applyHelmValuesOverrideNotApplied(context.Background(), nil, testNamespace, map[string]string{
		"release": "not-the-real-release", "wrong_key_path": "config.featurFlag",
	})
	if err == nil {
		t.Fatal("expected an error for a release that doesn't match the fixture's real release")
	}
}

func TestApplyHelmValuesOverrideNotApplied_RejectsTheActualCorrectKeyPath(t *testing.T) {
	_, err := applyHelmValuesOverrideNotApplied(context.Background(), nil, testNamespace, map[string]string{
		"release": "practice-release", "wrong_key_path": "config.featureFlag",
	})
	if err == nil {
		t.Fatal("expected an error when wrong_key_path is actually the chart's real (correct) key path")
	}
}

func TestHelmFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.helm.values-override-not-applied"]; !ok {
		t.Fatal("expected f.helm.values-override-not-applied to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.helm.values-override-not-applied"]; ok {
		t.Fatal("f.helm.values-override-not-applied must not ALSO be registered in the typed registry")
	}
}
