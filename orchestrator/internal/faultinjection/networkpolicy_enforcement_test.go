package faultinjection

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// This file live-verifies a real, cross-cutting finding from this
// session -- corrected twice:
//
// 1. An early test run found a real deny-all-egress policy having zero
//    effect on real traffic, which was (wrongly) diagnosed as "k3s has
//    no netpol controller at all." The fix at the time was to install a
//    separate, standalone kube-router-netpol DaemonSet.
//
// 2. That "fix" turned out to be wrong: k3s ships its OWN embedded
//    netpol controller by default (`--disable-network-policy` defaults
//    to false -- confirmed via `k3s server --help` and via this
//    project's k3s container's own logs, which show "Starting network
//    policy controller version v2.5.0" from the very first cluster
//    boot, well before the standalone DaemonSet was ever installed).
//    Running a second, independent kube-router instance against the
//    SAME iptables chains (KUBE-ROUTER-INPUT) caused the two
//    controllers to race -- confirmed live: k3s's embedded controller
//    hit an unrecovered panic (klog.Fatalf on a failed iptables
//    rule-delete/insert) and crashed the ENTIRE k3s process, twice,
//    including once mid-test-run. The standalone DaemonSet has been
//    removed; k3s's own embedded controller is what actually enforces
//    NetworkPolicy on this cluster, and always has.
//
// What the ORIGINAL "no enforcement at all" finding's true root cause
// was is not fully determined (possibly a genuine embedded-controller
// bug at the time, possibly a flawed first test) -- these tests are the
// real, live proof of the CURRENT state: whether
// f.k8s.networkpolicy-overblocks-traffic and
// f.cloud.egress-proxy-allowlist-too-strict actually produce their
// claimed symptom against k3s's own embedded controller, not just
// correct K8s objects (the fake-clientset unit tests these two handlers
// already have only assert the latter).
//
// Real-infra-gated (same skip convention as
// internal/orchestrator/ownership_rpc_test.go): needs a live cluster.
// Deliberately does NOT pre-check for any specific controller
// implementation (k3s's embedded controller runs inside the k3s server
// process itself, not as a discoverable K8s pod) -- if enforcement is
// genuinely absent, the tests below fail with a clear REGRESSION
// message rather than silently skipping.

func setupNetworkPolicyEnforcementTest(t *testing.T) (*kubernetes.Clientset, *k8s.Provisioner) {
	t.Helper()
	kubeconfig := "../../../.local/k3s-output/kubeconfig.yaml"
	restConfig, err := k8s.NewRestConfig(kubeconfig)
	if err != nil {
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(restConfig)
	if err != nil {
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		t.Skipf("skipping: k8s cluster unreachable (dev stack not running?): %v", err)
	}

	return clientset, k8s.NewProvisioner(clientset, restConfig, k8s.ProvisionerConfig{})
}

// pingPod execs a short-timeout wget from callerPod against targetIP,
// returning whether it succeeded.
func pingPod(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, callerPod, targetIP string) bool {
	t.Helper()
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, callerPod, "app",
		"wget -q -T 3 -O- http://"+targetIP+":8080", 10*time.Second)
	return err == nil && result.ExitCode == 0
}

func createEchoPod(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, namespace, name string, labels map[string]string) {
	t.Helper()
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &runAsNonRoot,
				RunAsUser:      &runAsUser,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "docker.io/library/busybox:latest",
					Command: []string{"sh", "-c", `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nok\n'; } | nc -l -p 8080; done`},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				},
			},
		},
	}
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating pod %s: %v", name, err)
	}
}

func createIdlePod(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) {
	t.Helper()
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &runAsNonRoot,
				RunAsUser:      &runAsUser,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "docker.io/library/busybox:latest",
					Command: []string{"sh", "-c", "sleep infinity"},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				},
			},
		},
	}
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating pod %s: %v", name, err)
	}
}

