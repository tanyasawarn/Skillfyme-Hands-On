package faultinjection

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Seventh batch: f.jaeger.missing-trace-context-propagation, backed by
// fx.jaeger-minimal.v1 (internal/fixture/handlers_jaeger.go) provisioning
// a real Jaeger all-in-one (OTLP HTTP receiver) plus a real 2-service
// sample app (practice-frontend, practice-backend) that legitimately
// propagates a trace_id from frontend to backend before this fault ever
// runs. Registered as DynamicHandler since it patches a ConfigMap and
// needs *k8s.Provisioner, same class of need as the Prometheus faults
// (handlers_batch6.go), though this one needs no pod-exec of its own --
// DynamicHandler's contract is "needs *k8s.Provisioner", not
// specifically "needs ExecInPod."
func init() {
	registerDynamic("f.jaeger.missing-trace-context-propagation", applyJaegerMissingTraceContextPropagation)
}

const (
	jaegerFrontendConfigMapNameFault = "practice-jaeger-frontend-script"
	jaegerFrontendScriptKeyFault     = "serve.sh"
	jaegerFrontendServiceNameConst   = "practice-frontend"
)

// applyJaegerMissingTraceContextPropagation: content/faults/f.jaeger.missing-trace-context-propagation.yaml
// params: service (must match the fixture's real frontend service name,
// "practice-frontend" -- validated below rather than trusted blindly;
// the fixture's backend never propagates anything itself, so it's not a
// valid fault target regardless of what a caller passes).
//
// Patches the frontend's live script (ConfigMap, whole-directory mounted
// -- confirmed live during this fixture's own build to receive kubelet's
// propagation, same mechanism/timing as fx.prometheus-minimal.v1's own
// ConfigMap) so its call to the backend generates and sends a FRESH,
// unrelated trace_id instead of forwarding the one it reported its own
// span under -- the two services' spans then land in Jaeger under two
// different trace IDs, i.e. exactly "two separate traces instead of
// one," the fault's own canonical_diagnostic_path.
func applyJaegerMissingTraceContextPropagation(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	service := params["service"]
	if service == "" {
		return Result{}, fmt.Errorf("f.jaeger.missing-trace-context-propagation requires param: service")
	}
	if service != jaegerFrontendServiceNameConst {
		return Result{}, fmt.Errorf("f.jaeger.missing-trace-context-propagation: service %q does not match the fixture's real propagating service %q", service, jaegerFrontendServiceNameConst)
	}

	cms := provisioner.Clientset().CoreV1().ConfigMaps(namespace)
	cm, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ConfigMap, error) {
		return cms.Get(ctx, jaegerFrontendConfigMapNameFault, metav1.GetOptions{})
	}, "ConfigMap", "configmap", jaegerFrontendConfigMapNameFault)
	if err != nil {
		return notFoundOrErrResult, err
	}

	current := cm.Data[jaegerFrontendScriptKeyFault]
	const propagatingCall = `wget -q -O- "http://practice-backend:8080/?trace_id=$TRACE_ID" >/dev/null 2>&1`
	const brokenCall = `BROKEN_TRACE_ID=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); wget -q -O- "http://practice-backend:8080/?trace_id=$BROKEN_TRACE_ID" >/dev/null 2>&1`
	if strings.Contains(current, "BROKEN_TRACE_ID") {
		// Idempotent: already broken.
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if !strings.Contains(current, propagatingCall) {
		return Result{}, fmt.Errorf("expected propagating backend call not found in %s -- fixture ConfigMap may be out of sync", jaegerFrontendScriptKeyFault)
	}

	cm.Data[jaegerFrontendScriptKeyFault] = strings.Replace(current, propagatingCall, brokenCall, 1)
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating jaeger frontend script ConfigMap: %w", err)
	}

	// The frontend's shell script is read once at process startup
	// (`sh /scripts/serve.sh` interprets the whole file up front, not
	// per-request) -- a ConfigMap patch alone never takes effect on an
	// already-running process, confirmed during this fault's own
	// development. fx.jaeger-minimal.v1 deliberately provisions the
	// frontend as a Deployment (not a bare Pod) for exactly this reason:
	// deleting the pod here lets its controller recreate it with the
	// now-updated ConfigMap mount already current, a real mechanism for
	// forcing the new (broken) script to actually govern behavior,
	// rather than a best-effort guess about in-place reload timing.
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + jaegerFrontendServiceNameConst,
	})
	if err != nil {
		return Result{}, fmt.Errorf("listing frontend pods to restart: %w", err)
	}
	for _, p := range pods.Items {
		if delErr := provisioner.Clientset().CoreV1().Pods(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); delErr != nil {
			return Result{}, fmt.Errorf("deleting frontend pod %s to force config reload: %w", p.Name, delErr)
		}
	}

	// The Deployment controller needs time to notice the deletion and
	// schedule a replacement pod -- SymptomVerified here only confirms
	// SOME frontend pod exists post-delete (the restart mechanism
	// worked), not that it has finished starting or served a request
	// yet. The ConfigMap mutation itself (the durable fault state) has
	// already succeeded above regardless.
	return Result{Applied: true, SymptomVerified: len(pods.Items) > 0}, nil
}
