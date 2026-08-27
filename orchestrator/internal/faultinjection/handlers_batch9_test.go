package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Jenkins controller +
// agent is covered by TestJenkinsFixtureAndFaults_LiveIntegration in
// internal/fixture/handlers_jenkins_test.go, real-infra-gated.
func TestApplyJenkinsAgentOffline_RequiresAgentLabel(t *testing.T) {
	_, err := applyJenkinsAgentOffline(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing agent_label")
	}
}

func TestApplyJenkinsAgentOffline_RejectsMismatchedAgentLabel(t *testing.T) {
	_, err := applyJenkinsAgentOffline(context.Background(), nil, testNamespace, map[string]string{"agent_label": "not-the-real-agent"})
	if err == nil {
		t.Fatal("expected an error for an agent_label that doesn't match the fixture's real agent")
	}
}

func TestApplyJenkinsStaleCachedDependency_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"job": "practice-pipeline"},
		{"stale_dependency": "0.9.0"},
	}
	for _, params := range cases {
		_, err := applyJenkinsStaleCachedDependency(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyJenkinsStaleCachedDependency_RejectsMismatchedJob(t *testing.T) {
	_, err := applyJenkinsStaleCachedDependency(context.Background(), nil, testNamespace, map[string]string{
		"job": "not-the-real-job", "stale_dependency": "0.9.0",
	})
	if err == nil {
		t.Fatal("expected an error for a job that doesn't match the fixture's real job")
	}
}

func TestJenkinsFaults_AreRegisteredAsDynamicHandlers(t *testing.T) {
	for _, id := range []string{"f.jenkins.agent-offline", "f.jenkins.stale-cached-dependency"} {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("expected %s to be registered in dynamicRegistry", id)
		}
		if _, ok := registry[id]; ok {
			t.Errorf("%s must not ALSO be registered in the typed registry", id)
		}
	}
}
