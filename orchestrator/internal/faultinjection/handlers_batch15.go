package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Fifteenth batch: the two non-AWS T2-gated Istio faults, both backed
// by fx.istio-minimal.v1 (internal/fixture/handlers_istio.go) -- a real
// istiod control plane + real sidecar-injected workloads. Both faults
// are min_tier: T2_ISOLATED_MICROVM in their content YAML; the RPC-level
// tier-precondition check (server.go's InjectFault, gated by
// faultinjection.RequiresT2) is what actually enforces that against
// real callers -- these handlers themselves assume a T2-shaped caller
// already passed that gate, matching every other handler in this
// package's own convention of not re-deriving checks the RPC layer
// already owns.
func init() {
	registerDynamic("f.istio.mtls-mode-mismatch", applyIstioMTLSModeMismatch)
	registerDynamic("f.istio.virtualservice-weight-sum-invalid", applyIstioVirtualServiceWeightSumInvalid)
}

const istioCallerPodLabelSelector = "app=practice-caller"

// istioSvcName/istioDRName/istioVSName mirror
// internal/fixture/handlers_istio.go's own constants of the same name --
// duplicated here (not imported; fixture's are unexported) following
// this codebase's existing cross-package pattern (see
// helmReleaseNameConst in handlers_batch10.go).
const (
	istioSvcNameConst = "practice-svc"
	istioDRNameConst  = "practice-dr"
	istioVSNameConst  = "practice-vs"
)

