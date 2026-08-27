package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.ansible-target.v1", applyAnsibleTarget)
	registerChecksum("fx.ansible-target.v1", "v1")
}

const (
	ansibleRunnerDeployment       = "practice-ansible-runner"
	ansibleTarget1Name            = "practice-ansible-target1"
	ansibleTarget2Name            = "practice-ansible-target2"
	ansibleInventoryConfigMapName = "practice-ansible-inventory"
	ansibleSSHKeyConfigMapName    = "practice-ansible-ssh-key"
	// ansibleInventoryHost is the fixed name
	// f.ansible.inventory-host-unreachable's params_schema targets
	// (inventory_host) -- content authors reference this exact name.
	// This fixture always makes target2 the one the fault can block
	// (target1 stays the always-reachable control host, matching the
	// fault's own "isn't obviously distinguished from a task logic bug"
	// framing -- a learner needs one KNOWN-good host for contrast).
	ansibleInventoryHost = "target2"
)

// ansibleSSHPrivateKeyPEM/ansibleSSHPublicKeyOpenSSH: a fixed, dev-only
// ed25519 keypair generated once for this fixture (NOT per-environment
// -- a shared fixture keypair is fine here since it only ever
// authenticates the runner pod to the target pods WITHIN one learner's
// own namespace, never crossing a trust boundary; matches this
// codebase's existing "dev-only, not a real secret" stance for e.g.
// WS_GATEWAY_JWT_SECRET's committed dev default). Generated with `ssh-
// keygen -t ed25519 -N ""` and confirmed live to authenticate
// successfully against linuxserver/openssh-server.
const ansibleSSHPrivateKeyPEM = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACAV5spRG/GjVVvZjE/ScRYNje64zbAFJ4wKFIY6l2Hu9QAAAKjz5Paj8+T2
owAAAAtzc2gtZWQyNTUxOQAAACAV5spRG/GjVVvZjE/ScRYNje64zbAFJ4wKFIY6l2Hu9Q
AAAEBAeslfT0Wzet82dPPsYR3A4Bjlz2qBsXqFSJLU86ZdHxXmylEb8aNVW9mMT9JxFg2N
7rjNsAUnjAoUhjqXYe71AAAAH3ByYWN0aWNlLWVuZ2luZS1hbnNpYmxlLWZpeHR1cmUBAg
MEBQY=
-----END OPENSSH PRIVATE KEY-----
`

const ansibleSSHPublicKeyOpenSSH = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBXmylEb8aNVW9mMT9JxFg2N7rjNsAUnjAoUhjqXYe71 practice-engine-ansible-fixture`

const ansibleInventoryINI = `[targets]
target1 ansible_host=practice-ansible-target1 ansible_port=2222 ansible_user=ansible
target2 ansible_host=practice-ansible-target2 ansible_port=2222 ansible_user=ansible

[targets:vars]
ansible_ssh_common_args='-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null'
ansible_private_key_file=/ansible-keys/id_ed25519
`

// applyAnsibleTarget is fx.ansible-target.v1: a real ansible-playbook
// runner pod (willhallonline/ansible, confirmed live) plus two real
// SSH-reachable target pods (linuxserver/openssh-server + python3
// installed at container start -- Ansible's default modules need a
// remote Python interpreter, confirmed live this specific combination
// works: `ansible targets -m ping` returns pong against both). Both
// targets are healthy/reachable at fixture-apply time --
// f.ansible.inventory-host-unreachable's handler later makes ONE of them
// (target2) genuinely unreachable by corrupting its inventory
// ansible_host entry, so SSH itself fails at DNS resolution. This
// codebase's existing NetworkPolicy-based mechanism
// (applyNetworkPolicyOverblocksTraffic/applyEgressProxyAllowlistTooStrict)
// was tried first and rejected here: live-tested against this project's
// real dev cluster, k3s's default CNI (Flannel, no netpol controller)
// does not enforce NetworkPolicy at all (confirmed with a real
// deny-all-egress policy that had zero effect) -- see
// handlers_batch11.go's own doc comment for the full finding, which
// applies to those pre-existing handlers too, not just this one.
func applyAnsibleTarget(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureAnsibleSSHKeyConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring ssh key ConfigMap: %w", err)
	}
	if err := ensureAnsibleInventoryConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring inventory ConfigMap: %w", err)
	}
	if err := ensureAnsibleSSHTargetPod(ctx, clientset, namespace, ansibleTarget1Name); err != nil {
		return fmt.Errorf("ensuring target1 pod: %w", err)
	}
	if err := ensureAnsibleSSHTargetPod(ctx, clientset, namespace, ansibleTarget2Name); err != nil {
		return fmt.Errorf("ensuring target2 pod: %w", err)
	}
	if err := ensureAnsibleRunnerDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring ansible runner deployment: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+ansibleTarget1Name, 90*time.Second); err != nil {
		return fmt.Errorf("waiting for target1 pod: %w", err)
	}
	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+ansibleTarget2Name, 90*time.Second); err != nil {
		return fmt.Errorf("waiting for target2 pod: %w", err)
	}
	runnerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+ansibleRunnerDeployment, 90*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for runner pod: %w", err)
	}

	// SSH target pods need a moment past Ready for sshd itself (started
	// by the image's own init system) to actually be accepting
	// connections -- confirmed live this small extra margin avoids a
	// real race where the healthy-baseline ping fails transiently right
	// after the pod first reports Ready.
	time.Sleep(5 * time.Second)

	pingCmd := "ansible targets -i /ansible-config/inventory.ini -m ping"
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "ansible", pingCmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("running healthy-baseline ansible ping: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("healthy-baseline ansible ping failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}

	return nil
}

func ensureAnsibleSSHKeyConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{
		"id_ed25519":     ansibleSSHPrivateKeyPEM,
		"id_ed25519.pub": ansibleSSHPublicKeyOpenSSH,
	}
	existing, err := cms.Get(ctx, ansibleSSHKeyConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ansibleSSHKeyConfigMapName, Namespace: namespace},
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

func ensureAnsibleInventoryConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{"inventory.ini": ansibleInventoryINI}
	existing, err := cms.Get(ctx, ansibleInventoryConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ansibleInventoryConfigMapName, Namespace: namespace},
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

// ensureAnsibleSSHTargetPod: linuxserver/openssh-server, keyed to the
// fixture's own fixed public key via env var, with python3 installed at
// container start via a Command override (the base image is Alpine and
// has no Python by default -- confirmed live Ansible's default modules
// fail with "module interpreter not found" without it). Runs as the
// image's own default (root-requiring s6 init: this specific image
// needs to start as root to manage sshd host keys/permissions,
// confirmed live during this fixture's own build -- it is NOT run
// privileged, and drops to its own unprivileged sshd user for the
// actual SSH session; PodSecurity "restricted" cannot be satisfied by
// this specific image's own init requirements, so this pod is deployed
// in the fault's own dedicated concern and documented here rather than
// silently forced into a posture that would break it).
func ensureAnsibleSSHTargetPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": name, "ansible-target": "true"}},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "ssh-key",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ansibleSSHKeyConfigMapName}},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "sshd",
					Image: "docker.io/linuxserver/openssh-server:latest",
					Env: []corev1.EnvVar{
						{Name: "PUBLIC_KEY", Value: ansibleSSHPublicKeyOpenSSH},
						{Name: "USER_NAME", Value: "ansible"},
						{Name: "PASSWORD_ACCESS", Value: "false"},
						// Real T1/T2 namespaces default-deny all egress
						// except to this proxy (ApplyEgressProxyAllowlist)
						// -- apk needs it to reach dl-cdn.alpinelinux.org,
						// confirmed live: without these, `apk add` fails
						// even to resolve DNS under the real policy.
						{Name: "HTTP_PROXY", Value: k8s.EgressProxyURL},
						{Name: "HTTPS_PROXY", Value: k8s.EgressProxyURL},
						{Name: "http_proxy", Value: k8s.EgressProxyURL},
						{Name: "https_proxy", Value: k8s.EgressProxyURL},
					},
					// python3 is installed SYNCHRONOUSLY before handing off
					// to the image's own s6-overlay init (PID 1, which
					// handles PUBLIC_KEY seeding + sshd startup) --
					// confirmed live this fixture's own first attempt (a
					// background `sleep 15 && apk add` racing against the
					// TCP-only readiness probe) let ansible connect via
					// SSH successfully before python3 had actually
					// finished installing, causing a real, reproducible
					// "module interpreter not found" failure on the
					// healthy baseline. Installing first and only THEN
					// execing /init means the readiness probe (which
					// gates on sshd actually listening) can never observe
					// a Ready pod with python3 still missing.
					Command: []string{"sh", "-c", `
apk add --no-cache python3 >/tmp/apk-install.log 2>&1
exec /init
`},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "ssh-key", MountPath: "/ssh-key"},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(2222)},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       5,
						FailureThreshold:    20,
					},
				},
			},
		},
	}
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ssh target pod %s: %w", name, err)
	}

	// A bare Pod has no stable cluster DNS name of its own -- only a
	// Service gives it one (confirmed live: the inventory's
	// ansible_host=practice-ansible-target1 failed to resolve at all
	// without this). Named identically to the pod so the inventory's
	// hostnames need no further change.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 2222, TargetPort: intstr.FromInt32(2222)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ssh target service %s: %w", name, err)
	}
	return nil
}

func ensureAnsibleRunnerDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)
	keyMode := int32(0o600)
	inventoryMode := int32(0o444)
	runAsNonRoot := true
	runAsUser := int64(1000) // willhallonline/ansible's own default UID
	fsGroup := int64(1000)
	allowPrivilegeEscalation := false

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: ansibleRunnerDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": ansibleRunnerDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": ansibleRunnerDeployment}},
				Spec: corev1.PodSpec{
					// fsGroup is REQUIRED here, not optional -- confirmed
					// live as a real bug during this fixture's own build:
					// ConfigMap-mounted files are always owned root:root
					// regardless of DefaultMode, so a 0600-moded key file
					// is unreadable by the non-root runAsUser without a
					// matching fsGroup (K8s chowns/chmods volume contents
					// to fsGroup on mount -- ssh's own strict permission
					// check on the private key then passes because the
					// container's own group actually owns it).
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						FSGroup:        &fsGroup,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{
							Name: "ssh-key",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: ansibleSSHKeyConfigMapName},
									DefaultMode:          &keyMode,
								},
							},
						},
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: ansibleInventoryConfigMapName},
									DefaultMode:          &inventoryMode,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "ansible",
							Image:   "docker.io/willhallonline/ansible:latest",
							Command: []string{"sh", "-c", "sleep infinity"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "ssh-key", MountPath: "/ansible-keys"},
								{Name: "config", MountPath: "/ansible-config"},
							},
							Env: []corev1.EnvVar{
								{Name: "ANSIBLE_HOST_KEY_CHECKING", Value: "False"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ansible runner deployment: %w", err)
	}
	return nil
}
