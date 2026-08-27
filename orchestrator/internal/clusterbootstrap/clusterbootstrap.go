// Package clusterbootstrap installs cluster-scoped controllers/CRDs
// (Tekton Pipelines, Prometheus, Jaeger, ELK, Argo CD, Istio) that
// several of Phase 2's Production Sim fixtures need but that no
// namespace-scoped fixture can create on its own -- a CRD is
// cluster-scoped by definition, and installing a controller's Deployment
// once per cluster (not once per learner namespace) is the correct
// mechanism for shared infrastructure every environment's fixture then
// targets or reuses.
//
// Deliberately shells out to `kubectl apply -f <manifest>` rather than
// hand-rolling a Go dynamic-client YAML-apply mechanism: the orchestrator
// process in this deployment already runs with a real, unrestricted
// kubeconfig (confirmed: a bare local process with client-cert admin
// access, not a restricted in-cluster ServiceAccount -- the same
// kubeconfig `kubectl` itself uses), applying the EXACT manifest each
// tool's own docs recommend rather than a hand-maintained re-derivation
// of it. This makes `kubectl` on PATH a real prerequisite for the
// deployment environment that runs these specific fixtures -- documented
// here and in PHASE2 docs, not hidden.
package clusterbootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// installMu serializes concurrent bootstrap attempts within this
// process -- two environments provisioning concurrently must not both
// run `kubectl apply` for the same cluster-scoped install at once
// (harmless but wasteful, and some installers aren't safe against
// concurrent apply of the same CRDs). A single package-level mutex is
// sufficient: cluster bootstrap is a once-per-cluster-lifetime event in
// practice (CRDInstalled below makes every later call after the first
// success a fast no-op).
var installMu sync.Mutex

// CRDInstalled reports whether crdName already exists -- the idempotency
// check every EnsureX function below runs first, so a repeat call (every
// environment provision after the first, in a long-lived cluster) is a
// single cheap GET, not a re-run of kubectl apply.
func CRDInstalled(ctx context.Context, restConfig *rest.Config, crdName string) (bool, error) {
	client, err := apiextensionsclientset.NewForConfig(restConfig)
	if err != nil {
		return false, fmt.Errorf("building apiextensions client: %w", err)
	}
	_, err = client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking CRD %s: %w", crdName, err)
	}
	return true, nil
}

// ApplyManifestURL runs `kubectl apply -f manifestURL` against the
// cluster restConfig points at. Builds a throwaway kubeconfig file from
// restConfig's own credentials (client-cert or bearer-token, whichever
// the orchestrator process itself was configured with -- same admin
// credentials internal/k8s.NewRestConfig already loaded) rather than
// assuming a KUBECONFIG env var/file exists at a known path, since the
// orchestrator's own Provisioner only carries a parsed *rest.Config, not
// a file path. The temp file is written 0600 and removed before this
// function returns, success or failure. Returns combined stdout+stderr
// on failure for a diagnosable error, never swallows kubectl's own
// output.
func ApplyManifestURL(ctx context.Context, restConfig *rest.Config, manifestURL string) error {
	installMu.Lock()
	defer installMu.Unlock()

	kubeconfigPath, cleanup, err := writeTempKubeconfig(restConfig)
	if err != nil {
		return fmt.Errorf("preparing kubeconfig for kubectl apply: %w", err)
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", manifestURL)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply -f %s failed: %w\noutput:\n%s", manifestURL, err, out.String())
	}
	return nil
}

// ApplyManifestContent is ApplyManifestURL's counterpart for a manifest
// this codebase embeds directly (go:embed'd Go string, e.g.
// internal/manifests.IstioMinimalYAML) rather than fetching from a URL
// -- piped via `kubectl apply -f -` (stdin) so no temp manifest file is
// needed, only the same temp kubeconfig ApplyManifestURL already writes.
func ApplyManifestContent(ctx context.Context, restConfig *rest.Config, manifestYAML string) error {
	installMu.Lock()
	defer installMu.Unlock()

	kubeconfigPath, cleanup, err := writeTempKubeconfig(restConfig)
	if err != nil {
		return fmt.Errorf("preparing kubeconfig for kubectl apply: %w", err)
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifestYAML)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply -f - failed: %w\noutput:\n%s", err, out.String())
	}
	return nil
}

