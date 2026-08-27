package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/clusterbootstrap"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/manifests"
)

func init() {
	register("fx.istio-minimal.v1", applyIstioMinimal)
	registerChecksum("fx.istio-minimal.v1", "v1")
}

const (
	istiodDeploymentCRDName = "peerauthentications.security.istio.io"
	istioSvcName            = "practice-svc"
	istioCallerName         = "practice-caller"
	istioDRName             = "practice-dr"
	istioVSName             = "practice-vs"
)

var (
	istioPeerAuthGVR = schema.GroupVersionResource{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"}
	istioDestRuleGVR = schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"}
	istioVSGVR       = schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
)

// applyIstioMinimal is fx.istio-minimal.v1: installs a real istiod
// control plane cluster-wide (profile=minimal, once per cluster --
// idempotent via clusterbootstrap.CRDInstalled's guard, same pattern as
// fx.tekton-pipeline.v1) via internal/manifests.IstioMinimalYAML (a
// generated, embedded `istioctl manifest generate` output -- see that
// file's own doc comment), enables real sidecar injection on this
// environment's own namespace, and deploys two real Istio-proxied
// workloads (practice-svc, practice-caller) plus a real, healthy
// DestinationRule and VirtualService baseline.
//
// Backs BOTH T2-gated Istio faults:
//   - f.istio.mtls-mode-mismatch: needs the REAL running data plane
//     (sidecar injection + istiod actually enforcing mTLS) -- confirmed
//     live this session that a real STRICT PeerAuthentication +
//     conflicting DestinationRule genuinely breaks a real HTTP call
//     between these two pods (a clean 503 from the caller's own
//     sidecar, not the raw "connection reset" v1's content wrongly
//     claimed -- see content/faults/f.istio.mtls-mode-mismatch.yaml's
//     own v2 doc comment for the full re-scope reasoning).
//   - f.istio.virtualservice-weight-sum-invalid: only needs the CRDs +
//     istioctl analyze (a static analyzer, no running data plane
//     required) -- confirmed live that modern Istio treats route
//     weights as relative proportions (60+60 is valid, NOT rejected),
//     and the one real rejection is a TOTAL weight of zero (real error
//     IST0106) -- see that fault's own v2 doc comment.
//
// Both faults are T2-gated (min_tier: T2_ISOLATED_MICROVM) and this
// session confirmed live that Provision(T2) itself fails at K8s
// admission in this environment (no Kata-capable node -- see
// internal/k8s/provision_t2_live_test.go). This fixture and its fault
// handlers are still built and live-verified for real, correct K8s
// mutations against a T1-shaped test namespace directly (bypassing the
// InjectFault RPC's own tier gate, which is proven separately and live)
// -- the RPC-level gate is what actually prevents these from running
// for real learners until a Kata-capable environment exists, not a gap
// in the handlers themselves.
func applyIstioMinimal(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	restConfig := provisioner.RestConfig()
	clientset := provisioner.Clientset()

	// The istio-proxy sidecar needs to reach istiod's xDS/gRPC service
	// in a DIFFERENT namespace (istio-system) -- confirmed live as a
	// real bug: under the real T1 default-deny NetworkPolicy baseline
	// (applyRealT1NetworkBaseline in tests; Provisioner.Provision for
	// real environments), this fixture's own intra-namespace-only allow
	// rule does NOT cover it, so the sidecar never gets its config and
	// the workload pod hangs waiting for Ready indefinitely rather than
	// erroring cleanly. ensureAPIServerEgressAllowed's ipBlock 0.0.0.0/0
	// rule (no port restriction, working around a real kube-router
	// ipBlock+port-filter engine bug documented on that function)
	// already covers ANY in-cluster destination, istiod included, so
	// reusing it here is both correct and the least-new-surface fix.
	if err := ensureAPIServerEgressAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing egress to istiod: %w", err)
	}

	// Checking the CRD alone is NOT sufficient to prove istiod itself is
	// running -- confirmed live as a real bug: CRDs are cluster-scoped
	// and survive a `kubectl delete ns istio-system` (e.g. from earlier
	// manual debugging), which orphans them while istiod itself is
	// gone. Also check for the real istiod Deployment before skipping
	// the install step.
	crdInstalled, err := clusterbootstrap.CRDInstalled(ctx, restConfig, istiodDeploymentCRDName)
	if err != nil {
		return fmt.Errorf("checking Istio CRDs installed: %w", err)
	}
	_, istiodErr := clientset.AppsV1().Deployments("istio-system").Get(ctx, "istiod", metav1.GetOptions{})
	installed := crdInstalled && istiodErr == nil
	if !installed {
		// `istioctl manifest generate` deliberately omits the
		// istio-system Namespace object itself -- confirmed live: it
		// assumes `istioctl install` (which pre-creates it) rather than
		// plain `kubectl apply`, and every resource in the manifest
		// targeting that namespace fails with a real "namespaces
		// istio-system not found" without this.
		if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "istio-system"},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating istio-system namespace: %w", err)
		}

		installCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
		defer cancel()
		if err := clusterbootstrap.ApplyManifestContent(installCtx, restConfig, manifests.IstioMinimalYAML); err != nil {
			return fmt.Errorf("installing Istio minimal profile: %w", err)
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
		defer waitCancel()
		if err := clusterbootstrap.WaitForCRDEstablished(waitCtx, restConfig, istiodDeploymentCRDName); err != nil {
			return fmt.Errorf("waiting for Istio CRDs to establish: %w", err)
		}
	}
	if err := waitForIstiodReady(ctx, clientset); err != nil {
		return fmt.Errorf("waiting for istiod deployment ready: %w", err)
	}

	if err := ensureSidecarInjectionEnabled(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("enabling sidecar injection: %w", err)
	}
	if err := ensureIstioTestWorkloads(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring istio test workloads: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+istioSvcName, 120*time.Second); err != nil {
		return fmt.Errorf("waiting for practice-svc pod: %w", err)
	}
	callerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+istioCallerName, 120*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for practice-caller pod: %w", err)
	}
	// Sidecar injection means 2 containers per pod (app + istio-proxy) --
	// WaitForPodByLabel's PodReady check already covers both being
	// ready, but the ready-condition can flip true a moment before
	// Envoy's own listener config actually propagates from istiod
	// (xDS push latency), confirmed live as a real, reproducible race
	// during this fixture's own build.
	time.Sleep(5 * time.Second)

	if err := ensureIstioDestinationRuleAndVirtualService(ctx, restConfig, namespace); err != nil {
		return fmt.Errorf("ensuring destinationrule/virtualservice baseline: %w", err)
	}

	verifyResult, err := k8s.ExecInPod(ctx, provisioner, namespace, callerPod, "app",
		fmt.Sprintf("wget -q -T 5 -O- http://%s:80/", istioSvcName), 15*time.Second)
	if err != nil {
		return fmt.Errorf("verifying healthy baseline call: %w", err)
	}
	if verifyResult.ExitCode != 0 {
		return fmt.Errorf("healthy baseline call failed (exit %d): %s", verifyResult.ExitCode, verifyResult.Stdout+verifyResult.Stderr)
	}
	return nil
}

