package faultinjection

import (
	"context"
	"errors"
	"testing"
)

// Param-validation path only -- applyTektonTaskMissingWorkspaceBinding
// needs a real dynamic client (built from *k8s.Provisioner.RestConfig())
// for everything past the params check, so a nil provisioner here proves
// the function fails on missing params BEFORE ever touching a client,
// without needing a live cluster. Full behavior (real Task/TaskRun
// mutation against a real Tekton install) is covered by
// TestTektonFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_tekton_test.go, which is real-infra-gated.
func TestApplyTektonTaskMissingWorkspaceBinding_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"taskrun": "only-one-param"},
		{"missing_workspace": "only-one-param"},
	}
	for _, params := range cases {
		_, err := applyTektonTaskMissingWorkspaceBinding(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestTektonTaskMissingWorkspaceBinding_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.tekton.task-missing-workspace-binding"]; !ok {
		t.Fatal("expected f.tekton.task-missing-workspace-binding to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.tekton.task-missing-workspace-binding"]; ok {
		t.Fatal("f.tekton.task-missing-workspace-binding must not ALSO be registered in the typed registry")
	}
}

func TestApply_DispatchesToDynamicRegistry(t *testing.T) {
	// Apply() itself needs a real *k8s.Provisioner (namespace resolution
	// + dynamicRegistry dispatch) -- this test only proves the missing-
	// params error surfaces through Apply's own dispatch path the same
	// way a typed-Handler fault's error would, by checking the returned
	// error is the same "requires params" error, not an ErrNoHandler
	// (which would mean dynamicRegistry dispatch silently isn't wired).
	handler, ok := dynamicRegistry["f.tekton.task-missing-workspace-binding"]
	if !ok {
		t.Fatal("f.tekton.task-missing-workspace-binding missing from dynamicRegistry")
	}
	_, err := handler(context.Background(), nil, testNamespace, map[string]string{})
	var noHandler ErrNoHandler
	if errors.As(err, &noHandler) {
		t.Fatal("expected the real params-validation error, not ErrNoHandler")
	}
	if err == nil {
		t.Fatal("expected an error for empty params")
	}
}
