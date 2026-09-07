package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// T3 workspace pod shape (PLAN.md Phase 3 / PLAN_PHASE3_PROJECTS.md 3.2).
//
// Unlike T1/T2, the risky code a learner runs on a T3 project does NOT
// execute in this pod -- it executes in a vended cloud sandbox account,
// reached with brokered short-lived credentials. This pod is the
// PLATFORM-cluster workspace: the editor (OpenVSCode in production; any
// image with the CLIs in local-real dev) plus a shared emptyDir the
// credential-broker sidecar writes an AWS credentials file into and the
// editor container reads. No gVisor (nothing untrusted runs here), no
// egress proxy (the pod talks to the cloud control plane and the
// platform-managed TF state backend, both allow-listed at the cluster
// NetworkPolicy layer in production; in local-real dev the fake AWS
// client + a MinIO-backed state backend mean it needs no external
// egress at all).
//
// The pod name / container name / service-account name are the SAME
// constants T1/T2 use (WorkspacePodName / WorkspaceContainerName /
// WorkspaceServiceAccountName) so every existing exec path --
// validation.ExecShell, the session broker's PTY, idledetect -- targets
// a T3 pod with no change. The credential sidecar is a SECOND container
// in the same pod, named CredsSidecarContainerName.
const (
	// CredsSidecarContainerName is the credential-broker sidecar in a T3
	// workspace pod. In production it runs the STS refresh loop; in
	// local-real dev the broker (internal/credbroker) runs in-process in
	// the orchestrator and writes the creds file into this container's
	// shared mount via ExecInPod, so the sidecar is just a `sleep`
	// holder that owns the emptyDir mount.
	CredsSidecarContainerName = "creds"

	// T3CredsVolumeName is the emptyDir shared between the editor
	// container and the creds sidecar.
	T3CredsVolumeName = "aws-creds"

	// T3DefaultCredsMountPath is where the AWS credentials file lands
	// inside the pod (both containers mount the same emptyDir here).
	T3DefaultCredsMountPath = "/var/run/secrets/aws"
)

// T3DefaultResources is the T3 workspace pod's per-container ceiling.
// The pod is an editor + a credentials holder + (in local-real dev) a
// terraform CLI run against a MinIO state backend -- it is not a compute
// tier, so this is modest and deliberately below T2's.
var T3DefaultResources = ResourceSpec{CPU: "2", Memory: "2Gi"}

// StartT3WorkspacePodInput is the data CreateT3WorkspacePod needs.
type StartT3WorkspacePodInput struct {
	AttemptID string
	EnvID     string
	// EditorImage is the workspace container image. In production the
	// OpenVSCode server image (infra/images/openvscode); in local-real
	// dev an image that carries terraform + aws CLI + bash (the same
	// linux-tools image works if terraform is added, else a dedicated
	// t3-tools image).
	EditorImage string
	// CredsMountPath defaults to T3DefaultCredsMountPath when empty.
	CredsMountPath string
	// Region is passed through as AWS_DEFAULT_REGION so CLI calls in the
	// pod are region-scoped without the learner setting it.
	Region string
	// FakeAWS, when true (local-real mode), points the AWS SDK/CLI in the
	// pod at a local endpoint and disables credential validation so a
	// `terraform apply` against the MinIO-backed state backend and the
	// fake control plane works with the placeholder brokered creds.
	FakeAWS bool
	// S3EndpointURL is the MinIO endpoint the pod's AWS SDK/CLI uses for
	// the S3 state backend in local-real mode.
	S3EndpointURL string
}

