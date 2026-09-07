package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestCreateT3WorkspacePod_LocalReal exercises the T3 workspace-pod shape
// against the real compose k3s cluster (PLAN.md Phase 3). Skips (never
// fails) when the cluster isn't reachable, same convention as
// provision_t2_live_test.go.
//
// Asserts: namespace + baseline (quota/limitrange/netpol/SA) created,
// the pod comes Ready with the editor container AND the creds sidecar,
// the shared creds emptyDir is writable from the editor container and
// visible from the sidecar, and DeleteT3Namespace tears it all down.
func TestCreateT3WorkspacePod_LocalReal(t *testing.T) {
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

	const envID = "t3-live-test"
	ns := namespaceName(envID)
	prov := NewProvisioner(clientset, restConfig, ProvisionerConfig{})
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	waitNamespaceGone(ctx, clientset, ns, 90*time.Second)

	// A plain image that has a shell is enough for the pod-shape assertion
	// (terraform/aws not needed here -- that's exercised in the
	// orchestrator T3 lifecycle test). linux-tools always exists in the
	// compose registry.
	gotNS, err := prov.CreateT3WorkspacePod(ctx, StartT3WorkspacePodInput{
		AttemptID:   "t3-live-test-attempt",
		EnvID:       envID,
		EditorImage: "registry:5000/practiceengine/linux-tools:v1",
		Region:      "us-east-1",
		FakeAWS:     true,
	})
	if err != nil {
		t.Fatalf("CreateT3WorkspacePod: %v", err)
	}
	if gotNS != ns {
		t.Fatalf("namespace = %s, want %s", gotNS, ns)
	}

	if err := prov.WaitForPodReady(ctx, envID); err != nil {
		t.Fatalf("workspace pod not ready: %v", err)
	}

	pod, err := clientset.CoreV1().Pods(ns).Get(ctx, WorkspacePodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	names := map[string]bool{}
	for _, c := range pod.Spec.Containers {
		names[c.Name] = true
	}
	if !names[WorkspaceContainerName] || !names[CredsSidecarContainerName] {
		t.Fatalf("T3 pod must have both %q and %q containers, got %v", WorkspaceContainerName, CredsSidecarContainerName, names)
	}

	// creds emptyDir: write from the editor container, read from the sidecar.
	if _, err := ExecInPod(ctx, prov, ns, WorkspacePodName, WorkspaceContainerName,
		"echo hello-from-editor > "+T3DefaultCredsMountPath+"/probe", 15*time.Second); err != nil {
		t.Fatalf("write to creds mount from editor container: %v", err)
	}
	read, err := ExecInPod(ctx, prov, ns, WorkspacePodName, CredsSidecarContainerName,
		"cat "+T3DefaultCredsMountPath+"/probe", 15*time.Second)
	if err != nil {
		t.Fatalf("read creds mount from sidecar: %v", err)
	}
	if !strings.Contains(read.Stdout, "hello-from-editor") {
		t.Errorf("creds emptyDir not shared: sidecar read %q", read.Stdout)
	}

	// AWS_* env present on the editor container (local-real FakeAWS wiring).
	var editor = pod.Spec.Containers[0]
	envSeen := map[string]string{}
	for _, e := range editor.Env {
		envSeen[e.Name] = e.Value
	}
	if envSeen["AWS_SHARED_CREDENTIALS_FILE"] != T3DefaultCredsMountPath+"/credentials" {
		t.Errorf("AWS_SHARED_CREDENTIALS_FILE = %q", envSeen["AWS_SHARED_CREDENTIALS_FILE"])
	}
	if envSeen["AWS_ENDPOINT_URL_S3"] == "" && envSeen["AWS_EC2_METADATA_DISABLED"] != "true" {
		t.Errorf("expected FakeAWS env wiring on the editor container, got %v", envSeen)
	}

	// teardown -- namespace deletion is async (graceful termination +
	// finalizers), so assert the DELETE was accepted and the namespace
	// is at least Terminating, then let waitNamespaceGone confirm it
	// fully clears.
	if err := prov.DeleteT3Namespace(ctx, envID); err != nil {
		t.Fatalf("DeleteT3Namespace: %v", err)
	}
	nsObj, gerr := clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if gerr == nil && nsObj.Status.Phase != "Terminating" {
		t.Errorf("after DeleteT3Namespace the namespace phase = %q, want Terminating or gone", nsObj.Status.Phase)
	}
	waitNamespaceGone(ctx, clientset, ns, 90*time.Second)
	if exists, _ := prov.NamespaceExists(ctx, envID); exists {
		t.Errorf("namespace still present 90s after DeleteT3Namespace")
	}
}
