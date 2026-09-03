package fixture

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/ttl"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/validation"
)

func init() {
	register("fx.k3s-ready.v1", applyK3sReady)
	registerChecksum("fx.k3s-ready.v1", "v1")

	register("fx.pod-crashloop.v1", applyPodCrashloop)
	registerChecksum("fx.pod-crashloop.v1", "v1")

	register("fx.node-app-repo.v1", applyNodeAppRepo)
	registerChecksum("fx.node-app-repo.v1", "v1")
}

// applyK3sReady: content's own naming ("k3s ready") reflects the doc's
// original nested-cluster framing, but a real nested k3s control plane
// is T2-only (internal/k8s/provision.go's own doc comment: "k3s/kind
// full Kubernetes... multi-node K8s (nested)" is explicitly a T2
// capability T1 cannot support). What T1 labs actually need -- and what
// every K8s activity's instructions/hints assume ("kubectl apply -f
// pod.yaml creates the Pod") -- is the LEARNER's own interactive kubectl
// working against a real cluster, scoped to their own namespace only.
// That's the platform's own T1 cluster (the same one the orchestrator
// itself runs on), reached via a scoped kubeconfig this fixture mints
// and writes into the workspace pod at ~/.kube/config.
//
// Scope is deliberately broader than MintValidatorCredentials' read-only
// grant (internal/orchestrator/credentials.go) -- a learner completing a
// lab needs to kubectl apply/create/expose/patch/rollout/set, not just
// read state -- but still namespace-scoped only (RoleBinding, never
// ClusterRoleBinding) and still excludes anything that could escape the
// namespace boundary (no access to Nodes, no cluster-scoped resources,
// no RBAC objects themselves -- a learner cannot grant themselves more
// access than this fixture already gives them).
func applyK3sReady(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()
	saName := "learner-workload"
	roleName := "learner-workload"
	bindingName := "learner-workload"

	// A real, live Provision()+ExecShell test against this project's
	// actual k3s cluster caught this: the namespace's default-deny
	// NetworkPolicy (internal/k8s/provision.go's applyDefaultDenyNetworkPolicy)
	// only allow-lists the egress proxy and kube-system DNS -- the K8s
	// API server itself was never reachable, so even a correct
	// kubeconfig (see below) would still hit a NetworkPolicy-level
	// connection block, not just a wrong-address bug. Must run before
	// the kubeconfig is written, though ordering relative to RBAC setup
	// below doesn't matter (independent K8s objects).
	if err := ensureAPIServerEgressAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing egress to the API server: %w", err)
	}

	if err := ensureServiceAccount(ctx, clientset, namespace, saName); err != nil {
		return fmt.Errorf("ensuring learner ServiceAccount: %w", err)
	}
	if err := ensureLearnerWorkloadRole(ctx, clientset, namespace, roleName); err != nil {
		return fmt.Errorf("ensuring learner Role: %w", err)
	}
	if err := ensureRoleBinding(ctx, clientset, namespace, bindingName, roleName, saName); err != nil {
		return fmt.Errorf("ensuring learner RoleBinding: %w", err)
	}
	// Also caught by the same live cluster test: kubectl's OWN first
	// call on every invocation is GET /api (API-version discovery), a
	// cluster-scoped NON-RESOURCE URL -- something a namespace-scoped
	// Role/RoleBinding can never grant, regardless of how permissive its
	// resource rules are (confirmed live: the RBAC above was already
	// correct for actual resource access, but kubectl still failed with
	// "Forbidden" on /api before this fix). K8s ships a built-in
	// ClusterRole named exactly for this (system:discovery -- read-only
	// access to /api, /apis, /healthz, /openapi, nothing about any real
	// resource) that every kubectl-using identity conventionally gets;
	// binding it here is the standard, minimal, safe way to make
	// discovery work without granting anything beyond it.
	if err := ensureDiscoveryClusterRoleBinding(ctx, clientset, namespace, saName); err != nil {
		return fmt.Errorf("ensuring learner discovery access: %w", err)
	}

	// Long-lived relative to a validator credential (doc §6.2's 5-minute
	// default is for read-only validator checks; a learner actively
	// working through a lab needs kubectl to keep working for the whole
	// attempt) -- matches the environment's own ttl_minutes ceiling in
	// practice (an expired token just means a lab's kubectl starts
	// failing with Unauthorized near the attempt's own TTL boundary,
	// not a security concern since the environment itself is about to
	// be torn down anyway).
	tokenTTLSeconds := int64(ttl.FixtureToken.Seconds())
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: int64Ptr(tokenTTLSeconds)},
	}
	tokenResult, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, saName, tokenReq, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("requesting learner kubeconfig token: %w", err)
	}

	// SECURITY/CORRECTNESS: deliberately NOT provisioner.RestConfig().Host
	// -- that's the orchestrator PROCESS's own address for reaching the
	// API server (typically the host-exposed port, e.g.
	// https://127.0.0.1:6443 for this project's local k3s), which is
	// only reachable from the orchestrator's own network namespace. A
	// pod running INSIDE the cluster reaches the API server via the
	// standard in-cluster Kubernetes Service DNS name instead -- every
	// pod gets this resolvable automatically, regardless of how the
	// orchestrator itself connects to the cluster. Caught by a live
	// Provision()+ExecShell test against this project's real k3s
	// cluster during this session: kubectl inside the workspace pod
	// failed with "connection refused" to 127.0.0.1:6443 (the
	// orchestrator's own address, meaningless from inside a pod) before
	// this fix. The cluster CA (restConfig.CAData) is still correct to
	// reuse -- it's the same cluster's CA regardless of which address
	// reaches its API server.
	const inClusterAPIServerHost = "https://kubernetes.default.svc"
	restConfig := provisioner.RestConfig()
	kubeconfig := buildKubeconfigYAML(inClusterAPIServerHost, restConfig.CAData, namespace, tokenResult.Status.Token)

	// Written via the same non-interactive exec mechanism every other
	// in-pod operation in this codebase uses (validation.ExecShell) --
	// the workspace pod has no separate "write a file" API, only exec.
	// heredoc keeps this a single exec call regardless of kubeconfig
	// size, rather than many small appends.
	writeCmd := fmt.Sprintf("mkdir -p ~/.kube && cat > ~/.kube/config <<'FIXTURE_EOF'\n%s\nFIXTURE_EOF\n", kubeconfig)
	result, err := validation.ExecShell(ctx, provisioner, envID, writeCmd, 10_000)
	if err != nil {
		return fmt.Errorf("writing learner kubeconfig into workspace pod: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("writing learner kubeconfig into workspace pod: exit %d: %s", result.ExitCode, result.Stderr)
	}

	return nil
}

