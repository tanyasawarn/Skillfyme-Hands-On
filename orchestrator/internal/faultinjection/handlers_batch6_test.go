package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- both handlers need a real
// *k8s.Provisioner (ConfigMap read/update, pod exec for the reload)
// for everything past param checks. Full behavior against a real
// Prometheus instance is covered by
// TestPrometheusFixtureAndFaults_LiveIntegration in
// internal/fixture/handlers_prometheus_test.go, real-infra-gated.
func TestApplyPrometheusScrapeTargetDown_RequiresJobName(t *testing.T) {
	_, err := applyPrometheusScrapeTargetDown(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing job_name")
	}
}

func TestApplyPrometheusScrapeTargetDown_RejectsMismatchedJobName(t *testing.T) {
	_, err := applyPrometheusScrapeTargetDown(context.Background(), nil, testNamespace, map[string]string{"job_name": "not-the-real-job"})
	if err == nil {
		t.Fatal("expected an error for a job_name that doesn't match the fixture's real scrape job")
	}
}

func TestApplyPrometheusAlertRuleSyntaxSilentFail_RequiresRulesFile(t *testing.T) {
	_, err := applyPrometheusAlertRuleSyntaxSilentFail(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing rules_file")
	}
}

func TestApplyPrometheusAlertRuleSyntaxSilentFail_RejectsMismatchedRulesFile(t *testing.T) {
	_, err := applyPrometheusAlertRuleSyntaxSilentFail(context.Background(), nil, testNamespace, map[string]string{"rules_file": "not-the-real-file.yml"})
	if err == nil {
		t.Fatal("expected an error for a rules_file that doesn't match the fixture's real rules file")
	}
}

func TestPrometheusFaults_AreRegisteredAsDynamicHandlers(t *testing.T) {
	for _, id := range []string{"f.prometheus.scrape-target-down", "f.prometheus.alert-rule-syntax-silent-fail"} {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("expected %s to be registered in dynamicRegistry", id)
		}
		if _, ok := registry[id]; ok {
			t.Errorf("%s must not ALSO be registered in the typed registry", id)
		}
	}
}
