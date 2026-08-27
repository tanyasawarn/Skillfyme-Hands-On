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
	register("fx.terraform-workspace.v1", applyTerraformWorkspace)
	registerChecksum("fx.terraform-workspace.v1", "v1")
}

const (
	tfRunnerDeployment = "practice-terraform-runner"
	tfMinioDeployment  = "practice-terraform-minio"
	tfMinioBucket      = "tf-state"
	tfMinioAccessKey   = "minioadmin"
	tfMinioSecretKey   = "minioadmin123" // dev-only, same "not a real secret" stance as the Ansible fixture's fixed SSH keypair
	tfConfigMapName    = "practice-terraform-config"

	// tfRegistryModuleSource/tfVersionA/tfVersionB: a REAL, public,
	// HashiCorp-published Terraform registry module
	// (registry.terraform.io/modules/hashicorp/dir/template) --
	// deliberately chosen because it "does not use any Terraform
	// providers, and does not declare any Terraform resources" (its own
	// published README), so pulling it needs zero cloud credentials,
	// unlike almost every other real registry module. Confirmed live
	// this session: v1.0.0 and v1.0.2 have IDENTICAL input/output
	// interfaces but a genuinely different resolved behavior for the
	// same input (v1.0.0 maps .svg files to Content-Type "image/svg";
	// v1.0.2 fixed it to the correct "image/svg+xml") -- a real,
	// observable "silently gets breaking-change behavior" difference
	// between two real, currently-published versions, not a synthetic
	// stand-in.
	tfRegistryModuleSource = "hashicorp/dir/template"
	tfVersionA             = "1.0.0"
	tfVersionB             = "1.0.2"
)

// tfWorkspaceMainTF: the local-backend workspace used by
// f.tf.state-drift-manual-change (a real local_file resource whose
// content can be changed by hand outside Terraform, producing a real
// plan diff) and as the general-purpose real Terraform workspace this
// fixture's params_schema-referenced resource_address
// ("local_file.tracked") targets.
const tfWorkspaceMainTF = `terraform {
  required_providers {
    local = { source = "hashicorp/local", version = "~> 2.5" }
  }
}

resource "local_file" "tracked" {
  filename = "${path.module}/tracked.txt"
  content  = "managed-by-terraform"
}
`

// tfLockWorkspaceMainTF: a SEPARATE workspace using the real S3 backend
// against this fixture's own MinIO instance, with use_lockfile = true
// (Terraform 1.10+'s native S3 conditional-write locking, no DynamoDB
// table needed). f.tf.state-lock-orphan needs THIS workspace
// specifically -- confirmed live (this session) that a local backend's
// OS-level flock self-releases the instant a holding process dies
// (even via SIGKILL), so a genuinely orphaned lock cannot be produced
// against a local backend; MinIO's lock object has no such OS tie-in
// and DOES survive a killed process, matching real production
// stale-lock incidents (which happen against remote backends, not
// local ones).
const tfLockWorkspaceMainTF = `terraform {
  backend "s3" {
    bucket                      = "tf-state"
    key                         = "terraform.tfstate"
    region                      = "us-east-1"
    endpoints                   = { s3 = "http://practice-terraform-minio:9000" }
    access_key                  = "minioadmin"
    secret_key                  = "minioadmin123"
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
    use_path_style               = true
    use_lockfile                 = true
  }
  required_providers {
    time = { source = "hashicorp/time", version = "~> 0.11" }
  }
}

resource "time_sleep" "wait" {
  create_duration = "8s"
}
`

// tfModuleWorkspaceTFTemplate: laid out twice (module-a, module-b),
// each calling the SAME real registry module at a different version
// constraint -- f.tf.module-version-pin-mismatch's actual mechanism
// (see tfRegistryModuleSource's doc comment). Each renders one .svg
// file and outputs its resolved content_type so the difference is
// directly observable without needing to inspect internal module
// state.
const tfModuleWorkspaceTFTemplate = `terraform {
  required_version = ">= 1.0"
}

module "files" {
  source   = "%s"
  version  = "%s"
  base_dir = "${path.module}/src"
}

output "svg_content_type" {
  value = module.files.files["icon.svg"].content_type
}
`

