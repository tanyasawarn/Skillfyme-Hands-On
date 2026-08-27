package fixture

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/clusterbootstrap"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/manifests"
)

func init() {
	register("fx.argocd-minimal.v1", applyArgoCDMinimal)
	registerChecksum("fx.argocd-minimal.v1", "v1")
}

const (
	argoCDNamespace         = "argocd"
	argoCDAppControllerName = "argocd-application-controller"
	argoCDManagedConfigMap  = "practice-argocd-managed"
	argoCDAppName           = "practice-app"
	// argoCDManagedFieldKey is the ConfigMap key Git declares a value
	// for -- the fault mutates this key's live value directly (kubectl
	// patch, no Git commit), producing real drift for a real
	// application-controller reconciliation loop to detect.
	argoCDManagedFieldKey = "greeting"
	argoCDManagedFieldGit = "hello-from-git"
)

// argoCDApplicationName returns a per-environment-namespaced Application
// name -- confirmed by inspection as a real, necessary fix (not
// discovered live, since this dev environment only ever exercises one
// learner namespace against fx.argocd-minimal.v1 at a time so far): the
// Application CRD lives in the single, cluster-wide `argocd` namespace,
// not per-learner, so a fixed name ("practice-app") would let a SECOND
// concurrent environment's Create silently no-op on IsAlreadyExists
// against the FIRST environment's own Application object (still pointed
// at the first environment's destination.namespace) -- the second
// learner's fixture apply would report success while actually never
// creating anything for their own namespace, a real cross-tenant
// correctness gap in the same spirit as the egress-proxy fault's own
// earlier blast-radius fix (see deferred.go's own history of that fix).
func argoCDApplicationName(namespace string) string {
	return argoCDAppName + "-" + namespace
}

var argoCDApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
var argoCDAppProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// applyArgoCDMinimal is fx.argocd-minimal.v1: installs a real Argo CD
// "core install" (application-controller, repo-server, redis,
// applicationset-controller, and the Application/ApplicationSet/
// AppProject CRDs -- no argocd-server/dex/UI, see
// internal/manifests.ArgoCDCoreInstallYAML's own header) once per
// cluster into a real `argocd` namespace, same cluster-wide-singleton
// pattern as fx.istio-minimal.v1's istiod (both are genuine control-
// plane installs, not something a single learner namespace should pay
// to reinstall) -- then creates a real `Application` object pointing at
// THIS environment's own fx.gitea-repo.v1 instance (must be seeded
// first; see this activity's own seed: ordering) as the Git source and
// this namespace as the sync destination.
//
// `argocd` namespace deliberately gets NO NetworkPolicy of its own --
// confirmed live this session that cluster-infra namespaces
// (istio-system, tekton-pipelines) already run with none (only
// per-learner environment namespaces get the default-deny baseline via
// internal/k8s/provision.go's applyDefaultDenyNetworkPolicy), so
// repo-server's own egress to this namespace's Gitea and
// application-controller's egress to the K8s API are both unrestricted
// by default, no extra rule needed on that side. The learner's own
// namespace already gets ensureAPIServerEgressAllowed's 0.0.0.0/0
// egress rule from other fixtures in the same seed list (Istio,
// Terraform) -- reused here too since it also covers reaching argocd's
// pods, not just the K8s API server itself.
//
// Backs f.gitops.argocd-out-of-sync-manual-drift (T2-gated, min_tier:
// T2_ISOLATED_MICROVM) -- same "handler is real and live-verified
// against a T1-shaped test namespace directly, RPC-level tier gate
// (faultinjection.RequiresT2) is what actually prevents this from
// running for real learners until a Kata-capable environment exists"
// precedent as fx.istio-minimal.v1's two faults.
func applyArgoCDMinimal(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	restConfig := provisioner.RestConfig()
	clientset := provisioner.Clientset()

	if err := ensureAPIServerEgressAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing egress to argocd/gitea: %w", err)
	}

	if err := ensureArgoCDInstalled(ctx, restConfig, clientset); err != nil {
		return fmt.Errorf("ensuring argocd core install: %w", err)
	}

	if _, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+giteaDeployment, 120*time.Second); err != nil {
		return fmt.Errorf("waiting for gitea pod (fx.gitea-repo.v1 must be seeded before fx.argocd-minimal.v1): %w", err)
	}
	runnerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+giteaRunnerDeployment, 90*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for gitea runner pod: %w", err)
	}

	if err := ensureGitDeclaredManifestPushed(ctx, provisioner, namespace, runnerPod); err != nil {
		return fmt.Errorf("pushing git-declared manifest: %w", err)
	}

	if err := ensureGiteaIngressFromArgoCDAllowed(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("allowing argocd repo-server ingress to gitea: %w", err)
	}

	if err := ensureArgoCDApplication(ctx, restConfig, namespace); err != nil {
		return fmt.Errorf("ensuring argocd application: %w", err)
	}

	if err := waitForArgoCDSyncedHealthy(ctx, restConfig, namespace, 180*time.Second); err != nil {
		return fmt.Errorf("waiting for initial sync: %w", err)
	}

	// Confirm the real managed object landed with Git's own value --
	// proves the controller actually reconciled, not just that the
	// Application object exists.
	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, argoCDManagedConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verifying managed configmap synced: %w", err)
	}
	if cm.Data[argoCDManagedFieldKey] != argoCDManagedFieldGit {
		return fmt.Errorf("managed configmap synced but has unexpected value %q for key %q", cm.Data[argoCDManagedFieldKey], argoCDManagedFieldKey)
	}
	return nil
}

