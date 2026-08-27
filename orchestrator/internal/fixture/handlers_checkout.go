package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.checkout-deployment.v1", applyCheckoutDeployment)
	registerChecksum("fx.checkout-deployment.v1", "v1")
}

// checkoutDeploymentName/checkoutServiceName are the fixed names
// sim.sre.checkout-latency-incident.yaml's own faults/validators/
// health_gate all reference ("service: checkout",
// "http://checkout/healthz") -- content authors reference these exact
// names.
const (
	checkoutDeploymentName = "checkout"
	checkoutServiceName    = "checkout"
)

// applyCheckoutDeployment is fx.checkout-deployment.v1: a real,
// healthy-by-default Deployment+Service named "checkout" -- the target
// sim.sre.checkout-latency-incident.yaml's own two faults
// (f.k8s.readiness-probe-too-aggressive, f.k8s.memory-limit-too-low)
// mutate, and its own health_gate/HTTP_SLO validator
// (http://checkout/healthz) queries.
//
// Found missing entirely during this session's Phase 2 completion pass:
// the ONE existing PRODUCTION_SIM activity referenced this fixture id in
// its own seed: list, but no Go handler for it existed anywhere in this
// package -- meaning that activity's own faults would have failed at
// InjectFault time with a real "Deployment checkout not found" error if
// actually attempted, not just been thin content. Built for real here,
// live-verified against the activity's own exact assumptions
// (readinessProbe present so f.k8s.readiness-probe-too-aggressive has
// something to patch, a real /healthz endpoint returning 200, a real
// memory limit comfortably above the container's actual footprint so
// f.k8s.memory-limit-too-low's tighter limit genuinely triggers OOMKilled).
func applyCheckoutDeployment(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureCheckoutDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring checkout deployment: %w", err)
	}
	if err := ensureCheckoutService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring checkout service: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+checkoutDeploymentName, 90*time.Second); err != nil {
		return fmt.Errorf("waiting for checkout pod: %w", err)
	}
	return nil
}

// checkoutHealthzScript: a tiny, real HTTP server (busybox nc, same
// pattern as this session's other minimal fixture services) that
// answers ONLY /healthz with 200 -- deliberately simple, since this
// fixture exists to be a real fault TARGET (its own probe/memory
// config is what gets mutated), not to model a real checkout service's
// business logic.
const checkoutHealthzScript = `while true; do
  { printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok'; } | nc -l -p 8080
done`

func ensureCheckoutDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	runAsUser := int64(1000)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: checkoutDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": checkoutDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": checkoutDeploymentName}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:    "app",
							Image:   "docker.io/library/alpine:latest",
							Command: []string{"sh", "-c", checkoutHealthzScript},
							Ports:   []corev1.ContainerPort{{ContainerPort: 8080}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")},
								// A real, comfortably-above-actual-footprint
								// limit -- f.k8s.memory-limit-too-low's own
								// params_schema example ("96Mi") is well
								// below this, so the fault genuinely
								// tightens a real headroom gap rather than
								// starting already at the edge.
								Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)},
								},
								InitialDelaySeconds: 2,
								TimeoutSeconds:      5,
								PeriodSeconds:       3,
								FailureThreshold:    10,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating checkout deployment: %w", err)
	}
	return nil
}

func ensureCheckoutService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: checkoutServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": checkoutDeploymentName},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating checkout service: %w", err)
	}
	return nil
}