var (
	istioDestRuleGVRFault = schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"}
	istioVSGVRFault       = schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	istioPeerAuthGVRFault = schema.GroupVersionResource{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"}
)

// applyIstioMTLSModeMismatch: content/faults/f.istio.mtls-mode-mismatch.yaml
// (v2) params: destinationrule (must be "practice-dr", the fixture's
// real DestinationRule).
//
// Creates a real STRICT PeerAuthentication for the namespace, then
// patches the fixture's real DestinationRule to trafficPolicy.tls.mode:
// DISABLE -- live-verified (this session, real istiod + real
// sidecar-injected pods): a real HTTP call from practice-caller to
// practice-svc, healthy before this mutation, genuinely fails afterward
// with a clean 503 from the caller's own Envoy sidecar (NOT a raw
// connection reset -- v1's content wrongly claimed that; see the
// content YAML's own v2 doc comment for the full re-scope reasoning).
func applyIstioMTLSModeMismatch(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	destinationRule := params["destinationrule"]
	if destinationRule == "" {
		return Result{}, fmt.Errorf("f.istio.mtls-mode-mismatch requires param: destinationrule")
	}
	if destinationRule != istioDRNameConst {
		return Result{}, fmt.Errorf("f.istio.mtls-mode-mismatch: destinationrule %q does not match the fixture's real destinationrule %q", destinationRule, istioDRNameConst)
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		return Result{}, fmt.Errorf("building dynamic client: %w", err)
	}

	strictPeerAuth := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "security.istio.io/v1",
		"kind":       "PeerAuthentication",
		"metadata":   map[string]any{"name": "practice-strict", "namespace": namespace},
		"spec": map[string]any{
			"mtls": map[string]any{"mode": "STRICT"},
		},
	}}
	if _, err := dyn.Resource(istioPeerAuthGVRFault).Namespace(namespace).Create(ctx, strictPeerAuth, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return Result{}, fmt.Errorf("creating STRICT PeerAuthentication: %w", err)
	}

	dr, err := dyn.Resource(istioDestRuleGVRFault).Namespace(namespace).Get(ctx, destinationRule, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("getting destinationrule %s: %w", destinationRule, err)
	}
	if err := unstructured.SetNestedMap(dr.Object, map[string]any{"tls": map[string]any{"mode": "DISABLE"}}, "spec", "trafficPolicy"); err != nil {
		return Result{}, fmt.Errorf("setting trafficPolicy.tls.mode: %w", err)
	}
	if _, err := dyn.Resource(istioDestRuleGVRFault).Namespace(namespace).Update(ctx, dr, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating destinationrule %s: %w", destinationRule, err)
	}

	// Give istiod's xDS push a moment to propagate the new config to
	// both sidecars before verifying -- confirmed live as a real,
	// reproducible race without this margin during this handler's own
	// build.
	time.Sleep(5 * time.Second)

	callerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, istioCallerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding practice-caller pod: %w", err)
	}
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, callerPod, "app",
		fmt.Sprintf("wget -q -T 5 -O- http://%s:80/ 2>&1; echo EXIT:$?", istioSvcNameConst), 15*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("verifying mTLS mismatch symptom: %w", err)
	}

	verified := !strings.Contains(result.Stdout, "EXIT:0")
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyIstioVirtualServiceWeightSumInvalid: content/faults/f.istio.virtualservice-weight-sum-invalid.yaml
// (v2) params: virtualservice (must be "practice-vs", the fixture's
// real VirtualService), weight_a/weight_b (accepted for the content
// author's own reference -- this fault always sets BOTH real route
// weights to 0 regardless of the values passed, since that's the one
// genuinely real Istio rejection, confirmed live -- see the content
// YAML's own v2 doc comment for why "doesn't sum to 100" was re-scoped
// away from; a nonzero-but-non-100 sum is valid modern Istio behavior).
//
// Patches the fixture's real VirtualService so its route weight is 0,
// which Istio's own real schema validation rejects (error IST0106:
// "total destination weight = 0", confirmed live via istioctl analyze
// against this exact object).
func applyIstioVirtualServiceWeightSumInvalid(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	virtualService := params["virtualservice"]
	if virtualService == "" {
		return Result{}, fmt.Errorf("f.istio.virtualservice-weight-sum-invalid requires param: virtualservice")
	}
	if virtualService != istioVSNameConst {
		return Result{}, fmt.Errorf("f.istio.virtualservice-weight-sum-invalid: virtualservice %q does not match the fixture's real virtualservice %q", virtualService, istioVSNameConst)
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		return Result{}, fmt.Errorf("building dynamic client: %w", err)
	}

	vs, err := dyn.Resource(istioVSGVRFault).Namespace(namespace).Get(ctx, virtualService, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("getting virtualservice %s: %w", virtualService, err)
	}

	// spec.http is itself a slice, so its first element ("the one HTTP
	// route rule") must be reached by indexing into the slice directly
	// -- unstructured.NestedSlice's path segments are all treated as MAP
	// KEYS, not array indices, confirmed live as a real bug in this
	// handler's first version ("spec", "http", "0", "route" does not
	// mean "index 0 of the http slice").
	httpRules, found, err := unstructured.NestedSlice(vs.Object, "spec", "http")
	if err != nil || !found || len(httpRules) == 0 {
		return Result{}, fmt.Errorf("reading spec.http: err=%v found=%v len=%d", err, found, len(httpRules))
	}
	httpRule, ok := httpRules[0].(map[string]any)
	if !ok {
		return Result{}, fmt.Errorf("unexpected spec.http[0] shape: %T", httpRules[0])
	}
	routes, found, err := unstructured.NestedSlice(httpRule, "route")
	if err != nil || !found {
		return Result{}, fmt.Errorf("reading existing route weights: err=%v found=%v", err, found)
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
		return Result{}, fmt.Errorf("setting zero weights: %w", err)
	}
	httpRules[0] = httpRule
	if err := unstructured.SetNestedSlice(vs.Object, httpRules, "spec", "http"); err != nil {
		return Result{}, fmt.Errorf("writing back spec.http: %w", err)
	}

	_, updateErr := dyn.Resource(istioVSGVRFault).Namespace(namespace).Update(ctx, vs, metav1.UpdateOptions{})
	// A real, live-verified Istio behavior this handler's own
	// verification relies on: K8s admission accepts the object (the CRD
	// schema alone does not enforce weight-sum semantics -- confirmed
	// live), so a successful Update here is expected, not an error; the
	// real rejection surfaces via istioctl analyze / the validating
	// webhook when one is registered, not plain kubectl apply/update.
	if updateErr != nil {
		return Result{}, fmt.Errorf("updating virtualservice %s: %w", virtualService, updateErr)
	}

	verifyResult, err := dyn.Resource(istioVSGVRFault).Namespace(namespace).Get(ctx, virtualService, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("re-reading virtualservice for verification: %w", err)
	}
	var verifyRoutes []any
	if verifyHTTPRules, found, _ := unstructured.NestedSlice(verifyResult.Object, "spec", "http"); found && len(verifyHTTPRules) > 0 {
		if verifyHTTPRule, ok := verifyHTTPRules[0].(map[string]any); ok {
			verifyRoutes, _, _ = unstructured.NestedSlice(verifyHTTPRule, "route")
		}
	}
	verified := len(verifyRoutes) > 0
	for _, r := range verifyRoutes {
		route, ok := r.(map[string]any)
		if !ok {
			verified = false
			break
		}
		w, _ := route["weight"].(int64)
		if w != 0 {
			verified = false
		}
	}
	return Result{Applied: true, SymptomVerified: verified}, nil
}