// applyPodCrashloop seeds a Pod that immediately and repeatedly crashes
// (exit 1 on every restart) -- the broken-baseline state
// lab.sre.classify-incident-severity.yaml and lab.k8s.troubleshooting.yaml
// both assume already exists before the learner starts diagnosing.
// Applied via the orchestrator's own cluster access (like every
// faultinjection handler), not via the learner's kubectl -- a fixture
// establishes the STARTING state before the learner has done anything,
// the same "orchestrator has cluster access, workspace pod doesn't
// (until fx.k3s-ready.v1 runs)" reasoning applies here too, and ordering
// (doc §5.5's "ordered" requirement) means this can run before or after
// fx.k3s-ready.v1 without depending on the learner's kubeconfig existing.
func applyPodCrashloop(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	// 35s: enough for CrashLoopBackOff to record ~2 restarts (backoff is
	// 10s, 20s, 40s...), which is all the paired validator needs to
	// distinguish "seeded and broken" (restartCount > 0) from "learner
	// deleted + recreated with a working command" (restartCount == 0).
	// Kept well inside the fixture-apply step's slice of the Provision
	// RPC deadline (content-CI uses 60s for the whole Provision).
	return applyPodCrashloopWithClientset(ctx, provisioner.Clientset(), namespace, 35*time.Second)
}

