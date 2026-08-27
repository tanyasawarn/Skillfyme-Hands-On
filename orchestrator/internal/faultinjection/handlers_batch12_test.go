package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Terraform workspace is
// covered by TestTerraformFaultInjection_LiveIntegration in
// internal/fixture/handlers_terraform_test.go, real-infra-gated.
func TestApplyTerraformStateLockOrphan_RequiresParams(t *testing.T) {
	_, err := applyTerraformStateLockOrphan(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: workspace")
	}
}

func TestApplyTerraformStateLockOrphan_RejectsMismatchedWorkspace(t *testing.T) {
	_, err := applyTerraformStateLockOrphan(context.Background(), nil, testNamespace, map[string]string{
		"workspace": "not-the-real-workspace",
	})
	if err == nil {
		t.Fatal("expected an error for a workspace that doesn't match the fixture's real lock-capable workspace")
	}
}

func TestApplyTerraformStateDriftManualChange_RequiresParams(t *testing.T) {
	_, err := applyTerraformStateDriftManualChange(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: resource_address")
	}
}

func TestApplyTerraformStateDriftManualChange_RejectsMismatchedResourceAddress(t *testing.T) {
	_, err := applyTerraformStateDriftManualChange(context.Background(), nil, testNamespace, map[string]string{
		"resource_address": "aws_instance.not_real",
	})
	if err == nil {
		t.Fatal("expected an error for a resource_address that doesn't match the fixture's real drift-capable resource")
	}
}

func TestApplyTerraformModuleVersionPinMismatch_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"module_source": "hashicorp/dir/template"},
		{"module_source": "hashicorp/dir/template", "version_a": "1.0.0"},
	}
	for _, params := range cases {
		_, err := applyTerraformModuleVersionPinMismatch(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyTerraformModuleVersionPinMismatch_RejectsMismatchedModuleSource(t *testing.T) {
	_, err := applyTerraformModuleVersionPinMismatch(context.Background(), nil, testNamespace, map[string]string{
		"module_source": "not/the/real/module", "version_a": "1.0.0", "version_b": "1.0.2",
	})
	if err == nil {
		t.Fatal("expected an error for a module_source that doesn't match the fixture's real registry module")
	}
}

func TestApplyTerraformModuleVersionPinMismatch_RejectsMismatchedVersions(t *testing.T) {
	_, err := applyTerraformModuleVersionPinMismatch(context.Background(), nil, testNamespace, map[string]string{
		"module_source": "hashicorp/dir/template", "version_a": "9.9.9", "version_b": "1.0.2",
	})
	if err == nil {
		t.Fatal("expected an error for version_a/version_b that don't match the fixture's real resolved versions")
	}
}

func TestTerraformFaults_AreRegisteredAsDynamicHandlers(t *testing.T) {
	for _, id := range []string{
		"f.tf.state-lock-orphan",
		"f.tf.state-drift-manual-change",
		"f.tf.module-version-pin-mismatch",
	} {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("expected %s to be registered in dynamicRegistry", id)
		}
		if _, ok := registry[id]; ok {
			t.Errorf("%s must not ALSO be registered in the typed registry", id)
		}
	}
}
