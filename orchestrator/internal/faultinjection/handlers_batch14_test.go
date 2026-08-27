package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real DinD daemon is covered
// by TestDinDFixtureAndFaults_LiveIntegration in
// internal/fixture/handlers_dind_test.go, real-infra-gated.

func TestApplyDockerfileWrongWorkdir_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"image": "practice-app:latest"},
		{"wrong_workdir": "/wrong-path"},
	}
	for _, params := range cases {
		_, err := applyDockerfileWrongWorkdir(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyDockerfileWrongWorkdir_RejectsTheActualCorrectWorkdir(t *testing.T) {
	_, err := applyDockerfileWrongWorkdir(context.Background(), nil, testNamespace, map[string]string{
		"image": "practice-app:latest", "wrong_workdir": "/app",
	})
	if err == nil {
		t.Fatal("expected an error when wrong_workdir is actually the app's real (correct) path")
	}
}

func TestApplyDockerNetworkNotAttached_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"client_container": "practice-client"},
		{"correct_network": "practice-net"},
	}
	for _, params := range cases {
		_, err := applyDockerNetworkNotAttached(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyDockerSwarmServiceImagePullFail_RequiresParams(t *testing.T) {
	_, err := applyDockerSwarmServiceImagePullFail(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: service")
	}
}

func TestApplyDockerSwarmServiceImagePullFail_RejectsMismatchedService(t *testing.T) {
	_, err := applyDockerSwarmServiceImagePullFail(context.Background(), nil, testNamespace, map[string]string{
		"service": "not-the-real-service",
	})
	if err == nil {
		t.Fatal("expected an error for a service that doesn't match the fixture's real service")
	}
}

func TestApplyGitHubActionsSecretNotPassed_RequiresParams(t *testing.T) {
	_, err := applyGitHubActionsSecretNotPassed(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: workflow")
	}
}

func TestApplyGitHubActionsSecretNotPassed_RejectsMismatchedWorkflow(t *testing.T) {
	_, err := applyGitHubActionsSecretNotPassed(context.Background(), nil, testNamespace, map[string]string{
		"workflow": "not-the-real-workflow.yml",
	})
	if err == nil {
		t.Fatal("expected an error for a workflow that doesn't match the fixture's real broken caller workflow")
	}
}

func TestDinDFaults_AreRegisteredAsDynamicHandlers(t *testing.T) {
	for _, id := range []string{
		"f.docker.dockerfile-wrong-workdir",
		"f.docker.network-not-attached",
		"f.docker.swarm-service-image-pull-fail",
		"f.github.actions-secret-not-passed",
	} {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("expected %s to be registered in dynamicRegistry", id)
		}
		if _, ok := registry[id]; ok {
			t.Errorf("%s must not ALSO be registered in the typed registry", id)
		}
	}
}
