package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.helm-release.v1", applyHelmRelease)
	registerChecksum("fx.helm-release.v1", "v1")
}

const (
	helmChartConfigMapName = "practice-helm-chart"
	helmRunnerDeployment   = "practice-helm-runner"
	helmServiceAccountName = "practice-helm-runner"
	helmRoleName           = "practice-helm-runner"
	helmRoleBindingName    = "practice-helm-runner"
	// helmReleaseName is the fixed name
	// f.helm.values-override-not-applied's params_schema targets
	// (release) -- content authors reference this exact name.
	helmReleaseName = "practice-release"
)

// practiceAppChartYAML/practiceAppValuesYAML/practiceAppConfigMapTemplateYAML:
// a tiny, real Helm chart (Chart.yaml + values.yaml + one template) --
// authored inline here (not read from content/fixtures/charts/, since
// this fixture needs to ship the chart's bytes to a ConfigMap the
// orchestrator's own process can reach regardless of where it's
// deployed, and a local chart avoids any dependency on an external
// chart repository, confirmed live to work with zero network access
// beyond the cluster itself). The single template renders
// config.featureFlag into a ConfigMap -- the exact value
// f.helm.values-override-not-applied's handler later tries (and, via
// the typo, fails) to override.
const practiceAppChartYAML = `apiVersion: v2
name: practice-app
description: Practice Engine fixture chart for f.helm.values-override-not-applied
version: 0.1.0
appVersion: "1.0"
`

const practiceAppValuesYAML = `config:
  featureFlag: "off"
`

const practiceAppConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
data:
  featureFlag: {{ .Values.config.featureFlag | quote }}
`

// applyHelmRelease is fx.helm-release.v1: a real Helm 3 binary (the
// official alpine/helm image, confirmed live) running inside a
// long-lived pod in this environment's own namespace, which performs a
// real `helm install` of the tiny chart above -- using the namespace's
// own in-cluster ServiceAccount credentials (no separate kubeconfig
// needed; Helm's Kubernetes client auto-detects in-cluster config the
// same way kubectl does), scoped to a Role/RoleBinding granting exactly
// what installing/upgrading THIS chart's resources in THIS namespace
// needs -- not cluster-admin. f.helm.values-override-not-applied's
// handler later execs into this same pod to run the real `helm upgrade`
// with a typo'd --set key path.
func applyHelmRelease(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	// The Helm runner pod's own in-cluster client talks to the K8s API
	// server directly (the same way kubectl does), which the real
	// default-deny NetworkPolicy blocks by default -- live-verified
	// (this session): `helm install` failed with "cluster unreachable:
	// dial tcp 10.43.0.1:443: connect: connection refused" without this,
	// the exact same gap fx.k3s-ready.v1 (applyK3sReady) already
	// documented and fixed for the learner's own kubectl.
	if err := ensureAPIServerEgressAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing egress to the API server: %w", err)
	}

	if err := ensureHelmChartConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring helm chart ConfigMap: %w", err)
	}
	if err := ensureHelmServiceAccountAndRBAC(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring helm runner RBAC: %w", err)
	}
	if err := ensureHelmRunnerDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring helm runner deployment: %w", err)
	}

	runnerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+helmRunnerDeployment, 120*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for helm runner pod: %w", err)
	}

	if err := runHelmInstall(ctx, provisioner, namespace, runnerPod); err != nil {
		return fmt.Errorf("running helm install: %w", err)
	}

	return nil
}

func ensureHelmChartConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{
		"Chart.yaml":               practiceAppChartYAML,
		"values.yaml":              practiceAppValuesYAML,
		"templates_configmap.yaml": practiceAppConfigMapTemplateYAML,
	}
	existing, err := cms.Get(ctx, helmChartConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: helmChartConfigMapName, Namespace: namespace},
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = data
	_, err = cms.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ensureHelmServiceAccountAndRBAC grants the helm runner pod exactly the
// verbs it needs to install/upgrade THIS chart's resources
// (ConfigMaps -- the only object kind the chart's template creates) in
// its own namespace -- not cluster-admin, matching this codebase's
// existing least-privilege convention for every other in-pod-tool
// fixture (fx.k3s-ready.v1's learner RBAC, etc).
func ensureHelmServiceAccountAndRBAC(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	autoMount := false
	sa := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: helmServiceAccountName, Namespace: namespace},
		AutomountServiceAccountToken: &autoMount,
	}
	if _, err := clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating service account: %w", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: helmRoleName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	if _, err := clientset.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating role: %w", err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: helmRoleBindingName, Namespace: namespace},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: helmServiceAccountName, Namespace: namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: helmRoleName},
	}
	if _, err := clientset.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating role binding: %w", err)
	}
	return nil
}

func ensureHelmRunnerDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(100) // alpine/helm's own image default UID
	allowPrivilegeEscalation := false
	replicas := int32(1)
	defaultMode := int32(0o444)
	autoMount := true

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: helmRunnerDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": helmRunnerDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": helmRunnerDeployment}},
				Spec: corev1.PodSpec{
					ServiceAccountName:           helmServiceAccountName,
					AutomountServiceAccountToken: &autoMount,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{
							Name: "chart",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: helmChartConfigMapName},
									DefaultMode:          &defaultMode,
								},
							},
						},
						{Name: "workdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:    "helm",
							Image:   "docker.io/alpine/helm:latest",
							Command: []string{"sh", "-c", "sleep infinity"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "chart", MountPath: "/chart-src"},
								{Name: "workdir", MountPath: "/work"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating helm runner deployment: %w", err)
	}
	return nil
}

// runHelmInstall lays out the chart from its ConfigMap mount into a real
// directory structure (templates/ subdirectory) -- ConfigMap volumes are
// flat, but Helm requires templates/*.yaml specifically, confirmed live
// -- then runs a real `helm install`. Idempotent: an already-installed
// release is left as-is.
func runHelmInstall(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod string) error {
	checkCmd := fmt.Sprintf("helm status %s -n %s >/dev/null 2>&1 && echo EXISTS || echo MISSING", helmReleaseName, namespace)
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "helm", checkCmd, 20*time.Second)
	if err != nil {
		return fmt.Errorf("checking existing release: %w", err)
	}
	if checkResult.Stdout == "EXISTS" {
		return nil
	}

	setupCmd := `
mkdir -p /work/chart/templates
cp /chart-src/Chart.yaml /work/chart/Chart.yaml
cp /chart-src/values.yaml /work/chart/values.yaml
cp /chart-src/templates_configmap.yaml /work/chart/templates/configmap.yaml
`
	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "helm", setupCmd, 15*time.Second); err != nil {
		return fmt.Errorf("laying out chart directory: %w", err)
	}

	installCmd := fmt.Sprintf("helm install %s /work/chart -n %s", helmReleaseName, namespace)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "helm", installCmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("running helm install: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("helm install failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}
