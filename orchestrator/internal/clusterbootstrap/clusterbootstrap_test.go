package clusterbootstrap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Live-infra-gated, same skip convention as
// internal/orchestrator/ownership_rpc_test.go: this package's whole
// point is shelling out to a real kubectl against a real cluster, so
// there is no meaningful fake/mock version of these tests.
func TestApplyManifestURL_InstallsTektonCRDAndBecomesEstablished(t *testing.T) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = "../../../.local/k3s-output/kubeconfig.yaml"
	}
	rc, err := k8s.NewRestConfig(kubeconfig)
	if err != nil {
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(rc)
	if err != nil {
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		t.Skipf("skipping: k8s cluster unreachable (dev stack not running?): %v", err)
	}

	already, err := CRDInstalled(context.Background(), rc, "tasks.tekton.dev")
	if err != nil {
		t.Fatalf("checking CRD installed: %v", err)
	}
	if already {
		t.Skip("skipping: tasks.tekton.dev CRD already installed on this cluster (from a prior run) -- nothing new to prove")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	err = ApplyManifestURL(ctx, rc, "https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml")
	if err != nil {
		t.Fatalf("ApplyManifestURL failed: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer waitCancel()
	if err := WaitForCRDEstablished(waitCtx, rc, "tasks.tekton.dev"); err != nil {
		t.Fatalf("waiting for tasks.tekton.dev to establish: %v", err)
	}

	installed, err := CRDInstalled(context.Background(), rc, "tasks.tekton.dev")
	if err != nil {
		t.Fatalf("re-checking CRD installed: %v", err)
	}
	if !installed {
		t.Fatal("expected tasks.tekton.dev to be installed after ApplyManifestURL + WaitForCRDEstablished")
	}
}
