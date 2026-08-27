package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Jaeger + sample app is
// covered by TestJaegerFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_jaeger_test.go, real-infra-gated.
func TestApplyJaegerMissingTraceContextPropagation_RequiresService(t *testing.T) {
	_, err := applyJaegerMissingTraceContextPropagation(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing service param")
	}
}

func TestApplyJaegerMissingTraceContextPropagation_RejectsMismatchedService(t *testing.T) {
	_, err := applyJaegerMissingTraceContextPropagation(context.Background(), nil, testNamespace, map[string]string{"service": "not-the-real-frontend"})
	if err == nil {
		t.Fatal("expected an error for a service that doesn't match the fixture's real frontend service")
	}
}

func TestJaegerFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.jaeger.missing-trace-context-propagation"]; !ok {
		t.Fatal("expected f.jaeger.missing-trace-context-propagation to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.jaeger.missing-trace-context-propagation"]; ok {
		t.Fatal("f.jaeger.missing-trace-context-propagation must not ALSO be registered in the typed registry")
	}
}