// ensureGiteaIngressFromArgoCDAllowed adds a real, narrowly-scoped
// NetworkPolicy INGRESS rule on the Gitea pod specifically, allowing
// traffic from the `argocd` namespace -- confirmed live as a real,
// necessary gap distinct from every other cross-namespace fixture this
// session built (Istio, Terraform): those all needed the LEARNER
// namespace's own EGRESS opened (covered by ensureAPIServerEgressAllowed's
// existing 0.0.0.0/0 rule), but here the traffic direction is reversed
// -- argocd-repo-server (living in the OTHER namespace, `argocd`) is the
// one initiating the connection INTO this namespace's Gitea pod, which
// this namespace's own real default-deny INGRESS baseline
// (k8s.Provisioner.ApplyDefaultDenyNetworkPolicy, applied to every real
// T1 environment) blocks by default -- confirmed live: repo-server's own
// sync attempt failed with a real "connect: connection refused" (not a
// DNS or credentials problem, both already ruled out) until this rule
// existed. Uses a namespaceSelector matching the argocd namespace's own
// automatic `kubernetes.io/metadata.name=argocd` label (present on every
// namespace by default since Kubernetes 1.21, confirmed live on this
// cluster) rather than requiring argocd's own namespace object to carry
// a custom label.
func ensureGiteaIngressFromArgoCDAllowed(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	tcpProtocol := corev1.ProtocolTCP
	giteaPort := intstr.FromInt32(3000)
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-argocd-ingress-to-gitea", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": giteaDeployment}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": argoCDNamespace}}},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: &giteaPort},
					},
				},
			},
		},
	}
	_, err := clientset.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// ensureArgoCDInstalled installs the cluster-wide Argo CD core install
// once, idempotently -- same CRD-existence-is-not-enough lesson learned
// live on fx.istio-minimal.v1 (CRDs are cluster-scoped and can survive a
// manual `kubectl delete ns argocd`), so this also checks for the real
// application-controller StatefulSet before skipping the install step.
func ensureArgoCDInstalled(ctx context.Context, restConfig *rest.Config, clientset *kubernetes.Clientset) error {
	crdInstalled, err := clusterbootstrap.CRDInstalled(ctx, restConfig, "applications.argoproj.io")
	if err != nil {
		return fmt.Errorf("checking argocd CRDs installed: %w", err)
	}
	_, controllerErr := clientset.AppsV1().StatefulSets(argoCDNamespace).Get(ctx, argoCDAppControllerName, metav1.GetOptions{})
	installed := crdInstalled && controllerErr == nil
	if !installed {
		if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: argoCDNamespace},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating argocd namespace: %w", err)
		}

		installCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
		defer cancel()
		if err := clusterbootstrap.ApplyManifestContentInNamespace(installCtx, restConfig, argoCDNamespace, manifests.ArgoCDCoreInstallYAML); err != nil {
			return fmt.Errorf("installing argocd core: %w", err)
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
		defer waitCancel()
		if err := clusterbootstrap.WaitForCRDEstablished(waitCtx, restConfig, "applications.argoproj.io"); err != nil {
			return fmt.Errorf("waiting for argocd CRDs to establish: %w", err)
		}
	}
	if err := waitForArgoCDControllerReady(ctx, clientset); err != nil {
		return err
	}
	return ensureDefaultAppProject(ctx, restConfig)
}

// ensureDefaultAppProject creates the `default` AppProject -- confirmed
// live as a real, necessary step: unlike the full install (which seeds
// it via argocd-server's own bootstrap-on-first-run logic), core-install
// mode installs ONLY the CRDs/controllers, no default AppProject object.
// Without one, any Application referencing project: default (this
// fixture's own Application, and the conventional default for anyone
// not using AppProjects deliberately) fails K8s-level admission into a
// usable state with a real, permanent
// "Application referencing project which does not exist" InvalidSpecError
// -- confirmed live via `kubectl get application -o jsonpath=
// '{.status.conditions}'` showing exactly that message, sync/health
// stuck at Unknown/Unknown indefinitely.
func ensureDefaultAppProject(ctx context.Context, restConfig *rest.Config) error {
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AppProject",
		"metadata":   map[string]any{"name": "default", "namespace": argoCDNamespace},
		"spec": map[string]any{
			"sourceRepos":  []any{"*"},
			"destinations": []any{map[string]any{"server": "*", "namespace": "*"}},
			"clusterResourceWhitelist": []any{
				map[string]any{"group": "*", "kind": "*"},
			},
		},
	}}
	if _, err := dyn.Resource(argoCDAppProjectGVR).Namespace(argoCDNamespace).Create(ctx, project, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default AppProject: %w", err)
	}
	return nil
}

