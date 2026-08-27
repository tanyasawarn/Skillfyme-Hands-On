package fixture

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func waitForAllPodsReady(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace string, minCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err == nil && len(pods.Items) >= minCount {
			allReady := true
			for _, p := range pods.Items {
				ready := false
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						ready = true
					}
				}
				if !ready {
					allReady = false
					break
				}
			}
			if allReady {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("pods in namespace %s did not all become Ready within %s", namespace, timeout)
}

func jaegerPodName(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace string) string {
	t.Helper()
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + jaegerDeploymentName})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("finding jaeger pod: %v", err)
	}
	return pods.Items[0].Name
}

// jaegerServicesResponse/jaegerTraceResponse mirror the two Jaeger query
// API shapes this test needs.
type jaegerServicesResponse struct {
	Data []string `json:"data"`
}
type jaegerTraceResponse struct {
	Data []struct {
		TraceID string `json:"traceID"`
		Spans   []struct {
			ProcessID string `json:"processID"`
		} `json:"spans"`
		Processes map[string]struct {
			ServiceName string `json:"serviceName"`
		} `json:"processes"`
	} `json:"data"`
}

func queryJaegerAPI(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, jaegerPod, path string, out any) {
	t.Helper()
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, jaegerPod, "jaeger", "wget -q -O- 'http://localhost:16686"+path+"'", 15*time.Second)
	if err != nil {
		t.Fatalf("querying jaeger API %s: %v", path, err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("jaeger API query %s failed (exit %d): %s", path, result.ExitCode, result.Stderr)
	}
	if err := json.Unmarshal([]byte(result.Stdout), out); err != nil {
		t.Fatalf("decoding jaeger API response from %s: %v\nraw: %s", path, err, result.Stdout)
	}
}

func TestJaegerFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-jaeger-test-" + envID[:8]

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

	if err := applyJaegerMinimal(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyJaegerMinimal failed: %v", err)
	}

	waitForAllPodsReady(t, ctx, provisioner, ns, 3, 90*time.Second)
	jaegerPod := jaegerPodName(t, ctx, provisioner, ns)

	t.Run("healthy baseline: frontend call produces one connected trace across both services", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, jaegerPod, "jaeger", "wget -q -O- http://practice-frontend:8080/", 15*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("calling frontend: err=%v result=%+v", err, result)
		}

		time.Sleep(3 * time.Second)
		var services jaegerServicesResponse
		queryJaegerAPI(t, ctx, provisioner, ns, jaegerPod, "/api/services", &services)
		hasFrontend, hasBackend := false, false
		for _, s := range services.Data {
			if s == jaegerFrontendServiceName {
				hasFrontend = true
			}
			if s == jaegerBackendServiceName {
				hasBackend = true
			}
		}
		if !hasFrontend || !hasBackend {
			t.Fatalf("expected both practice-frontend and practice-backend to have reported spans, got services: %v", services.Data)
		}

		// The real proof of propagation: query the frontend's most
		// recent trace and confirm the backend's span landed under the
		// SAME trace ID (both processes referenced in one trace).
		var frontendTraces jaegerTraceResponse
		queryJaegerAPI(t, ctx, provisioner, ns, jaegerPod, "/api/traces?service="+jaegerFrontendServiceName+"&limit=1", &frontendTraces)
		if len(frontendTraces.Data) == 0 {
			t.Fatal("expected at least one trace for practice-frontend")
		}
		trace := frontendTraces.Data[0]
		serviceNames := map[string]bool{}
		for _, proc := range trace.Processes {
			serviceNames[proc.ServiceName] = true
		}
		if !serviceNames[jaegerFrontendServiceName] || !serviceNames[jaegerBackendServiceName] {
			t.Fatalf("expected ONE trace containing both frontend and backend spans (propagation working), got processes: %v", serviceNames)
		}
	})

	t.Run("f.jaeger.missing-trace-context-propagation breaks propagation: frontend and backend spans land in different traces", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, jaegerFrontendConfigMapName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading frontend script ConfigMap: %v", err)
		}
		const propagatingCall = `wget -q -O- "http://practice-backend:8080/?trace_id=$TRACE_ID" >/dev/null 2>&1`
		const brokenCall = `BROKEN_TRACE_ID=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); wget -q -O- "http://practice-backend:8080/?trace_id=$BROKEN_TRACE_ID" >/dev/null 2>&1`
		script := cm.Data[jaegerFrontendScriptKey]
		if !strings.Contains(script, propagatingCall) {
			t.Fatalf("fixture's frontend script does not contain the expected propagating call -- fixture/fault contract drift:\n%s", script)
		}
		cm.Data[jaegerFrontendScriptKey] = strings.Replace(script, propagatingCall, brokenCall, 1)
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating frontend ConfigMap: %v", err)
		}

		// Force the config to actually take effect: delete the frontend
		// pod so its Deployment recreates it with the patched script
		// (matches the real fault handler's own mechanism).
		pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + jaegerFrontendServiceName})
		if err != nil {
			t.Fatalf("listing frontend pods: %v", err)
		}
		for _, p := range pods.Items {
			if err := clientset.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
				t.Fatalf("deleting frontend pod: %v", err)
			}
		}
		waitForAllPodsReady(t, ctx, provisioner, ns, 3, 90*time.Second)

		result, err := k8s.ExecInPod(ctx, provisioner, ns, jaegerPod, "jaeger", "wget -q -O- http://practice-frontend:8080/", 15*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("calling frontend after fault: err=%v result=%+v", err, result)
		}
		time.Sleep(3 * time.Second)

		var frontendTraces jaegerTraceResponse
		queryJaegerAPI(t, ctx, provisioner, ns, jaegerPod, "/api/traces?service="+jaegerFrontendServiceName+"&limit=1", &frontendTraces)
		if len(frontendTraces.Data) == 0 {
			t.Fatal("expected at least one trace for practice-frontend after the fault")
		}
		trace := frontendTraces.Data[0]
		serviceNames := map[string]bool{}
		for _, proc := range trace.Processes {
			serviceNames[proc.ServiceName] = true
		}
		if serviceNames[jaegerBackendServiceName] {
			t.Fatalf("SECURITY/CORRECTNESS REGRESSION: expected the frontend's trace to NOT include the backend after the fault (propagation should be broken), but it does: %v", serviceNames)
		}
		if !serviceNames[jaegerFrontendServiceName] {
			t.Fatalf("expected the frontend's own trace to still contain the frontend's own span, got: %v", serviceNames)
		}
	})
}
