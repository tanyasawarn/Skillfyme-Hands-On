package fixture

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func elkPodName(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, appLabel string) string {
	t.Helper()
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + appLabel})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("finding pod with app=%s: %v", appLabel, err)
	}
	return pods.Items[0].Name
}

type esCountResponse struct {
	Count int `json:"count"`
}

func esCount(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, esPod, index string) int {
	t.Helper()
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, esPod, "elasticsearch",
		"curl -s http://localhost:9200/"+index+"/_count", 15*time.Second)
	if err != nil {
		t.Fatalf("querying ES count: %v", err)
	}
	var resp esCountResponse
	if err := json.Unmarshal([]byte(result.Stdout), &resp); err != nil {
		t.Fatalf("decoding ES count response: %v\nraw: %s", err, result.Stdout)
	}
	return resp.Count
}

// waitForESCount polls until index's document count reaches at least
// want, or timeout elapses -- a fixed sleep isn't reliable here: even
// once Logstash's own readiness probe (port 9600) passes, its
// Elasticsearch OUTPUT plugin's connection pool can still be warming up
// separately (confirmed live during this test's own development: a
// document posted right after Ready sometimes doesn't appear for
// several more seconds, well past any single fixed sleep chosen in
// advance).
func waitForESCount(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, esPod, index string, want int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int
	for time.Now().Before(deadline) {
		last = esCount(t, ctx, provisioner, namespace, esPod, index)
		if last >= want {
			return last
		}
		time.Sleep(2 * time.Second)
	}
	return last
}

func TestELKFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-elk-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyELKMinimal(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyELKMinimal failed: %v", err)
	}

	waitForAllPodsReady(t, ctx, provisioner, ns, 2, 180*time.Second)
	logstashPod := elkPodName(t, ctx, provisioner, ns, logstashDeploymentName)
	esPod := elkPodName(t, ctx, provisioner, ns, elasticsearchDeploymentName)

	t.Run("healthy baseline: a real document posted to Logstash is indexed in Elasticsearch", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, logstashPod, "logstash",
			`curl -s -o /dev/null -w "%{http_code}" -X POST -H "Content-Type: application/json" -d '{"message":"healthy baseline doc"}' http://localhost:8080/`,
			15*time.Second)
		if err != nil {
			t.Fatalf("posting doc to logstash: %v", err)
		}
		if result.Stdout != "200" {
			t.Fatalf("expected HTTP 200 from logstash HTTP input, got %q (stderr: %s)", result.Stdout, result.Stderr)
		}

		count := waitForESCount(t, ctx, provisioner, ns, esPod, elkIndexName, 1, 60*time.Second)
		if count < 1 {
			t.Fatalf("expected at least 1 document indexed in %s within 60s, got %d", elkIndexName, count)
		}
	})

	t.Run("f.elk.logstash-pipeline-blocked: conflicting documents are dropped while unrelated documents keep flowing", func(t *testing.T) {
		baselineCount := esCount(t, ctx, provisioner, ns, esPod, elkIndexName)

		// Seed the conflict_field mapping as a string (same mechanism the
		// real fault handler uses).
		seedResult, err := k8s.ExecInPod(ctx, provisioner, ns, logstashPod, "logstash",
			`curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d '{"message":"seed","conflict_field":"a string value"}' http://localhost:8080/`,
			15*time.Second)
		if err != nil {
			t.Fatalf("seeding mapping: %v (stderr: %s)", err, seedResult.Stderr)
		}
		afterSeedCount := waitForESCount(t, ctx, provisioner, ns, esPod, elkIndexName, baselineCount+1, 30*time.Second)
		if afterSeedCount != baselineCount+1 {
			t.Fatalf("expected the seed doc itself to index successfully (count %d -> %d) within 30s, got %d", baselineCount, baselineCount+1, afterSeedCount)
		}

		// Burst of genuinely type-conflicting documents (object where the
		// mapping expects a string) -- real Elasticsearch mapper
		// rejection, not a simulated one.
		const burstSize = 10
		burstResult, err := k8s.ExecInPod(ctx, provisioner, ns, logstashPod, "logstash",
			`for i in $(seq 1 10); do curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d '{"message":"conflicting doc","conflict_field":{"nested":"object"}}' http://localhost:8080/; done; echo done`,
			30*time.Second)
		if err != nil {
			t.Fatalf("sending conflicting burst: %v (stderr: %s)", err, burstResult.Stderr)
		}

		// No growth is expected -- can't "wait for" an absence of change,
		// so this polls for a while and then confirms the count settled
		// and stayed at afterSeedCount (each conflicting doc is rejected
		// synchronously by Logstash's own HTTP response, so by the time
		// the burst curl loop above returned, every one of the 10
		// requests already completed -- this settle window only accounts
		// for the OUTPUT side's async indexing of anything that DID get
		// accepted, not the input side).
		time.Sleep(5 * time.Second)
		afterBurstCount := esCount(t, ctx, provisioner, ns, esPod, elkIndexName)
		if afterBurstCount != afterSeedCount {
			t.Fatalf("expected all %d conflicting documents to be REJECTED (count unchanged at %d), but count became %d -- either the conflict didn't manifest or some conflicting docs were incorrectly accepted", burstSize, afterSeedCount, afterBurstCount)
		}

		// The real proof this isn't a full pipeline stall: an unrelated
		// document posted immediately after the conflicting burst must
		// still index successfully.
		unrelatedResult, err := k8s.ExecInPod(ctx, provisioner, ns, logstashPod, "logstash",
			`curl -s -o /dev/null -w "%{http_code}" -X POST -H "Content-Type: application/json" -d '{"message":"unrelated doc after the fault"}' http://localhost:8080/`,
			15*time.Second)
		if err != nil || unrelatedResult.Stdout != "200" {
			t.Fatalf("expected an unrelated document to still be accepted (HTTP 200) after the fault, got stdout=%q err=%v", unrelatedResult.Stdout, err)
		}
		finalCount := waitForESCount(t, ctx, provisioner, ns, esPod, elkIndexName, afterBurstCount+1, 30*time.Second)
		if finalCount != afterBurstCount+1 {
			t.Fatalf("CORRECTNESS REGRESSION: expected the unrelated document to index successfully (count %d -> %d) within 30s, proving the pipeline is NOT fully stalled, got %d", afterBurstCount, afterBurstCount+1, finalCount)
		}
	})
}