// waitForIstiodReady confirms both the istiod Deployment itself AND its
// mutating webhook configuration exist, then adds a real settle delay --
// confirmed live as a real, reproducible race on a genuinely FRESH
// install (as opposed to reusing an already-running istiod from an
// earlier call): istiod's own readiness probe can pass before the
// webhook's CA bundle has fully propagated to the API server, so a pod
// created too soon after "istiod ready" gets silently skipped for
// sidecar injection entirely (comes up as a plain 1-container pod, not
// an error) -- which this fixture's own later assumption of 2
// containers then hangs waiting for.
func waitForIstiodReady(ctx context.Context, clientset *kubernetes.Clientset) error {
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		dep, err := clientset.AppsV1().Deployments("istio-system").Get(ctx, "istiod", metav1.GetOptions{})
		webhookReady := false
		if err == nil && dep.Status.ReadyReplicas > 0 {
			webhook, whErr := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "istio-sidecar-injector", metav1.GetOptions{})
			if whErr == nil && len(webhook.Webhooks) > 0 && len(webhook.Webhooks[0].ClientConfig.CABundle) > 0 {
				webhookReady = true
			}
		}
		if webhookReady {
			// Real settle margin past "webhook object has a CA bundle"
			// for the API server's own admission-webhook client cache to
			// pick it up -- confirmed live this specific extra margin
			// closes the race a bare readiness check alone did not.
			time.Sleep(10 * time.Second)
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("istiod/sidecar-injector webhook not ready within 150s")
}

