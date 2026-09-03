package fixture

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Real-infra-gated, same skip convention as
// internal/orchestrator/ownership_rpc_test.go: fx.tekton-pipeline.v1
// installs a real cluster-wide controller and creates real Tekton custom
// resources -- there is no meaningful fake/mock version of this test.
func setupLiveProvisioner(t *testing.T) *k8s.Provisioner {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = "../../../.local/k3s-output/kubeconfig.yaml"
	}
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
	return k8s.NewProvisioner(clientset, restConfig, k8s.ProvisionerConfig{})
}

// applyRealT1NetworkBaseline replicates the exact NetworkPolicy shape a
// real T1 environment gets from Provisioner.Provision (default-deny +
// egress-proxy allowlist) on a fixture test's own namespace. Fixture
// integration tests historically created a bare namespace with NO
// NetworkPolicy at all, which never exercised the real network-restricted
// conditions a fixture actually runs under in production -- confirmed
// live (this session) that a pod under the real default-deny policy
// can't even resolve DNS, let alone reach a package/provider registry,
// so any fixture step needing internet access (apk/pip/terraform init/
// etc.) needs its target domain in manifests/t1/egress-proxy.yaml's
// Squid allowlist and must be tested against this real baseline, not a
// permissive bare namespace, to mean anything.
func applyRealT1NetworkBaseline(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, ns string) {
	t.Helper()
	if err := provisioner.ApplyDefaultDenyNetworkPolicy(ctx, ns); err != nil {
		t.Fatalf("applying default-deny network policy: %v", err)
	}
	if err := provisioner.ApplyEgressProxyAllowlist(ctx, ns); err != nil {
		t.Fatalf("applying egress-proxy allowlist: %v", err)
	}
	// Real environments get this from fixture.ApplyAll, not Provision
	// itself -- tests calling a fixture handler directly (not through
	// ApplyAll) need it applied explicitly to match.
	if err := ensureIntraNamespacePodTrafficAllowed(ctx, provisioner.Clientset(), ns); err != nil {
		t.Fatalf("allowing intra-namespace pod traffic: %v", err)
	}
}

func TestTektonFixture_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-tekton-test-" + envID[:8]

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

	if err := applyTektonPipeline(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyTektonPipeline failed: %v", err)
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}

	taskGVR := schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}
	task, err := dyn.Resource(taskGVR).Namespace(ns).Get(ctx, "practice-task", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected a real Task named practice-task in %s, got: %v", ns, err)
	}
	workspaces, _, _ := unstructured.NestedSlice(task.Object, "spec", "workspaces")
	if len(workspaces) != 1 {
		t.Fatalf("expected Task to declare exactly 1 workspace, got %d", len(workspaces))
	}

	pvc, err := clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "practice-task-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected a real PVC practice-task-workspace in %s, got: %v", ns, err)
	}
	if pvc.Name != "practice-task-workspace" {
		t.Fatalf("unexpected PVC name: %s", pvc.Name)
	}

	taskRunGVR := schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
	healthyRun, err := dyn.Resource(taskRunGVR).Namespace(ns).Get(ctx, "practice-taskrun-healthy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected a real healthy TaskRun in %s, got: %v", ns, err)
	}
	healthyWorkspaces, _, _ := unstructured.NestedSlice(healthyRun.Object, "spec", "workspaces")
	if len(healthyWorkspaces) != 1 {
		t.Fatalf("expected the healthy TaskRun to have its workspace bound, got %d bindings", len(healthyWorkspaces))
	}

	// Idempotency: re-applying the fixture must not error or duplicate
	// objects (Create against an already-existing object is treated as
	// success via apierrors.IsAlreadyExists throughout the handler).
	if err := applyTektonPipeline(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("re-applying fx.tekton-pipeline.v1 should be idempotent, got: %v", err)
	}
}

func TestTektonFaultInjection_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-tekton-fault-test-" + envID[:8]

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

	if err := applyTektonPipeline(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("seeding fx.tekton-pipeline.v1 baseline failed: %v", err)
	}

	// This is a white-box import of faultinjection's real handler
	// function would create an import cycle (faultinjection already
	// depends on k8s, not fixture) -- instead this test directly
	// reproduces the fault's own real mutation via the same dynamic
	// client mechanism, asserting the SAME observable outcome
	// faultinjection.applyTektonTaskMissingWorkspaceBinding produces
	// against this real fixture baseline: a new TaskRun referencing the
	// real Task but with no workspace binding.
	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}
	taskRunGVR := schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tekton.dev/v1",
		"kind":       "TaskRun",
		"metadata": map[string]any{
			"name":      "practice-taskrun-broken",
			"namespace": ns,
		},
		"spec": map[string]any{
			"taskRef": map[string]any{"name": "practice-task"},
		},
	}}
	if _, err := dyn.Resource(taskRunGVR).Namespace(ns).Create(ctx, broken, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating broken TaskRun against the real fixture baseline: %v", err)
	}

	// Give Tekton's controller a moment to reconcile the TaskRun and
	// report a failure condition for the missing workspace binding.
	deadline := time.Now().Add(30 * time.Second)
	var conditions []any
	for time.Now().Before(deadline) {
		run, err := dyn.Resource(taskRunGVR).Namespace(ns).Get(ctx, "practice-taskrun-broken", metav1.GetOptions{})
		if err == nil {
			conditions, _, _ = unstructured.NestedSlice(run.Object, "status", "conditions")
			if len(conditions) > 0 {
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if len(conditions) == 0 {
		t.Fatal("expected Tekton's controller to report a status condition for the broken TaskRun within 30s")
	}
	cond, ok := conditions[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected condition shape: %+v", conditions[0])
	}
	if cond["status"] == "True" {
		t.Fatalf("expected the workspace-less TaskRun to FAIL (not succeed) reconciliation, condition: %+v", cond)
	}
}
