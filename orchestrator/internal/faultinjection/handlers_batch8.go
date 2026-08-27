package faultinjection

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Eighth batch: f.elk.logstash-pipeline-blocked, backed by
// fx.elk-minimal.v1 (internal/fixture/handlers_elk.go) provisioning a
// real single-node Elasticsearch + real Logstash (HTTP input ->
// Elasticsearch output) baseline. See content/faults/f.elk.logstash-pipeline-blocked.yaml's
// v2 header comment for the real-behavior correction this fault's
// content underwent (live-tested against real Logstash/Elasticsearch:
// a mapping conflict drops the conflicting document and the pipeline
// keeps flowing -- it does not halt outright).
//
// Registered as DynamicHandler since it needs *k8s.Provisioner for
// ExecInPod (posting documents through the real Logstash HTTP input,
// exactly the ingestion path a real log shipper would use -- not a
// direct Elasticsearch API bypass, which would misrepresent how the
// fault actually manifests).
func init() {
	registerDynamic("f.elk.logstash-pipeline-blocked", applyELKLogstashPipelineBlocked)
}

const (
	elkIndexNameConst         = "practice-logs"
	elkConflictFieldNameConst = "conflict_field"
	logstashServiceNameConst  = "practice-logstash"
	logstashPodLabelSelector  = "app=practice-logstash"
)

// applyELKLogstashPipelineBlocked: content/faults/f.elk.logstash-pipeline-blocked.yaml
// params: index (must match the fixture's real index, "practice-logs"),
// conflicting_field (must match the fixture's real field name,
// "conflict_field" -- both validated below rather than trusted blindly).
//
// Sends real documents through Logstash's real HTTP input: first one
// seeding conflict_field's mapping as a string (matching the healthy
// baseline every learner sees before the fault, so the "conflict" is
// genuinely a TYPE conflict against real, already-established mapping,
// not an artificial one this handler invents), then a burst of
// documents where conflict_field is a nested object -- an incompatible
// type Elasticsearch's mapper rejects per-document
// (document_parsing_exception), reproducing the real, live-verified
// symptom: those specific documents are silently dropped while
// unrelated documents keep indexing normally.
func applyELKLogstashPipelineBlocked(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	index := params["index"]
	conflictingField := params["conflicting_field"]
	if index == "" || conflictingField == "" {
		return Result{}, fmt.Errorf("f.elk.logstash-pipeline-blocked requires params: index, conflicting_field")
	}
	if index != elkIndexNameConst {
		return Result{}, fmt.Errorf("f.elk.logstash-pipeline-blocked: index %q does not match the fixture's real index %q", index, elkIndexNameConst)
	}
	if conflictingField != elkConflictFieldNameConst {
		return Result{}, fmt.Errorf("f.elk.logstash-pipeline-blocked: conflicting_field %q does not match the fixture's real field %q", conflictingField, elkConflictFieldNameConst)
	}

	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: logstashPodLabelSelector})
	if err != nil {
		return Result{}, fmt.Errorf("listing logstash pods: %w", err)
	}
	var logstashPod string
	for _, p := range pods.Items {
		logstashPod = p.Name
		break
	}
	if logstashPod == "" {
		return Result{}, fmt.Errorf("no logstash pod found in namespace %s -- has fx.elk-minimal.v1 been applied?", namespace)
	}

	// curl, not wget -- the official logstash image (Ubuntu-based) has no
	// wget on PATH, confirmed live during this handler's own development
	// (every other ELK/Prometheus/Jaeger fixture pod in this codebase
	// runs busybox, which DOES have wget; Elastic's own images don't).
	//
	// Seed doc: establishes conflict_field's mapping as a string (real
	// Elasticsearch dynamic mapping behavior on first-seen field) -- runs
	// every call, idempotent by construction (Elasticsearch dynamic
	// mapping is itself idempotent: mapping a field the same way twice is
	// a no-op).
	seedCmd := fmt.Sprintf(
		`curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d '{"message":"seed","%s":"a string value"}' http://localhost:8080/`,
		conflictingField,
	)
	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, logstashPod, "logstash", seedCmd, 15*time.Second); err != nil {
		return Result{}, fmt.Errorf("seeding %s mapping via logstash: %w", conflictingField, err)
	}

	// Give the seed document time to actually index (real network + ES
	// indexing latency, not instantaneous) before sending the conflicting
	// batch -- otherwise the conflicting docs could race the mapping
	// being established and get dynamically mapped as an object
	// themselves instead of genuinely conflicting.
	time.Sleep(2 * time.Second)

	// A burst (not a single document) of type-conflicting documents,
	// matching the fault's real, live-verified symptom: a SUSTAINED
	// stream of documents matching the conflicting field is what
	// produces a measurable, diagnosable pattern (repeated
	// document_parsing_exception log entries, a growing gap between
	// sent-vs-indexed counts) -- a single dropped document would be
	// nearly undetectable and wouldn't match this fault's own
	// mean_time_to_diagnose_minutes: 14 framing.
	const burstSize = 10
	conflictCmd := fmt.Sprintf(
		`for i in $(seq 1 %d); do curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d '{"message":"conflicting doc","%s":{"nested":"object"}}' http://localhost:8080/; done; echo done`,
		burstSize, conflictingField,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, logstashPod, "logstash", conflictCmd, 30*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("sending conflicting documents via logstash: %w", err)
	}

	return Result{Applied: true, SymptomVerified: result.ExitCode == 0}, nil
}