// ApplyManifestContentInNamespace is ApplyManifestContent's counterpart
// for a manifest that relies on `kubectl apply -n <ns>` to supply the
// namespace for objects that omit it themselves (Argo CD's own
// core-install.yaml is written this way -- confirmed by inspection, only
// a single RoleBinding subject hardcodes `namespace: argocd`, every other
// namespaced object has none and expects -n to fill it in, the same
// contract plain `kubectl apply -n argocd -f core-install.yaml` documents
// upstream).
func ApplyManifestContentInNamespace(ctx context.Context, restConfig *rest.Config, namespace, manifestYAML string) error {
	installMu.Lock()
	defer installMu.Unlock()

	kubeconfigPath, cleanup, err := writeTempKubeconfig(restConfig)
	if err != nil {
		return fmt.Errorf("preparing kubeconfig for kubectl apply: %w", err)
	}
	defer cleanup()

	// --server-side: confirmed live as a real fix, not a style choice --
	// plain client-side apply writes the whole applied object into a
	// kubectl.kubernetes.io/last-applied-configuration annotation, and
	// Argo CD's own upstream applicationsets.argoproj.io CRD is large
	// enough that annotation exceeds the API server's 262144-byte
	// annotation-value limit ("Too long: may not be more than 262144
	// bytes"), a real, reproducible failure on this exact manifest.
	// Server-side apply tracks field ownership instead of that
	// annotation, so it has no such size ceiling.
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath, "apply", "--server-side", "--force-conflicts", "-n", namespace, "-f", "-")
	cmd.Stdin = strings.NewReader(manifestYAML)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply --server-side -n %s -f - failed: %w\noutput:\n%s", namespace, err, out.String())
	}
	return nil
}

// writeTempKubeconfig renders restConfig (whatever this orchestrator
// process itself authenticates with -- client-cert or bearer-token) into
// a minimal kubeconfig YAML file kubectl can use directly, in a
// process-private temp directory. Supports both auth styles this
// codebase's own internal/k8s.NewRestConfig can produce (a real
// kubeconfig's client-cert auth for local dev -- confirmed earlier this
// session -- or a bearer token for an in-cluster ServiceAccount
// deployment), since which one applies depends on how the orchestrator
// itself was deployed, not something this package should assume.
func writeTempKubeconfig(restConfig *rest.Config) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "clusterbootstrap-kubeconfig-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	caB64 := base64.StdEncoding.EncodeToString(restConfig.CAData)

	var userBlock string
	switch {
	case len(restConfig.CertData) > 0 && len(restConfig.KeyData) > 0:
		userBlock = fmt.Sprintf(`      client-certificate-data: %s
      client-key-data: %s`,
			base64.StdEncoding.EncodeToString(restConfig.CertData),
			base64.StdEncoding.EncodeToString(restConfig.KeyData))
	case restConfig.BearerToken != "":
		userBlock = fmt.Sprintf(`      token: %s`, restConfig.BearerToken)
	default:
		cleanup()
		return "", nil, fmt.Errorf("rest.Config has neither client-cert nor bearer-token credentials -- cannot build a kubeconfig for kubectl")
	}

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
  - name: cluster
    cluster:
      server: %s
      certificate-authority-data: %s
contexts:
  - name: ctx
    context:
      cluster: cluster
      user: user
current-context: ctx
users:
  - name: user
    user:
%s
`, restConfig.Host, caB64, userBlock)

	path = dir + "/kubeconfig.yaml"
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing temp kubeconfig: %w", err)
	}
	return path, cleanup, nil
}

// WaitForCRDEstablished polls until crdName's Established condition is
// True -- kubectl apply returns as soon as the CRD object is accepted,
// but the API server needs a moment to actually serve the new resource
// type; a fixture that immediately tries to Create a custom resource
// against a CRD that was just applied can hit "no matches for kind"
// without this wait.
func WaitForCRDEstablished(ctx context.Context, restConfig *rest.Config, crdName string) error {
	client, err := apiextensionsclientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building apiextensions client: %w", err)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for CRD %s to establish: %w", crdName, ctx.Err())
		case <-ticker.C:
			crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("checking CRD %s establishment: %w", crdName, err)
			}
			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					return nil
				}
			}
		}
	}
}