func waitForRunning(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, namespace string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			running := 0
			for _, p := range pods.Items {
				if p.Status.Phase == corev1.PodRunning {
					running++
				}
			}
			if running >= count {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("expected %d Running pods in %s within %s", count, namespace, timeout)
}

func TestNetworkPolicyOverblocksTraffic_ReallyBlocksTrafficOnThisCluster(t *testing.T) {
	clientset, provisioner := setupNetworkPolicyEnforcementTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ns := "np-enforce-test-" + time.Now().Format("150405")
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	t.Cleanup(func() { _ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}) })

	// Real T1-equivalent baseline: default-deny egress, matching
	// internal/k8s/provision.go's applyDefaultDenyNetworkPolicy shape.
	if _, err := clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-egress"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating default-deny policy: %v", err)
	}

	createIdlePod(t, ctx, clientset, ns, "caller")
	createEchoPod(t, ctx, clientset, ns, "billing-service", map[string]string{"app": "billing-service"})
	createEchoPod(t, ctx, clientset, ns, "auth-service", map[string]string{"app": "auth-service"})
	waitForRunning(t, ctx, clientset, ns, 3, 30*time.Second)

	billingPod, err := clientset.CoreV1().Pods(ns).Get(ctx, "billing-service", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting billing-service pod: %v", err)
	}
	authPod, err := clientset.CoreV1().Pods(ns).Get(ctx, "auth-service", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting auth-service pod: %v", err)
	}

	if ok := pingPod(t, ctx, provisioner, ns, "caller", billingPod.Status.PodIP); ok {
		t.Fatal("expected caller to be blocked from billing-service BEFORE the fault (default-deny, no allow-rule yet), but it succeeded")
	}

	// The handler itself seeds the allow-list (a real platform team's
	// allow-list covering its known dependencies) deliberately omitting
	// missing_dependency -- see applyNetworkPolicyOverblocksTraffic's doc
	// comment for why an additive "block" policy can't reproduce this
	// symptom under real NetworkPolicy union semantics.
	result, err := applyNetworkPolicyOverblocksTraffic(ctx, clientset, ns, map[string]string{
		"missing_dependency": "billing-service",
		"known_dependencies": "auth-service",
	})
	if err != nil {
		t.Fatalf("applyNetworkPolicyOverblocksTraffic failed: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected Applied=true")
	}
	time.Sleep(5 * time.Second)

	if ok := pingPod(t, ctx, provisioner, ns, "caller", authPod.Status.PodIP); !ok {
		t.Fatal("expected caller to reach auth-service AFTER the fault (it's on the allow-list), but it could not -- allow-list policy may not have applied correctly")
	}
	if ok := pingPod(t, ctx, provisioner, ns, "caller", billingPod.Status.PodIP); ok {
		t.Fatal("REGRESSION: f.k8s.networkpolicy-overblocks-traffic did not actually block billing-service on this cluster even though it was left off the allow-list -- NetworkPolicy enforcement (k3s's embedded netpol controller) may not be running or the earlier root cause of the original finding may still apply")
	}
}

func TestEgressProxyAllowlistTooStrict_ReallyBlocksEgressOnThisCluster(t *testing.T) {
	clientset, provisioner := setupNetworkPolicyEnforcementTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ns := "egress-enforce-test-" + time.Now().Format("150405")
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	t.Cleanup(func() { _ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}) })

	if _, err := clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-egress"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating default-deny policy: %v", err)
	}

	createIdlePod(t, ctx, clientset, ns, "caller")
	createEchoPod(t, ctx, clientset, ns, "egress-proxy", map[string]string{"app": "egress-proxy"})
	waitForRunning(t, ctx, clientset, ns, 2, 30*time.Second)

	proxyPod, err := clientset.CoreV1().Pods(ns).Get(ctx, "egress-proxy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting egress-proxy pod: %v", err)
	}

	// allow-egress-proxy is the exact policy name
	// applyEgressProxyAllowlistTooStrict deletes.
	if _, err := clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "egress-proxy"}}}}},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating allow-egress-proxy policy: %v", err)
	}
	time.Sleep(5 * time.Second)

	if ok := pingPod(t, ctx, provisioner, ns, "caller", proxyPod.Status.PodIP); !ok {
		t.Fatal("expected caller to reach egress-proxy BEFORE the fault, but it could not")
	}

	result, err := applyEgressProxyAllowlistTooStrict(ctx, clientset, ns, map[string]string{})
	if err != nil {
		t.Fatalf("applyEgressProxyAllowlistTooStrict failed: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected Applied=true")
	}
	time.Sleep(5 * time.Second)

	if ok := pingPod(t, ctx, provisioner, ns, "caller", proxyPod.Status.PodIP); ok {
		t.Fatal("REGRESSION: f.cloud.egress-proxy-allowlist-too-strict did not actually block real egress on this cluster -- NetworkPolicy enforcement (k3s's embedded netpol controller) may not be running or the earlier root cause of the original finding may still apply")
	}
}