// applyTerraformWorkspace is fx.terraform-workspace.v1: a real
// `hashicorp/terraform` binary running inside a long-lived runner pod,
// plus a real MinIO instance backing the S3-locking workspace. Three
// separate real Terraform workspaces are laid out under the runner's
// own writable workdir:
//   - /work/drift    (local backend, local_file.tracked -- state-drift fault)
//   - /work/lock     (S3-against-MinIO backend, use_lockfile -- lock-orphan fault)
//   - /work/module-a, /work/module-b (local backend each, same real
//     registry module at two different real versions -- module-pin fault)
//
// Every real `terraform init`/`apply` this fixture and its faults run
// needs registry.terraform.io reachable, which is why HTTP_PROXY is set
// (routes through the real T1/T2 egress-proxy allowlist, which this
// session added registry.terraform.io/releases.hashicorp.com to).
func applyTerraformWorkspace(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureAPIServerEgressAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing egress to the API server: %w", err)
	}
	if err := ensureTerraformConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring terraform config ConfigMap: %w", err)
	}
	if err := ensureTerraformMinioDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring minio deployment: %w", err)
	}
	if err := ensureTerraformRunnerDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring terraform runner deployment: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+tfMinioDeployment, 90*time.Second); err != nil {
		return fmt.Errorf("waiting for minio pod: %w", err)
	}
	runnerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+tfRunnerDeployment, 90*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for terraform runner pod: %w", err)
	}

	minioPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, "app="+tfMinioDeployment)
	if err != nil {
		return fmt.Errorf("finding minio pod: %w", err)
	}
	if err := ensureTerraformMinioBucket(ctx, provisioner, namespace, minioPod); err != nil {
		return fmt.Errorf("ensuring minio bucket: %w", err)
	}

	if err := layoutAndApplyTerraformWorkspace(ctx, provisioner, namespace, runnerPod, "drift", tfWorkspaceMainTF); err != nil {
		return fmt.Errorf("applying drift workspace baseline: %w", err)
	}
	if err := layoutAndApplyTerraformWorkspace(ctx, provisioner, namespace, runnerPod, "lock", tfLockWorkspaceMainTF); err != nil {
		return fmt.Errorf("applying lock workspace baseline: %w", err)
	}
	if err := layoutTerraformModuleWorkspace(ctx, provisioner, namespace, runnerPod, "module-a", tfVersionA); err != nil {
		return fmt.Errorf("laying out module-a workspace: %w", err)
	}
	if err := layoutTerraformModuleWorkspace(ctx, provisioner, namespace, runnerPod, "module-b", tfVersionB); err != nil {
		return fmt.Errorf("laying out module-b workspace: %w", err)
	}

	return nil
}

func ensureTerraformConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{
		"drift_main.tf": tfWorkspaceMainTF,
		"lock_main.tf":  tfLockWorkspaceMainTF,
	}
	existing, err := cms.Get(ctx, tfConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: tfConfigMapName, Namespace: namespace},
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

func ensureTerraformMinioDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: tfMinioDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": tfMinioDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": tfMinioDeployment}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:    "minio",
							Image:   "docker.io/minio/minio:latest",
							Command: []string{"minio", "server", "/data", "--address", ":9000"},
							Env: []corev1.EnvVar{
								{Name: "MINIO_ROOT_USER", Value: tfMinioAccessKey},
								{Name: "MINIO_ROOT_PASSWORD", Value: tfMinioSecretKey},
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 9000}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/minio/health/live", Port: intstr.FromInt32(9000)},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       3,
								FailureThreshold:    20,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating minio deployment: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: tfMinioDeployment, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": tfMinioDeployment},
			Ports:    []corev1.ServicePort{{Port: 9000, TargetPort: intstr.FromInt32(9000)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating minio service: %w", err)
	}
	return nil
}

func ensureTerraformRunnerDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	replicas := int32(1)
	configMode := int32(0o444)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: tfRunnerDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": tfRunnerDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": tfRunnerDeployment}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: tfConfigMapName},
									DefaultMode:          &configMode,
								},
							},
						},
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:    "terraform",
							Image:   "docker.io/hashicorp/terraform:latest",
							Command: []string{"sh", "-c", "sleep infinity"},
							Env: []corev1.EnvVar{
								// terraform init needs registry.terraform.io/
								// releases.hashicorp.com -- routed through the
								// real T1/T2 egress-proxy allowlist, confirmed
								// live this session (same fix as the Ansible
								// fixture's apk access).
								{Name: "HTTP_PROXY", Value: k8s.EgressProxyURL},
								{Name: "HTTPS_PROXY", Value: k8s.EgressProxyURL},
								{Name: "http_proxy", Value: k8s.EgressProxyURL},
								{Name: "https_proxy", Value: k8s.EgressProxyURL},
								// NO_PROXY is REQUIRED here, not optional --
								// confirmed live as a real bug during this
								// fixture's own build: without it, Terraform's
								// AWS SDK (unlike the Ansible fixture's apk,
								// which never talks to another in-namespace
								// pod) routed its S3-backend traffic to this
								// fixture's own MinIO service THROUGH Squid
								// instead of directly, and Squid's real
								// deny-all-by-default response (an HTML error
								// page, not S3 XML) surfaced as a confusing
								// "403 + XML syntax error" from Terraform
								// rather than a clean connection failure.
								{Name: "NO_PROXY", Value: tfMinioDeployment + ",.svc.cluster.local,.svc"},
								{Name: "no_proxy", Value: tfMinioDeployment + ",.svc.cluster.local,.svc"},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config", MountPath: "/tf-config"},
								{Name: "work", MountPath: "/work"},
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
		return fmt.Errorf("creating terraform runner deployment: %w", err)
	}
	return nil
}