// applyPodCrashloopWithClientset is the clientset-only core of
// applyPodCrashloop -- split out so it's unit-testable without a live
// *k8s.Provisioner (which needs a real *rest.Config to construct).
//
// crashLoopWait bounds how long it waits for the pod to become OBSERVABLY
// crash-looping (restartCount >= 6). Pass 0 to skip the wait (unit tests
// against a fake clientset, where restartCount never advances).
func applyPodCrashloopWithClientset(ctx context.Context, clientset kubernetes.Interface, namespace string, crashLoopWait time.Duration) error {
	// SecurityContext fields here are REQUIRED, not optional hardening --
	// caught by a real live Provision() call against this project's
	// actual k3s cluster: without them, the K8s API server's own
	// PodSecurity "restricted" admission controller (enforced on every
	// T1 namespace, internal/k8s/provision.go's createNamespace) rejects
	// the Create call outright with "violates PodSecurity restricted:
	// allowPrivilegeEscalation != false... unrestricted capabilities...
	// runAsNonRoot != true... seccompProfile" -- a fake clientset's unit
	// tests never catch this since fake clientsets don't enforce
	// admission control, only a real cluster does. Mirrors
	// internal/k8s/provision.go's createWorkspacePod's own
	// SecurityContext exactly, since every pod in a restricted namespace
	// needs the identical shape regardless of which code creates it.
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "broken-app", Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				RunAsUser:    &runAsUser,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "docker.io/library/busybox:latest",
					Command: []string{"sh", "-c", "echo 'simulated crash: missing required config' >&2; exit 1"},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
				},
			},
		},
	}
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating broken-app pod: %w", err)
	}

	if crashLoopWait <= 0 {
		return nil
	}

	// Wait until the pod is OBSERVABLY, STABLY broken before returning:
	// restartCount >= 2 AND the container is currently in "waiting"
	// (CrashLoopBackOff), not a transient Running blip between crashes.
	//
	// Both conditions matter for lab.k8s.troubleshooting's two null-path
	// validators:
	//   - v.pod-not-restarting asserts restartCount == 0 (fixed state), so
	//     the seeded pod must have restartCount > 0.
	//   - v.pod-running-stable asserts phase == Running (fixed state). A
	//     CrashLoopBackOff pod cycles Running->Error->waiting; returning
	//     while it happens to be in its brief Running phase makes that
	//     validator PASS against the untouched broken env (a content-CI
	//     null-path failure). Waiting for state.Waiting ensures we hand
	//     back a pod that is currently NOT Running.
	//
	// Threshold 2 keeps this ~20-40s (CrashLoopBackOff backoff is
	// 10s,20s,40s...), inside the fixture-apply step's slice of the
	// Provision RPC deadline.
	deadline := time.Now().Add(crashLoopWait)
	for time.Now().Before(deadline) {
		got, getErr := clientset.CoreV1().Pods(namespace).Get(ctx, "broken-app", metav1.GetOptions{})
		if getErr == nil && len(got.Status.ContainerStatuses) > 0 {
			cs := got.Status.ContainerStatuses[0]
			if cs.RestartCount >= 2 && cs.State.Waiting != nil && got.Status.Phase != corev1.PodRunning {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	// Not fatal: the pod IS crash-looping, it just hasn't settled into a
	// stable CrashLoopBackOff in the window (a slow node). The lab is
	// still solvable; log-worthy, not error-worthy.
	return nil
}

// applyNodeAppRepo seeds a minimal Node.js app + Dockerfile into the
// workspace pod's /workspace/app directory -- lab.k8s.deploy-node-app.yaml's
// starting state names this exact path ("The application source is in
// /workspace/app. Build an image tagged node-app:v1."). /workspace (not
// the container's WORKDIR ~/, which resolves to /home/ubuntu -- a
// different, non-persistent location) is the mounted emptyDir volume
// every workspace pod gets (internal/k8s/provision.go's
// createWorkspacePod), so this is the only path choice that matches both
// the activity's own instructions and where files actually persist for
// the attempt's lifetime. A real git clone of an external repo (doc
// §3.2's illustrative fixture description) needs a real content
// repository this platform doesn't have one authored for yet; this
// fixture is the honest subset -- a real, buildable Dockerfile + app
// source written directly, not a placeholder that leaves /workspace/app
// empty.
func applyNodeAppRepo(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	const appJS = `const http = require('http');
const server = http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'text/plain'});
  res.end('Hello from node-app\n');
});
server.listen(3000, () => console.log('listening on 3000'));
`
	const dockerfile = `FROM node:20-alpine
WORKDIR /app
COPY app.js .
EXPOSE 3000
CMD ["node", "app.js"]
`
	writeCmd := fmt.Sprintf(
		"mkdir -p /workspace/app && cat > /workspace/app/app.js <<'FIXTURE_EOF'\n%s\nFIXTURE_EOF\ncat > /workspace/app/Dockerfile <<'FIXTURE_EOF'\n%s\nFIXTURE_EOF\n",
		appJS, dockerfile,
	)
	result, err := validation.ExecShell(ctx, provisioner, envID, writeCmd, 10_000)
	if err != nil {
		return fmt.Errorf("seeding node-app repo: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("seeding node-app repo: exit %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}