// CreateT3WorkspacePod provisions the namespace + the T3 workspace pod
// (editor container + creds sidecar) on the platform cluster. It reuses
// the T1 namespace baseline (ResourceQuota, LimitRange, default-deny
// NetworkPolicy, restricted PSS, non-automount ServiceAccount) so a T3
// namespace is as locked down as a T1 one -- the difference is only the
// pod shape, not the namespace guard-rails.
//
// Idempotent: a retried call over an existing namespace/pod is a no-op.
func (p *Provisioner) CreateT3WorkspacePod(ctx context.Context, in StartT3WorkspacePodInput) (namespace string, err error) {
	ns := namespaceName(in.EnvID)
	credsPath := in.CredsMountPath
	if credsPath == "" {
		credsPath = T3DefaultCredsMountPath
	}

	// Namespace + the same baseline T1 gets. Tier is TierT1SharedContainer
	// here on purpose: the namespace-level guard-rails (restricted PSS,
	// default-deny netpol, quota) are exactly what a T3 workspace pod
	// should also run under -- there is no "T3 namespace" concept, only
	// a T3 pod shape.
	if err := p.createNamespace(ctx, ns, in.AttemptID, TierT1SharedContainer); err != nil {
		return "", fmt.Errorf("t3 namespace: %w", err)
	}
	if err := p.applyResourceQuota(ctx, ns, TierT1SharedContainer); err != nil {
		return "", fmt.Errorf("t3 quota: %w", err)
	}
	if err := p.applyLimitRange(ctx, ns, TierT1SharedContainer); err != nil {
		return "", fmt.Errorf("t3 limitrange: %w", err)
	}
	if err := p.applyDefaultDenyNetworkPolicy(ctx, ns); err != nil {
		return "", fmt.Errorf("t3 netpol: %w", err)
	}
	if err := p.applyServiceAccount(ctx, ns); err != nil {
		return "", fmt.Errorf("t3 serviceaccount: %w", err)
	}

	env := []corev1.EnvVar{
		{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: credsPath + "/credentials"},
		{Name: "AWS_CONFIG_FILE", Value: credsPath + "/config"},
	}
	if in.Region != "" {
		env = append(env, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: in.Region})
		env = append(env, corev1.EnvVar{Name: "AWS_REGION", Value: in.Region})
	}
	if in.FakeAWS {
		// local-real mode: make the AWS SDK/CLI resolve against the local
		// endpoint and skip credential-freshness checks so terraform can
		// run against the MinIO state backend with placeholder creds.
		env = append(env,
			corev1.EnvVar{Name: "AWS_EC2_METADATA_DISABLED", Value: "true"},
			corev1.EnvVar{Name: "AWS_STS_REGIONAL_ENDPOINTS", Value: "regional"},
		)
		if in.S3EndpointURL != "" {
			env = append(env, corev1.EnvVar{Name: "AWS_ENDPOINT_URL_S3", Value: in.S3EndpointURL})
			env = append(env, corev1.EnvVar{Name: "AWS_ENDPOINT_URL", Value: in.S3EndpointURL})
		}
	}

	limitCPU := nonEmpty("", T3DefaultResources.CPU)
	limitMem := nonEmpty("", T3DefaultResources.Memory)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspacePodName,
			Namespace: ns,
			Labels: map[string]string{
				"app":                           WorkspacePodName,
				"practiceengine.dev/tier":       "t3",
				"practiceengine.dev/attempt-id": in.AttemptID,
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: WorkspaceServiceAccountName,
			SecurityContext:    RestrictedPodSecurityContext(),
			RestartPolicy:      corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            WorkspaceContainerName,
					Image:           in.EditorImage,
					SecurityContext: RestrictedContainerSecurityContext(false),
					Env:             env,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(limitCPU),
							corev1.ResourceMemory: resource.MustParse(limitMem),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: WorkspacePodName, MountPath: "/workspace"},
						{Name: T3CredsVolumeName, MountPath: credsPath},
					},
					// Keeps the container alive; every exec path (broker
					// creds write, validation.ExecShell, PTY) execs in.
					Command: []string{"sh", "-c", "sleep infinity"},
				},
				{
					Name:            CredsSidecarContainerName,
					Image:           in.EditorImage,
					SecurityContext: RestrictedContainerSecurityContext(false),
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("50m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: T3CredsVolumeName, MountPath: credsPath},
					},
					Command: []string{"sh", "-c", "sleep infinity"},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         WorkspacePodName,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
				{
					Name:         T3CredsVolumeName,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
		},
	}

	_, cerr := p.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err := ignoreAlreadyExists(cerr); err != nil {
		return "", fmt.Errorf("t3 workspace pod: %w", err)
	}
	return ns, nil
}

// DeleteT3Namespace tears down a T3 workspace namespace. Thin alias over
// Destroy(envID) kept named for the t3driver call site's clarity, and so
// a future T3-specific teardown step (e.g. finalising a sidecar log
// flush) has a single place to live.
func (p *Provisioner) DeleteT3Namespace(ctx context.Context, envID string) error {
	return p.Destroy(ctx, envID)
}
