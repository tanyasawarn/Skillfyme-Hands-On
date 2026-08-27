package fixture

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// TestArgoCDFixtureAndFault_LiveIntegration is real-infra-gated and
// heavier than most of this package's other live tests -- a real Argo
// CD core install (application-controller, repo-server, once per
// cluster) plus a real fx.gitea-repo.v1 instance as the tracked Git
// source. The fault subtest directly reproduces
// faultinjection.applyArgoCDOutOfSyncManualDrift's own real mutation
// (disable auto-sync, patch the live ConfigMap outside Git) rather than
// cross-importing faultinjection (import cycle -- same convention as
// handlers_istio_test.go), asserting the same observable outcome that
// handler verifies: a real reconciliation loop reports OutOfSync.
//
// T2-gated in its real content YAML; this test exercises the K8s-
// mutation layer directly against a T1-shaped test namespace, matching
// M3's precedent for the Istio pair (Provision(T2) itself fails at K8s
// admission in this environment -- see
// internal/k8s/provision_t2_live_test.go). The RPC-level tier gate
// (server.go's InjectFault, proven separately) is what actually
// prevents this from running for real learners, not a gap in the
// underlying mutation logic this test verifies.
func TestArgoCDFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 420*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-argocd-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyGiteaRepo(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyGiteaRepo failed: %v", err)
	}
	if err := applyArgoCDMinimal(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyArgoCDMinimal failed: %v", err)
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}

	t.Run("f.gitops.argocd-out-of-sync-manual-drift: disabling auto-sync and hand-editing the managed object produces a real, un-self-healed OutOfSync", func(t *testing.T) {
		app, err := dyn.Resource(argoCDApplicationGVR).Namespace(argoCDNamespace).Get(ctx, argoCDApplicationName(ns), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting application: %v", err)
		}
		automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
		if !found || automated["selfHeal"] != true {
			t.Fatalf("expected healthy baseline to have syncPolicy.automated.selfHeal=true, got found=%v automated=%+v", found, automated)
		}

		unstructured.RemoveNestedField(app.Object, "spec", "syncPolicy", "automated")
		if _, err := dyn.Resource(argoCDApplicationGVR).Namespace(argoCDNamespace).Update(ctx, app, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("disabling auto-sync: %v", err)
		}

		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, argoCDManagedConfigMap, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting managed configmap: %v", err)
		}
		if cm.Data[argoCDManagedFieldKey] != argoCDManagedFieldGit {
			t.Fatalf("expected healthy baseline configmap value %q, got %q", argoCDManagedFieldGit, cm.Data[argoCDManagedFieldKey])
		}
		cm.Data[argoCDManagedFieldKey] = "hand-edited-not-in-git"
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("applying manual drift: %v", err)
		}

		deadline := time.Now().Add(60 * time.Second)
		var lastSyncStatus string
		for time.Now().Before(deadline) {
			refreshed, err := dyn.Resource(argoCDApplicationGVR).Namespace(argoCDNamespace).Get(ctx, argoCDApplicationName(ns), metav1.GetOptions{})
			if err == nil {
				lastSyncStatus, _, _ = unstructured.NestedString(refreshed.Object, "status", "sync", "status")
				if lastSyncStatus == "OutOfSync" {
					break
				}
			}
			time.Sleep(3 * time.Second)
		}
		if lastSyncStatus != "OutOfSync" {
			t.Fatalf("REGRESSION: expected application to report OutOfSync after manual drift with auto-sync disabled, last status: %q", lastSyncStatus)
		}

		// Confirm the drift genuinely was NOT self-healed (the real
		// point of this fault) -- if auto-sync were still on, this
		// would already be back to "hello-from-git" by now.
		afterCM, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, argoCDManagedConfigMap, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("re-getting configmap: %v", err)
		}
		if afterCM.Data[argoCDManagedFieldKey] != "hand-edited-not-in-git" {
			t.Fatalf("expected the manual drift to persist (not self-healed), got %q", afterCM.Data[argoCDManagedFieldKey])
		}
	})
}
