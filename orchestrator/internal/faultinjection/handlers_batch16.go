package faultinjection

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Sixteenth batch: the ArgoCD T2-gated fault, backed by
// fx.argocd-minimal.v1 (internal/fixture/handlers_argocd.go) -- a real
// Argo CD core install (application-controller, repo-server) + a real
// Application syncing a real fx.gitea-repo.v1 repo into this namespace.
// min_tier: T2_ISOLATED_MICROVM in its content YAML; the RPC-level
// tier-precondition check (server.go's InjectFault, gated by
// faultinjection.RequiresT2) is what actually enforces that against
// real callers, same convention as handlers_batch15.go's Istio faults.
func init() {
	registerDynamic("f.gitops.argocd-out-of-sync-manual-drift", applyArgoCDOutOfSyncManualDrift)
}

const (
	argoCDNamespaceConst        = "argocd"
	argoCDAppNameConst          = "practice-app"
	argoCDManagedConfigMapConst = "practice-argocd-managed"
	argoCDManagedFieldKeyConst  = "greeting"
	argoCDDriftedValue          = "hand-edited-not-in-git"
)

var argoCDApplicationGVRFault = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// applyArgoCDOutOfSyncManualDrift: content/faults/f.gitops.argocd-out-of-sync-manual-drift.yaml
// params: application (must be "practice-app", the fixture's logical
// Application name -- the REAL object name is
// "practice-app-<namespace>", per-environment-namespaced since the
// Application CRD itself lives in the single, cluster-wide `argocd`
// namespace and a fixed name would collide across concurrent learner
// environments; see fx.argocd-minimal.v1's argoCDApplicationName's own
// doc comment for the full reasoning).
//
// Real mechanism, matching the content's own framing exactly ("auto-sync
// was previously turned off" is the PRECONDITION, "someone applied a
// manual kubectl change" is the SYMPTOM-PRODUCING action): first removes
// spec.syncPolicy.automated from the real Application (the fixture's
// healthy baseline has it present with selfHeal:true -- leaving it on
// would auto-revert the drift within seconds, defeating the fault
// entirely), then patches the real managed ConfigMap's data directly via
// the K8s API (not a Git commit) to a value Git does NOT declare. With
// auto-sync off, application-controller's own reconciliation loop still
// runs (it's a live process, not disabled) and correctly flags the
// Application OutOfSync on its next diff, but never writes the drift
// back -- the real "silently never self-heals" symptom the fault's
// summary describes.
func applyArgoCDOutOfSyncManualDrift(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	application := params["application"]
	if application == "" {
		return Result{}, fmt.Errorf("f.gitops.argocd-out-of-sync-manual-drift requires param: application")
	}
	if application != argoCDAppNameConst {
		return Result{}, fmt.Errorf("f.gitops.argocd-out-of-sync-manual-drift: application %q does not match the fixture's real application %q", application, argoCDAppNameConst)
	}
	appObjectName := argoCDAppNameConst + "-" + namespace

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		return Result{}, fmt.Errorf("building dynamic client: %w", err)
	}

	app, err := dyn.Resource(argoCDApplicationGVRFault).Namespace(argoCDNamespaceConst).Get(ctx, appObjectName, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("getting application %s: %w", appObjectName, err)
	}
	unstructured.RemoveNestedField(app.Object, "spec", "syncPolicy", "automated")
	if _, err := dyn.Resource(argoCDApplicationGVRFault).Namespace(argoCDNamespaceConst).Update(ctx, app, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("disabling auto-sync on application %s: %w", appObjectName, err)
	}

	clientset := provisioner.Clientset()
	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, argoCDManagedConfigMapConst, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("getting managed configmap %s: %w", argoCDManagedConfigMapConst, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[argoCDManagedFieldKeyConst] = argoCDDriftedValue
	if _, err := clientset.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("applying manual drift to configmap %s: %w", argoCDManagedConfigMapConst, err)
	}

	// Give application-controller's own reconciliation loop (real
	// polling interval, not instant) a real window to observe the drift
	// and mark the Application OutOfSync before verifying.
	deadline := time.Now().Add(60 * time.Second)
	var lastSyncStatus string
	for time.Now().Before(deadline) {
		refreshed, err := dyn.Resource(argoCDApplicationGVRFault).Namespace(argoCDNamespaceConst).Get(ctx, appObjectName, metav1.GetOptions{})
		if err == nil {
			lastSyncStatus, _, _ = unstructured.NestedString(refreshed.Object, "status", "sync", "status")
			if lastSyncStatus == "OutOfSync" {
				return Result{Applied: true, SymptomVerified: true}, nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return Result{Applied: true, SymptomVerified: false}, fmt.Errorf("application did not report OutOfSync within 60s (last status: %s)", lastSyncStatus)
}