func ensureSidecarInjectionEnabled(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	ns, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting namespace: %w", err)
	}
	if ns.Labels["istio-injection"] == "enabled" {
		return nil
	}
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels["istio-injection"] = "enabled"
	_, err = clientset.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	return err
}

// ensureIstioTestWorkloads: two plain Deployments with no explicit
// SecurityContext -- Istio's own sidecar injection (istio-init or the
// CNI-based equivalent) needs to run its traffic-redirect setup with
// elevated privilege at container-init time in this profile, confirmed
// live this session incompatible with PodSecurity "restricted" the same
// way Jenkins/Ansible/Gitea's own root-requiring init already is; this
// fixture's own test namespace is deliberately created without that
// label, same documented, scoped exception.
func ensureIstioTestWorkloads(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)

	svcDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: istioSvcName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": istioSvcName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": istioSvcName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "app",
							Image:   "docker.io/library/alpine:latest",
							Command: []string{"sh", "-c", `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok'; } | nc -l -p 8080; done`},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, svcDeployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating practice-svc deployment: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: istioSvcName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": istioSvcName},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating practice-svc service: %w", err)
	}

	callerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: istioCallerName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": istioCallerName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": istioCallerName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "app",
							Image:   "docker.io/library/alpine:latest",
							Command: []string{"sh", "-c", "sleep infinity"},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, callerDeployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating practice-caller deployment: %w", err)
	}
	return nil
}

// ensureIstioDestinationRuleAndVirtualService: a real, healthy baseline
// DestinationRule (no explicit tls.mode override -- inherits mesh
// default, which does NOT conflict with any PeerAuthentication) and a
// real VirtualService with a valid 50/50 weighted split -- the healthy
// state both fault handlers mutate away from.
func ensureIstioDestinationRuleAndVirtualService(ctx context.Context, restConfig *rest.Config, namespace string) error {
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	dr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1",
		"kind":       "DestinationRule",
		"metadata":   map[string]any{"name": istioDRName, "namespace": namespace},
		"spec": map[string]any{
			"host": istioSvcName,
			"subsets": []any{
				map[string]any{"name": "v1", "labels": map[string]any{"app": istioSvcName}},
			},
		},
	}}
	if _, err := dyn.Resource(istioDestRuleGVR).Namespace(namespace).Create(ctx, dr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating healthy destinationrule: %w", err)
	}

	vs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1",
		"kind":       "VirtualService",
		"metadata":   map[string]any{"name": istioVSName, "namespace": namespace},
		"spec": map[string]any{
			"hosts": []any{istioSvcName},
			"http": []any{
				map[string]any{
					"route": []any{
						map[string]any{
							"destination": map[string]any{"host": istioSvcName, "subset": "v1"},
							"weight":      int64(100),
						},
					},
				},
			},
		},
	}}
	if _, err := dyn.Resource(istioVSGVR).Namespace(namespace).Create(ctx, vs, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating healthy virtualservice: %w", err)
	}
	return nil
}
