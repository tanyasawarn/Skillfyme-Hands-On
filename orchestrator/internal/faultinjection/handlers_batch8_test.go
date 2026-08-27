package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Logstash + Elasticsearch
// is covered by TestELKFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_elk_test.go, real-infra-gated.
func TestApplyELKLogstashPipelineBlocked_RequiresParams(t *testing.T) {
	cases := []map[string]string{
		{},
		{"index": "practice-logs"},
		{"conflicting_field": "conflict_field"},
	}
	for _, params := range cases {
		_, err := applyELKLogstashPipelineBlocked(context.Background(), nil, testNamespace, params)
		if err == nil {
			t.Errorf("params=%v: expected an error for missing required params, got none", params)
		}
	}
}

func TestApplyELKLogstashPipelineBlocked_RejectsMismatchedIndex(t *testing.T) {
	_, err := applyELKLogstashPipelineBlocked(context.Background(), nil, testNamespace, map[string]string{
		"index": "not-the-real-index", "conflicting_field": "conflict_field",
	})
	if err == nil {
		t.Fatal("expected an error for an index that doesn't match the fixture's real index")
	}
}

func TestApplyELKLogstashPipelineBlocked_RejectsMismatchedField(t *testing.T) {
	_, err := applyELKLogstashPipelineBlocked(context.Background(), nil, testNamespace, map[string]string{
		"index": "practice-logs", "conflicting_field": "not-the-real-field",
	})
	if err == nil {
		t.Fatal("expected an error for a conflicting_field that doesn't match the fixture's real field")
	}
}

func TestELKFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.elk.logstash-pipeline-blocked"]; !ok {
		t.Fatal("expected f.elk.logstash-pipeline-blocked to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.elk.logstash-pipeline-blocked"]; ok {
		t.Fatal("f.elk.logstash-pipeline-blocked must not ALSO be registered in the typed registry")
	}
}
