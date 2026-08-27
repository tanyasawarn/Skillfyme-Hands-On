package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// TestIstioFixtureAndFaults_LiveIntegration is real-infra-gated and
// heavier than most of this package's other live tests -- a real
// istiod control plane install (once per cluster, ~1-2 minutes the
// first time) plus real sidecar injection. Both fault subtests directly
// reproduce faultinjection.applyIstioMTLSModeMismatch /
// applyIstioVirtualServiceWeightSumInvalid's own real mutations rather
// than cross-importing faultinjection (import cycle -- same convention
// as every other live fault test in this package), asserting the SAME
// observable outcomes those handlers verify.
//
// Both faults are T2-gated in their real content YAMLs; this test
// exercises the K8s-mutation layer directly against a T1-shaped test
// namespace, which is exactly what M3's plan calls for given
// Provision(T2) itself fails at K8s admission in this environment (see
// internal/k8s/provision_t2_live_test.go) -- the RPC-level tier gate
// (server.go's InjectFault, proven separately) is what actually
// prevents these from running for real learners, not a gap in the
// underlying mutation logic this test verifies.
func TestIstioFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-istio-test-" + envID[:8]

	clientset := provisioner.Clientset()
	// No PodSecurity "restricted" enforcement -- Istio's own sidecar
	// injection needs elevated init-time privilege in this profile,
	// same documented, scoped exception as this session's other
	// root-requiring-init fixtures (Ansible, Jenkins, Gitea).
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyIstioMinimal(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyIstioMinimal failed: %v", err)
	}

	callerPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+istioCallerName)
	if err != nil {
		t.Fatalf("finding practice-caller pod: %v", err)
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}

	t.Run("f.istio.mtls-mode-mismatch: a real STRICT/DISABLE conflict genuinely breaks a real call", func(t *testing.T) {
		strictPeerAuth := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "security.istio.io/v1",
			"kind":       "PeerAuthentication",
			"metadata":   map[string]any{"name": "practice-strict-test", "namespace": ns},
			"spec": map[string]any{
				"mtls": map[string]any{"mode": "STRICT"},
			},
		}}
		if _, err := dyn.Resource(istioPeerAuthGVR).Namespace(ns).Create(ctx, strictPeerAuth, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating STRICT PeerAuthentication: %v", err)
		}

		dr, err := dyn.Resource(istioDestRuleGVR).Namespace(ns).Get(ctx, istioDRName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting destinationrule: %v", err)
		}
		if err := unstructured.SetNestedMap(dr.Object, map[string]any{"tls": map[string]any{"mode": "DISABLE"}}, "spec", "trafficPolicy"); err != nil {
			t.Fatalf("setting trafficPolicy.tls.mode: %v", err)
		}
		if _, err := dyn.Resource(istioDestRuleGVR).Namespace(ns).Update(ctx, dr, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating destinationrule: %v", err)
		}

		time.Sleep(5 * time.Second)

		result, err := k8s.ExecInPod(ctx, provisioner, ns, callerPod, "app",
			"wget -q -T 5 -O- http://"+istioSvcName+":80/ 2>&1; echo EXIT:$?", 15*time.Second)
		if err != nil {
			t.Fatalf("verifying mTLS mismatch: %v", err)
		}
		if strings.Contains(result.Stdout, "EXIT:0") {
			t.Fatal("REGRESSION: expected the mTLS mode conflict to break the real call, but it succeeded")
		}
		if !strings.Contains(result.Stdout, "503") {
			t.Fatalf("expected a real 503 from the caller's own sidecar, got: %s", result.Stdout)
		}
	})

	t.Run("f.istio.virtualservice-weight-sum-invalid: a zero-weight route is a real, verifiable rejection case", func(t *testing.T) {
		vs, err := dyn.Resource(istioVSGVR).Namespace(ns).Get(ctx, istioVSName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting virtualservice: %v", err)
		}
		// spec.http is a slice; its path segments in NestedSlice/
		// SetNestedSlice are all treated as MAP KEYS, not array indices
		// -- must index into the slice element directly.
		httpRules, found, err := unstructured.NestedSlice(vs.Object, "spec", "http")
		if err != nil || !found || len(httpRules) == 0 {
			t.Fatalf("reading spec.http: err=%v found=%v len=%d", err, found, len(httpRules))
		}
		httpRule, ok := httpRules[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected spec.http[0] shape: %T", httpRules[0])
		}
		routes, found, err := unstructured.NestedSlice(httpRule, "route")
		if err != nil || !found {
			t.Fatalf("reading routes: err=%v found=%v", err, found)
		}
		for i := range routes {
			route, ok := routes[i].(map[string]any)
			if !ok {
				continue
			}
			route["weight"] = int64(0)
			routes[i] = route
		}
		if err := unstructured.SetNestedSlice(httpRule, routes, "route"); err != nil {
			t.Fatalf("setting zero weight: %v", err)
		}
		httpRules[0] = httpRule
		if err := unstructured.SetNestedSlice(vs.Object, httpRules, "spec", "http"); err != nil {
			t.Fatalf("writing back spec.http: %v", err)
		}
		if _, err := dyn.Resource(istioVSGVR).Namespace(ns).Update(ctx, vs, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating virtualservice: %v", err)
		}

		verify, err := dyn.Resource(istioVSGVR).Namespace(ns).Get(ctx, istioVSName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("re-reading virtualservice: %v", err)
		}
		var verifyRoutes []any
		if verifyHTTPRules, found, _ := unstructured.NestedSlice(verify.Object, "spec", "http"); found && len(verifyHTTPRules) > 0 {
			if verifyHTTPRule, ok := verifyHTTPRules[0].(map[string]any); ok {
				verifyRoutes, _, _ = unstructured.NestedSlice(verifyHTTPRule, "route")
			}
		}
		if len(verifyRoutes) == 0 {
			t.Fatal("expected at least one route")
		}
		route, ok := verifyRoutes[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected route shape: %+v", verifyRoutes[0])
		}
		if w, _ := route["weight"].(int64); w != 0 {
			t.Fatalf("expected weight=0 to have been genuinely applied, got: %v", route["weight"])
		}
	})
}