// ensureTerraformMinioBucket creates the S3 bucket the lock workspace's
// backend targets, using MinIO's own bundled `mc` client INSIDE the
// MinIO pod itself (confirmed live: the hashicorp/terraform image ships
// only busybox wget, which has no PUT-method support at all needed for
// S3's plain-HTTP-PUT bucket-creation API -- mc, already present in the
// minio/minio image, is the simplest real option). Idempotent: `mc mb`
// against an already-existing bucket is a harmless no-op error, ignored
// here rather than pre-checked.
func ensureTerraformMinioBucket(ctx context.Context, provisioner *k8s.Provisioner, namespace, minioPod string) error {
	cmd := fmt.Sprintf(
		"mc alias set local http://localhost:9000 %s %s >/dev/null 2>&1 && mc mb local/%s 2>&1 || true",
		tfMinioAccessKey, tfMinioSecretKey, tfMinioBucket,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, minioPod, "minio", cmd, 20*time.Second)
	if err != nil {
		return fmt.Errorf("creating minio bucket via mc: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("creating minio bucket failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}
	return nil
}

// layoutAndApplyTerraformWorkspace lays out mainTF under
// /work/<workspaceDir>/main.tf and runs a real `terraform init` +
// `apply -auto-approve`, establishing the healthy baseline state each
// fault then mutates. Idempotent: an already-applied workspace (its
// state file already exists) is left as-is.
func layoutAndApplyTerraformWorkspace(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod, workspaceDir, mainTF string) error {
	checkCmd := fmt.Sprintf("test -f /work/%s/.applied && echo EXISTS || echo MISSING", workspaceDir)
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", checkCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("checking existing workspace state: %w", err)
	}
	if checkResult.Stdout == "EXISTS" {
		return nil
	}

	setupCmd := fmt.Sprintf("mkdir -p /work/%s && cp /tf-config/%s_main.tf /work/%s/main.tf", workspaceDir, workspaceDir, workspaceDir)
	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", setupCmd, 15*time.Second); err != nil {
		return fmt.Errorf("laying out workspace directory: %w", err)
	}

	initCmd := fmt.Sprintf("cd /work/%s && terraform init -no-color", workspaceDir)
	initResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", initCmd, 60*time.Second)
	if err != nil {
		return fmt.Errorf("running terraform init: %w", err)
	}
	if initResult.ExitCode != 0 {
		return fmt.Errorf("terraform init failed (exit %d): %s", initResult.ExitCode, initResult.Stdout+initResult.Stderr)
	}

	applyCmd := fmt.Sprintf("cd /work/%s && terraform apply -auto-approve -no-color && touch .applied", workspaceDir)
	applyResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", applyCmd, 60*time.Second)
	if err != nil {
		return fmt.Errorf("running terraform apply: %w", err)
	}
	if applyResult.ExitCode != 0 {
		return fmt.Errorf("terraform apply failed (exit %d): %s", applyResult.ExitCode, applyResult.Stdout+applyResult.Stderr)
	}
	return nil
}

// layoutTerraformModuleWorkspace lays out a workspace calling
// tfRegistryModuleSource at the given version and runs `terraform init`
// (not apply -- the module declares no resources, "init" is where its
// version resolution actually happens and is all f.tf.module-version-
// pin-mismatch's diagnostic path needs: "terraform init -upgrade ->
// notice different resolved module versions"). Also runs a real `plan`
// so the resolved svg_content_type output is queryable via `terraform
// show` for the fault's own verification.
func layoutTerraformModuleWorkspace(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod, workspaceDir, version string) error {
	checkCmd := fmt.Sprintf("test -f /work/%s/.applied && echo EXISTS || echo MISSING", workspaceDir)
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", checkCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("checking existing module workspace state: %w", err)
	}
	if checkResult.Stdout == "EXISTS" {
		return nil
	}

	mainTF := fmt.Sprintf(tfModuleWorkspaceTFTemplate, tfRegistryModuleSource, version)
	setupCmd := fmt.Sprintf(`mkdir -p /work/%s/src && echo '<svg></svg>' > /work/%s/src/icon.svg && cat > /work/%s/main.tf <<'TFEOF'
%sTFEOF`, workspaceDir, workspaceDir, workspaceDir, mainTF)
	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", setupCmd, 15*time.Second); err != nil {
		return fmt.Errorf("laying out module workspace directory: %w", err)
	}

	initCmd := fmt.Sprintf("cd /work/%s && terraform init -no-color", workspaceDir)
	initResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", initCmd, 60*time.Second)
	if err != nil {
		return fmt.Errorf("running terraform init for module workspace: %w", err)
	}
	if initResult.ExitCode != 0 {
		return fmt.Errorf("terraform init failed for module workspace (exit %d): %s", initResult.ExitCode, initResult.Stdout+initResult.Stderr)
	}

	applyCmd := fmt.Sprintf("cd /work/%s && terraform apply -auto-approve -no-color && touch .applied", workspaceDir)
	applyResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", applyCmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("running terraform apply for module workspace: %w", err)
	}
	if applyResult.ExitCode != 0 {
		return fmt.Errorf("terraform apply failed for module workspace (exit %d): %s", applyResult.ExitCode, applyResult.Stdout+applyResult.Stderr)
	}
	return nil
}
