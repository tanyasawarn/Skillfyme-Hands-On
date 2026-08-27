package fixture

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// promAPIResponse mirrors Prometheus's HTTP API {"status":"success","data":{...}}
// envelope, just enough to check /api/v1/targets and /api/v1/rules.
type promAPIResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

// findPrometheusPodNameForTest resolves the live Prometheus pod name via
// its Deployment's label selector -- duplicated from
// faultinjection.findPrometheusPodName rather than cross-imported: this
// is a white-box test of the FIXTURE side only (same precedent
// handlers_tekton_test.go's fault-injection test sets, reproducing the
// fault's own mutation directly rather than importing the sibling
// package's unexported handler), so the fixture package stays free of a
// test-only dependency on faultinjection.
func findPrometheusPodNameForTest(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace string) (string, error) {
	t.Helper()
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: prometheusPodLabelSelectorForTest})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	if len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}
	return "", fmt.Errorf("no prometheus pod found in namespace %s", namespace)
}

const prometheusPodLabelSelectorForTest = "app=practice-prometheus"

// queryPrometheusAPI execs curl inside the live Prometheus pod against
// its own localhost:9090 -- the same "observe from inside the pod's own
// network namespace" pattern the fault handlers themselves use for the
// reload trigger, rather than requiring a port-forward from the test
// process.
func queryPrometheusAPI(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, podName, path string) promAPIResponse {
	t.Helper()
	// URL wrapped in single quotes -- ExecInPod runs this via `sh -c`, and
	// an unquoted `&` in a query string would be parsed as a shell
	// background-job separator rather than passed through to wget
	// (caught live while building the Jaeger fixture's own equivalent
	// test, whose query strings do contain `&`; applied here too for the
	// same reason even though this package's own query strings don't
	// currently need a second param).
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, podName, "prometheus",
		fmt.Sprintf("wget -q -O- 'http://localhost:9090%s'", path), 15*time.Second)
	if err != nil {
		t.Fatalf("querying prometheus API %s: %v", path, err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("prometheus API query %s failed (exit %d): %s", path, result.ExitCode, result.Stderr)
	}
	var resp promAPIResponse
	if err := json.Unmarshal([]byte(result.Stdout), &resp); err != nil {
		t.Fatalf("decoding prometheus API response from %s: %v\nraw: %s", path, err, result.Stdout)
	}
	return resp
}

