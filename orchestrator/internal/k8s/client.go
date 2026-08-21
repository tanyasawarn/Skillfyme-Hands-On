// Package k8s wraps the Kubernetes client-go clientset with the
// namespace-per-environment provisioning logic from doc §5.2. This is the
// T1 (SHARED_CONTAINER) driver referenced by doc §5.1's tier-selection
// rule -- the only tier this Phase 1 slice implements, per PLAN.md's
// "T1 driver only" scope for M1.2.
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewRestConfig builds the raw *rest.Config. It prefers an explicit
// kubeconfig path (local dev against the docker-compose k3s service) and
// falls back to in-cluster config (when the Orchestrator itself runs as
// a pod inside the cluster it manages -- the real deployment target).
// Exposed separately from NewClientset because internal/sessionbroker's
// exec-stream executor (remotecommand.NewSPDYExecutor) needs the raw
// *rest.Config, not just a *kubernetes.Clientset.
func NewRestConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		if _, statErr := os.Stat(kubeconfigPath); statErr != nil {
			return nil, fmt.Errorf("kubeconfig not found at %s: %w", kubeconfigPath, statErr)
		}
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	return rest.InClusterConfig()
}

// NewClientset builds a Kubernetes clientset from an already-resolved
// *rest.Config (see NewRestConfig).
func NewClientsetFromConfig(cfg *rest.Config) (*kubernetes.Clientset, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating k8s clientset: %w", err)
	}
	return clientset, nil
}

// NewClientset is the one-shot convenience form for callers that only
// need the clientset (e.g. internal/k8s.Provisioner, which never touches
// the raw rest.Config).
func NewClientset(kubeconfigPath string) (*kubernetes.Clientset, error) {
	cfg, err := NewRestConfig(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building k8s client config: %w", err)
	}
	return NewClientsetFromConfig(cfg)
}
