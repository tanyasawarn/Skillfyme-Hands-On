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
	register("fx.rollout-workloads.v1", applyRolloutWorkloads)
	registerChecksum("fx.rollout-workloads.v1", "v1")
}

// Fixed names sim.k8s.rollout-stuck-incident.yaml's own faults/validators
// reference -- content authors reference these exact names.
const (
	rolloutDeploymentName  = "web-frontend"
	rolloutStatefulSetName = "cache-cluster"
	rolloutHeadlessSvcName = "cache-cluster"
)

// applyRolloutWorkloads is fx.rollout-workloads.v1: a real, healthy
// Deployment (2 replicas) and a real, healthy StatefulSet (3 replicas,
// real headless Service -- required for StatefulSet pod DNS identity)
// backing sim.k8s.rollout-stuck-incident.yaml's two independent rollout
// faults.
func applyRolloutWorkloads(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureRolloutDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring web-frontend deployment: %w", err)
	}
	if err := ensureRolloutStatefulSet(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring cache-cluster statefulset: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+rolloutDeploymentName, 60*time.Second); err != nil {
		return fmt.Errorf("waiting for web-frontend pod: %w", err)
	}

	// StatefulSet pods come up strictly in ordinal order -- wait long
	// enough for all 3 (cache-cluster-0/1/2) to report Ready before this
	// fixture hands off, matching every other fixture's own "healthy
	// baseline confirmed before returning" convention.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, rolloutStatefulSetName, metav1.GetOptions{})
		if err == nil && sts.Status.ReadyReplicas >= 3 {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("cache-cluster did not reach 3 ready replicas within 90s")
}

const rolloutHelloScript = `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok'; } | nc -l -p 8080; done`

func ensureRolloutDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(2)
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	runAsUser := int64(1000)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: rolloutDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": rolloutDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": rolloutDeploymentName}},
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
							Command: []string{"sh", "-c", rolloutHelloScript},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating web-frontend deployment: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: rolloutDeploymentName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": rolloutDeploymentName},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating web-frontend service: %w", err)
	}
	return nil
}

func ensureRolloutStatefulSet(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	// A headless Service is required for a StatefulSet's own pod DNS
	// identity (<pod>.<service>.<ns>.svc.cluster.local) -- without one
	// the StatefulSet controller still creates pods, but not with the
	// stable network identity the fault's own hostname-based readiness
	// probe assumes.
	headlessSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: rolloutHeadlessSvcName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  map[string]string{"app": rolloutStatefulSetName},
			Ports:     []corev1.ServicePort{{Port: 8080}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, headlessSvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating headless service: %w", err)
	}

	replicas := int32(3)
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	runAsUser := int64(1000)

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: rolloutStatefulSetName, Namespace: namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: rolloutHeadlessSvcName,
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": rolloutStatefulSetName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": rolloutStatefulSetName}},
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
							Command: []string{"sh", "-c", rolloutHelloScript},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().StatefulSets(namespace).Create(ctx, sts, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating cache-cluster statefulset: %w", err)
	}
	return nil
}