// waitForArgoCDControllerReady polls the application-controller
// StatefulSet and the repo-server Deployment (the two components a real
// sync actually depends on -- redis and applicationset-controller aren't
// on this fault's own critical path).
func waitForArgoCDControllerReady(ctx context.Context, clientset *kubernetes.Clientset) error {
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		sts, stsErr := clientset.AppsV1().StatefulSets(argoCDNamespace).Get(ctx, argoCDAppControllerName, metav1.GetOptions{})
		repo, repoErr := clientset.AppsV1().Deployments(argoCDNamespace).Get(ctx, "argocd-repo-server", metav1.GetOptions{})
		if stsErr == nil && repoErr == nil && sts.Status.ReadyReplicas > 0 && repo.Status.ReadyReplicas > 0 {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("argocd application-controller/repo-server not ready after 150s")
}

// ensureGitDeclaredManifestPushed pushes a small K8s manifest (a
// ConfigMap with a known key/value) into fx.gitea-repo.v1's own repo, at
// a fixed path this Application's spec.source.path points at -- reuses
// the exact same runner-pod git-push mechanism
// ensureGiteaHealthyBaselinePush already established and live-verified
// this session (giteaLegitCIUser is on the push whitelist, so this
// works even after f.gitlab.protected-branch-blocks-push's own fault is
// also applied in the same environment).
func ensureGitDeclaredManifestPushed(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod string) error {
	manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
data:
  %s: %s
`, argoCDManagedConfigMap, argoCDManagedFieldKey, argoCDManagedFieldGit)

	cmd := fmt.Sprintf(`
set -e
rm -rf /work/argocd-manifests
mkdir -p /work/argocd-manifests
cd /work/argocd-manifests
git clone -q http://%s:%s@%s:3000/%s/%s.git . 2>&1
git config user.email "%s@example.com"
git config user.name "%s"
mkdir -p manifests
cat > manifests/configmap.yaml <<'EOF'
%s
EOF
git add manifests/configmap.yaml
git commit -q -m "chore: declare practice-argocd-managed configmap" --allow-empty
git push origin %s 2>&1
`, giteaLegitCIUser, giteaLegitCIPass, giteaDeployment, giteaRepoOwner, giteaRepoName,
		giteaLegitCIUser, giteaLegitCIUser, manifest, giteaProtectedBranch)

	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", cmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("pushing git-declared manifest: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("pushing git-declared manifest failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}
	return nil
}

// ensureArgoCDApplication creates a real Application object (auto-sync
// + self-heal enabled, matching the fault's own content: "auto-sync was
// previously turned off" implies the HEALTHY baseline this fixture must
// establish has it ON) pointing at fx.gitea-repo.v1's own in-namespace
// repo via its cluster-DNS FQDN (argocd's repo-server pod lives in a
// DIFFERENT namespace, so a short service name would not resolve) and
// this namespace as the sync destination.
func ensureArgoCDApplication(ctx context.Context, restConfig *rest.Config, namespace string) error {
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	repoURL := fmt.Sprintf("http://%s:%s@%s.%s.svc.cluster.local:3000/%s/%s.git",
		giteaLegitCIUser, giteaLegitCIPass, giteaDeployment, namespace, giteaRepoOwner, giteaRepoName)

	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": argoCDApplicationName(namespace), "namespace": argoCDNamespace},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        repoURL,
				"targetRevision": giteaProtectedBranch,
				"path":           "manifests",
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": namespace,
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{
					"selfHeal": true,
					"prune":    true,
				},
			},
		},
	}}
	if _, err := dyn.Resource(argoCDApplicationGVR).Namespace(argoCDNamespace).Create(ctx, app, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating application: %w", err)
	}
	return nil
}

// waitForArgoCDSyncedHealthy polls the Application's own status.sync and
// status.health fields (the same fields the fault's own re-scoped
// diagnostic path reads via `kubectl get application -o yaml` -- see
// content/faults/f.gitops.argocd-out-of-sync-manual-drift.yaml's v2
// comment) for Synced+Healthy.
func waitForArgoCDSyncedHealthy(ctx context.Context, restConfig *rest.Config, namespace string, timeout time.Duration) error {
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		obj, err := dyn.Resource(argoCDApplicationGVR).Namespace(argoCDNamespace).Get(ctx, argoCDApplicationName(namespace), metav1.GetOptions{})
		if err == nil {
			syncStatus, _, _ := unstructured.NestedString(obj.Object, "status", "sync", "status")
			healthStatus, _, _ := unstructured.NestedString(obj.Object, "status", "health", "status")
			lastStatus = fmt.Sprintf("sync=%s health=%s", syncStatus, healthStatus)
			if syncStatus == "Synced" && healthStatus == "Healthy" {
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("application not Synced+Healthy after %s (last status: %s)", timeout, lastStatus)
}
