package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestT1Isolation_LocalReal is PLAN.md M1.14 ("security baseline audit")
// exercised through REAL execution on the compose k3s cluster, not just
// asserting the pod-spec shape.
//
// gVisor: this local cluster runs `runc` (no gVisor node -- that needs
// GKE-Sandbox, blocked on a GCP billing account, see
// evaluation/phase1/results/phase1-local-verification.md). The FALLBACK
// isolation stack -- PodSecurity `restricted` admission + non-root +
// drop-ALL-caps + seccomp RuntimeDefault + a default-deny NetworkPolicy
// -- is what actually protects a local T1 workload, and this test
// verifies each layer by real API calls and a real in-pod command:
//
//  1. PSS `restricted` ADMISSION really rejects a privileged pod in the
//     managed namespace (not just "our template doesn't set privileged").
//  2. The provisioned workspace pod really runs as non-root, with no
//     added capabilities and seccomp RuntimeDefault.
//  3. The default-deny NetworkPolicy really blocks egress: a `curl` to an
//     external address from inside the pod times out / fails.
//
// Skips (never fails) when the dev stack is unreachable.
func TestT1Isolation_LocalReal(t *testing.T) {
	restConfig, err := NewRestConfig("../../../.local/k3s-output/kubeconfig.yaml")
	if err != nil {
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := NewClientsetFromConfig(restConfig)
	if err != nil {
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		t.Skipf("skipping: k8s cluster unreachable (dev stack not running?): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	const envID = "t1-isolation-live"
	ns := namespaceName(envID)
	prov := NewProvisioner(clientset, restConfig, ProvisionerConfig{}) // GVisorEnabled=false: local runc
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	waitNamespaceGone(ctx, clientset, ns, 90*time.Second)

	// Provision a real T1 environment (namespace + quota + limitrange +
	// default-deny netpol + restricted PSS + SA + workspace pod).
	if err := prov.Provision(ctx, ProvisionRequest{
		AttemptID: "t1-isolation-attempt",
		EnvID:     envID,
		Tier:      TierT1SharedContainer,
		Image:     "registry:5000/practiceengine/linux-tools:v1",
	}); err != nil {
		t.Fatalf("Provision(T1): %v", err)
	}
	if err := prov.WaitForPodReady(ctx, envID); err != nil {
		t.Fatalf("workspace pod not ready: %v", err)
	}

	// --- 1. PSS `restricted` really rejects a privileged pod ----------
	priv := true
	badPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "rogue-privileged", Namespace: ns},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "x",
				Image:           "busybox:latest",
				Command:         []string{"sh", "-c", "sleep 60"},
				SecurityContext: &corev1.SecurityContext{Privileged: &priv},
			}},
		},
	}
	_, createErr := clientset.CoreV1().Pods(ns).Create(ctx, badPod, metav1.CreateOptions{})
	if createErr == nil {
		_ = clientset.CoreV1().Pods(ns).Delete(context.Background(), "rogue-privileged", metav1.DeleteOptions{})
		t.Fatal("PSS `restricted` did NOT reject a privileged pod -- isolation fallback is not being enforced")
	}
	if !apierrors.IsForbidden(createErr) ||
		!strings.Contains(createErr.Error(), "PodSecurity") ||
		!strings.Contains(createErr.Error(), `"restricted`) {
		t.Fatalf("privileged pod was rejected, but not by PodSecurity `restricted` admission as expected: %v", createErr)
	}
	t.Logf("PSS restricted rejected a privileged pod: %v", createErr)

	// --- 2. the workspace pod really runs restricted -----------------
	pod, err := clientset.CoreV1().Pods(ns).Get(ctx, WorkspacePodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get workspace pod: %v", err)
	}
	psc := pod.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Errorf("workspace pod is not runAsNonRoot: %+v", psc)
	}
	if psc == nil || psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("workspace pod seccomp is not RuntimeDefault: %+v", psc.SeccompProfile)
	}
	csc := pod.Spec.Containers[0].SecurityContext
	if csc == nil || csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Errorf("workspace container allows privilege escalation: %+v", csc)
	}
	if csc == nil || csc.Capabilities == nil || len(csc.Capabilities.Drop) == 0 || string(csc.Capabilities.Drop[0]) != "ALL" {
		t.Errorf("workspace container does not drop ALL capabilities: %+v", csc.Capabilities)
	}
	// prove it at runtime: `id -u` inside the pod is not 0
	idOut, err := ExecInPod(ctx, prov, ns, WorkspacePodName, WorkspaceContainerName, "id -u", 15*time.Second)
	if err != nil {
		t.Fatalf("exec `id -u` in workspace pod: %v", err)
	}
	if strings.TrimSpace(idOut.Stdout) == "0" {
		t.Errorf("workspace pod is running as root (uid 0) despite runAsNonRoot")
	}
	t.Logf("workspace pod runtime uid = %s (non-root)", strings.TrimSpace(idOut.Stdout))

	// --- 3. default-deny NetworkPolicy really blocks egress ----------
	// Hit a raw public IP with proxy env vars cleared, so this exercises
	// the NetworkPolicy egress rule directly (not the "egress-proxy DNS
	// doesn't resolve" path). 1.1.1.1:443 is a stable anycast endpoint.
	// A default-deny egress policy makes the TCP connect hang -> curl
	// exits non-zero on --max-time. (Cluster-internal DNS to kube-dns is
	// still allowed by k3s's baseline, so `getent hosts` would succeed;
	// the assertion is specifically about EXTERNAL reachability.)
	netOut, _ := ExecInPod(ctx, prov, ns, WorkspacePodName, WorkspaceContainerName,
		"env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy "+
			"curl -sS --max-time 8 -o /dev/null -w '%{http_code}' https://1.1.1.1 2>&1; echo \" exit=$?\"",
		25*time.Second)
	blocked := netOut.ExitCode != 0 ||
		strings.Contains(netOut.Stdout, "exit=7") || // failed to connect
		strings.Contains(netOut.Stdout, "exit=28") || // operation timed out
		strings.Contains(netOut.Stdout, "exit=35") || // TLS connect error (SYN dropped mid-handshake)
		strings.Contains(netOut.Stdout, "timed out") ||
		strings.Contains(netOut.Stdout, "Failed to connect") ||
		strings.Contains(netOut.Stdout, "Connection refused")
	reachedInternet := strings.HasPrefix(strings.TrimSpace(netOut.Stdout), "200") ||
		strings.HasPrefix(strings.TrimSpace(netOut.Stdout), "3")
	if reachedInternet || !blocked {
		t.Errorf("default-deny NetworkPolicy did NOT block direct external egress: stdout=%q stderr=%q exit=%d",
			netOut.Stdout, netOut.Stderr, netOut.ExitCode)
	} else {
		t.Logf("default-deny NetworkPolicy blocked direct external egress as expected: %q", strings.TrimSpace(netOut.Stdout))
	}

	// Confirm the NetworkPolicy object itself exists and is default-deny
	// (empty podSelector, both policy types).
	np, err := clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, "default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("default-deny NetworkPolicy missing: %v", err)
	}
	if len(np.Spec.PodSelector.MatchLabels) != 0 || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Errorf("default-deny NetworkPolicy podSelector is not empty (would not select all pods): %+v", np.Spec.PodSelector)
	}
	hasEgress, hasIngress := false, false
	for _, pt := range np.Spec.PolicyTypes {
		if pt == "Egress" {
			hasEgress = true
		}
		if pt == "Ingress" {
			hasIngress = true
		}
	}
	if !hasEgress || !hasIngress {
		t.Errorf("default-deny NetworkPolicy does not cover both Ingress+Egress: %v", np.Spec.PolicyTypes)
	}

	// --- teardown ---------------------------------------------------
	if err := prov.Destroy(ctx, envID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	t.Log("T1 isolation fallback verified live: PSS restricted admission rejects privileged; workspace pod non-root + drop-ALL + seccomp RuntimeDefault; default-deny NetworkPolicy blocks egress")
}