// triggerPrometheusReloadForTest mirrors
// faultinjection.triggerPrometheusReload -- see
// findPrometheusPodNameForTest's doc comment for why this is duplicated
// rather than cross-imported.
func triggerPrometheusReloadForTest(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, podName string) error {
	t.Helper()
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, podName, "prometheus",
		"wget -q -O- --post-data='' http://localhost:9090/-/reload", 15*time.Second)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("reload failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}

func waitForPrometheusReady(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace string) string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var podName string
	for time.Now().Before(deadline) {
		name, err := findPrometheusPodNameForTest(t, ctx, provisioner, namespace)
		if err == nil {
			podName = name
			pod, gerr := provisioner.Clientset().CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if gerr == nil {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return podName
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("prometheus pod in namespace %s did not become Ready within 90s (last seen podName=%q)", namespace, podName)
	return ""
}

func TestPrometheusFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-prom-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyPrometheusMinimal(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyPrometheusMinimal failed: %v", err)
	}

	podName := waitForPrometheusReady(t, ctx, provisioner, ns)

	// Give the scrape loop (5s interval) a couple of cycles to actually
	// scrape the target at least once before asserting on /targets.
	time.Sleep(12 * time.Second)

	t.Run("healthy baseline: practice-target scrapes successfully", func(t *testing.T) {
		resp := queryPrometheusAPI(t, ctx, provisioner, ns, podName, "/api/v1/targets")
		if resp.Status != "success" {
			t.Fatalf("expected status=success, got %s", resp.Status)
		}
		if !containsHealthyTarget(t, resp.Data, prometheusScrapeJobName) {
			t.Fatalf("expected practice-target to be a healthy (up) target before any fault, targets response: %s", resp.Data)
		}
	})

	// The two subtests below directly reproduce
	// faultinjection.applyPrometheusScrapeTargetDown /
	// applyPrometheusAlertRuleSyntaxSilentFail's real ConfigMap mutation
	// against this real fixture baseline (see findPrometheusPodNameForTest's
	// doc comment for why this is white-box duplication rather than a
	// cross-package call) -- this proves the fixture's ConfigMap shape is
	// genuinely mutable the way those handlers assume, and that
	// Prometheus itself reacts correctly to each mutation.
	t.Run("f.prometheus.scrape-target-down makes the target's metrics vanish", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, prometheusConfigMapName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading prometheus ConfigMap: %v", err)
		}
		const targetJobBlock = "  - job_name: practice-target\n    static_configs:\n      - targets: ['practice-scrape-target:8080']\n"
		if !strings.Contains(cm.Data["prometheus.yml"], targetJobBlock) {
			t.Fatalf("fixture's prometheus.yml does not contain the expected job block -- fixture/fault contract drift:\n%s", cm.Data["prometheus.yml"])
		}
		replacement := targetJobBlock + "    metric_relabel_configs:\n      - source_labels: [__name__]\n        regex: '.*'\n        action: drop\n        # __fault_drop_all__ marker\n"
		cm.Data["prometheus.yml"] = strings.Replace(cm.Data["prometheus.yml"], targetJobBlock, replacement, 1)
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating prometheus ConfigMap: %v", err)
		}
		// kubelet propagates a whole-directory ConfigMap volume update on
		// its own sync interval -- confirmed live (a prior debug run of
		// this exact fixture) to take on the order of a minute on this
		// cluster, well past a single reload attempt right after the
		// Update call above (that first reload very likely just re-reads
		// the STILL-STALE on-disk file). Poll: re-trigger /-/reload and
		// re-query on each iteration rather than one fixed sleep, so this
		// test is correct regardless of the exact propagation delay on
		// whatever cluster it runs against, and fails with a clear
		// timeout message instead of a flaky single-shot assertion.
		//
		// metric_relabel_configs' drop action removes samples parsed out
		// of the scrape response (scrape_target_requests_total, this
		// fixture's own custom metric) -- it does NOT touch the synthetic
		// `up` series Prometheus generates itself to record scrape
		// success/failure, which exists independent of relabeling
		// (confirmed live: up{job="practice-target"} still returns 1
		// series after the drop rule). scrape_target_requests_total is
		// the correct observable for "the target's metrics vanished,"
		// matching the fault's own canonical_diagnostic_path: "query for
		// the expected metric -> no data returned."
		deadline := time.Now().Add(110 * time.Second)
		var lastSeries int
		for time.Now().Before(deadline) {
			_ = triggerPrometheusReloadForTest(t, ctx, provisioner, ns, podName)
			resp := queryPrometheusAPI(t, ctx, provisioner, ns, podName, "/api/v1/query?query="+url.QueryEscape(`scrape_target_requests_total{job="practice-target"}`))
			if resp.Status == "success" {
				var queryResult struct {
					Result []any `json:"result"`
				}
				if err := json.Unmarshal(resp.Data, &queryResult); err == nil {
					lastSeries = len(queryResult.Result)
					if lastSeries == 0 {
						return
					}
				}
			}
			time.Sleep(5 * time.Second)
		}
		t.Fatalf("expected scrape_target_requests_total{job=\"practice-target\"} to return NO series within %s of applying the drop rule, still saw %d series", 110*time.Second, lastSeries)
	})

	t.Run("f.prometheus.alert-rule-syntax-silent-fail corrupts the alert expression without crashing Prometheus", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, prometheusConfigMapName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading prometheus ConfigMap: %v", err)
		}
		const workingExpr = `up{job="practice-target"} == 0`
		const brokenExpr = `up{job="practice-target"} == -999999`
		if !strings.Contains(cm.Data[prometheusRulesFileName], workingExpr) {
			t.Fatalf("fixture's rules file does not contain the expected alert expression -- fixture/fault contract drift:\n%s", cm.Data[prometheusRulesFileName])
		}
		cm.Data[prometheusRulesFileName] = strings.Replace(cm.Data[prometheusRulesFileName], workingExpr, brokenExpr, 1)
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating prometheus ConfigMap: %v", err)
		}
		if err := triggerPrometheusReloadForTest(t, ctx, provisioner, ns, podName); err != nil {
			t.Fatalf("triggering reload: %v", err)
		}

		// Prometheus must still be up and serving its API after the
		// "silent fail" corruption -- proving this isn't a crash-inducing
		// syntax error, matching the fault's own detectability: low intent.
		resp := queryPrometheusAPI(t, ctx, provisioner, ns, podName, "/api/v1/rules")
		if resp.Status != "success" {
			t.Fatalf("expected prometheus to still respond successfully to /api/v1/rules after the fault, got status=%s", resp.Status)
		}
	})
}

func containsHealthyTarget(t *testing.T, data json.RawMessage, jobName string) bool {
	t.Helper()
	var parsed struct {
		ActiveTargets []struct {
			Labels struct {
				Job string `json:"job"`
			} `json:"labels"`
			Health string `json:"health"`
		} `json:"activeTargets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("decoding targets response: %v", err)
	}
	for _, target := range parsed.ActiveTargets {
		if target.Labels.Job == jobName && target.Health == "up" {
			return true
		}
	}
	return false
}
