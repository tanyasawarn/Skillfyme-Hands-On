package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// parsedKubectlGet is the subset of `kubectl get <resource>[/<name>|
// <name>]` this package needs -- the authored content only ever uses
// `get`, never other verbs, and always targets a single named resource
// (see content/activities/*.yaml's K8S_ASSERT run fields).
type parsedKubectlGet struct {
	Kind string // "pod", "svc", "deploy", "hpa", "networkpolicy", etc. (as written by content authors)
	Name string
}

// kubectlGetRe matches `kubectl get <kind> <name>` or `kubectl get
// <kind>/<name>`, tolerating the flags (-n, -o jsonpath=...) that follow
// -- those flags are ignored here since expect.jsonpath (not the -o flag)
// is what this package actually evaluates against the fetched object.
var kubectlGetRe = regexp.MustCompile(`^kubectl\s+get\s+([a-zA-Z.]+)(?:/|\s+)([a-zA-Z0-9._-]+)`)

func parseKubectlCommand(cmd string) (parsedKubectlGet, error) {
	cmd = strings.TrimSpace(cmd)
	m := kubectlGetRe.FindStringSubmatch(cmd)
	if m == nil {
		return parsedKubectlGet{}, fmt.Errorf("K8S_ASSERT run must be a `kubectl get <kind> <name>` command, got: %q", cmd)
	}
	return parsedKubectlGet{Kind: strings.ToLower(m[1]), Name: m[2]}, nil
}

// resourceKindAliases maps every kubectl short-name/plural/singular form
// that appears in content/activities' authored K8S_ASSERT validators to
// a canonical key used by fetchResourceAsMap's switch. Content authors
// write natural kubectl shorthand (svc, deploy, hpa, ns) -- this is the
// same alias table kubectl itself effectively has, scoped to only the
// kinds this package's faults/validators actually reference.
var resourceKindAliases = map[string]string{
	"pod": "pod", "pods": "pod", "po": "pod",
	"deployment": "deployment", "deployments": "deployment", "deploy": "deployment",
	"service": "service", "services": "service", "svc": "service",
	"configmap": "configmap", "configmaps": "configmap", "cm": "configmap",
	"secret": "secret", "secrets": "secret",
	"statefulset": "statefulset", "statefulsets": "statefulset", "sts": "statefulset",
	"persistentvolumeclaim": "pvc", "persistentvolumeclaims": "pvc", "pvc": "pvc",
	"resourcequota": "resourcequota", "resourcequotas": "resourcequota", "quota": "resourcequota",
	"horizontalpodautoscaler": "hpa", "horizontalpodautoscalers": "hpa", "hpa": "hpa",
	"node": "node", "nodes": "node", "no": "node",
	"namespace": "namespace", "namespaces": "namespace", "ns": "namespace",
	"endpoints": "endpoints", "ep": "endpoints",
}

// fetchResourceAsMap gets one resource by kind+name and converts it to a
// generic map so evalJSONPath can walk it the same way it walks parsed
// SHELL_JSON output -- one jsonpath evaluator serves both validator types.
func fetchResourceAsMap(ctx context.Context, provisioner *k8s.Provisioner, namespace string, parsed parsedKubectlGet) (any, error) {
	kind, ok := resourceKindAliases[parsed.Kind]
	if !ok {
		return nil, fmt.Errorf("unsupported K8S_ASSERT resource kind %q (not in the supported alias table)", parsed.Kind)
	}

	clientset := provisioner.Clientset()
	var obj runtime.Object
	var err error

	switch kind {
	case "pod":
		obj, err = clientset.CoreV1().Pods(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "deployment":
		obj, err = clientset.AppsV1().Deployments(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "service":
		obj, err = clientset.CoreV1().Services(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "configmap":
		obj, err = clientset.CoreV1().ConfigMaps(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "secret":
		obj, err = clientset.CoreV1().Secrets(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "statefulset":
		obj, err = clientset.AppsV1().StatefulSets(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "pvc":
		obj, err = clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "resourcequota":
		obj, err = clientset.CoreV1().ResourceQuotas(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "hpa":
		obj, err = clientset.AutoscalingV1().HorizontalPodAutoscalers(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	case "node":
		obj, err = clientset.CoreV1().Nodes().Get(ctx, parsed.Name, metav1.GetOptions{}) // cluster-scoped, namespace ignored
	case "namespace":
		obj, err = clientset.CoreV1().Namespaces().Get(ctx, parsed.Name, metav1.GetOptions{}) // cluster-scoped
	case "endpoints":
		obj, err = clientset.CoreV1().Endpoints(namespace).Get(ctx, parsed.Name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}

	// Round-trip through JSON so jsonpath.Execute sees plain
	// map[string]interface{} the same way it would for SHELL_JSON's
	// parsed file content -- the same evaluator handles both.
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("encoding fetched resource: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decoding fetched resource: %w", err)
	}
	return generic, nil
}
