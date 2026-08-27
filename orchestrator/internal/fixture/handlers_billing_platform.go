package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.billing-platform.v1", applyBillingPlatform)
	registerChecksum("fx.billing-platform.v1", "v1")
}

// Fixed names sim.k8s.platform-migration-incident.yaml's own faults and
// validators reference -- content authors reference these exact names.
const (
	billingServiceName  = "billing-service"
	paymentServiceName  = "payment-service"
	authServiceName     = "auth-service"
	configServiceName   = "config-service"
	billingConfigMap    = "billing-config"
	billingPVCName      = "billing-data"
	billingStorageClass = "billing-standard"
	billingQuotaName    = "billing-quota"
)

// applyBillingPlatform is fx.billing-platform.v1: a real, healthy-by-
// default multi-service baseline backing
// sim.k8s.platform-migration-incident.yaml's four independent K8s
// faults (a renamed ConfigMap key, a missing StorageClass, a
// NetworkPolicy allow-list gap, and a too-tight ResourceQuota) -- each
// fault's own real handler (internal/faultinjection) mutates exactly
// one of the real objects this fixture creates.
func applyBillingPlatform(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureBillingStorageClass(ctx, clientset); err != nil {
		return fmt.Errorf("ensuring storage class: %w", err)
	}
	if err := ensureBillingConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring billing config: %w", err)
	}
	if err := ensureBillingQuota(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring billing quota: %w", err)
	}
	if err := ensureBillingPVC(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring billing pvc: %w", err)
	}

	// Real backing services billing-service's allow-list either does
	// (auth-service, config-service) or doesn't (payment-service) cover
	// -- same minimal HTTP-answering pattern as this session's other
	// fixtures.
	for _, name := range []string{paymentServiceName, authServiceName, configServiceName} {
		if err := ensureBillingPeerService(ctx, clientset, namespace, name); err != nil {
			return fmt.Errorf("ensuring %s: %w", name, err)
		}
	}
	if err := ensureBillingServiceDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring billing-service deployment: %w", err)
	}

	for _, name := range []string{paymentServiceName, authServiceName, configServiceName} {
		if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+name, 60*time.Second); err != nil {
			return fmt.Errorf("waiting for %s pod: %w", name, err)
		}
	}
	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+billingServiceName, 60*time.Second); err != nil {
		return fmt.Errorf("waiting for billing-service pod: %w", err)
	}
	return nil
}

// ensureBillingStorageClass: a real, valid StorageClass that DOES exist
// -- f.k8s.pvc-storageclass-missing's handler later swaps
// billing-data's spec.storageClassName to a class that doesn't, exactly
// matching that fault's real mechanism (already live-verified in this
// session's earlier fixture work).
func ensureBillingStorageClass(ctx context.Context, clientset *kubernetes.Clientset) error {
	waitForFirstConsumer := storagev1.VolumeBindingWaitForFirstConsumer
	sc := &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: billingStorageClass},
		Provisioner:       "rancher.io/local-path",
		VolumeBindingMode: &waitForFirstConsumer,
	}
	if _, err := clientset.StorageV1().StorageClasses().Create(ctx, sc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating storage class: %w", err)
	}
	return nil
}

func ensureBillingConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: billingConfigMap, Namespace: namespace},
		Data:       map[string]string{"DB_HOST": "billing-db.internal"},
	}
	if _, err := clientset.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating configmap: %w", err)
	}
	return nil
}

// ensureBillingQuota: a real, comfortably-generous ResourceQuota (well
// above what 3 replicas at billing-service's real per-pod request need)
// -- f.k8s.resourcequota-blocks-deploy's handler later tightens hard_cpu
// well below that, matching its own real, already-verified mechanism.
func ensureBillingQuota(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: billingQuotaName, Namespace: namespace},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU: resource.MustParse("4"),
			},
		},
	}
	if _, err := clientset.CoreV1().ResourceQuotas(namespace).Create(ctx, rq, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating resourcequota: %w", err)
	}
	return nil
}

// ensureBillingPVC: bound successfully against the real storage class
// above -- deliberately NOT mounted by any pod in this fixture (t2's
// validator only checks phase=Bound, matching the fault's own real
// scope: swapping storageClassName alone is enough to reproduce Pending
// without needing a real mount to observe it).
func ensureBillingPVC(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: billingPVCName, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &billingStorageClassRef,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating pvc: %w", err)
	}
	return nil
}

var billingStorageClassRef = billingStorageClass

func ensureBillingPeerService(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) error {
	replicas := int32(1)
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	runAsUser := int64(1000)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
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
							Command: []string{"sh", "-c", `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok'; } | nc -l -p 8080; done`},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							// REQUIRED, not optional, once a namespace has
							// ANY compute-resource ResourceQuota (this
							// fixture's own billing-quota) -- confirmed
							// live as a real bug: K8s rejects pod creation
							// outright ("must specify requests.cpu for:
							// <container>") for any container missing an
							// explicit request once such a quota exists,
							// regardless of how much headroom the quota
							// actually has.
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
		return fmt.Errorf("creating %s deployment: %w", name, err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating %s service: %w", name, err)
	}
	return nil
}

// ensureBillingServiceDeployment: the actual target this activity's
// learner works on -- references billing-config's REAL key (DB_HOST,
// matching f.k8s.configmap-key-renamed's own old_key param exactly) via
// envFrom, so the fault's rename genuinely produces a real
// CreateContainerConfigError.
func ensureBillingServiceDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)
	allowPrivilegeEscalation := false
	runAsNonRoot := true
	runAsUser := int64(1000)
	optional := false

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: billingServiceName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": billingServiceName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": billingServiceName}},
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
							Command: []string{"sh", "-c", `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok'; } | nc -l -p 8080; done`},
							Env: []corev1.EnvVar{
								{
									Name: "DB_HOST",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: billingConfigMap},
											Key:                  "DB_HOST",
											Optional:             &optional,
										},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating billing-service deployment: %w", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: billingServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": billingServiceName},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating billing-service service: %w", err)
	}
	return nil
}
