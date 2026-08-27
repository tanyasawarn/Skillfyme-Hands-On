package fixture

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testNamespace = "env-test"

func TestEnsureServiceAccount_NeverAutoMounted(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureServiceAccount(context.Background(), clientset, testNamespace, "learner-workload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sa, err := clientset.CoreV1().ServiceAccounts(testNamespace).Get(context.Background(), "learner-workload", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected SA to exist: %v", err)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("expected AutomountServiceAccountToken=false -- this SA must never be mounted into any pod, only used to mint a token handed to the learner separately (via kubeconfig)")
	}
}

func TestEnsureServiceAccount_Idempotent(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureServiceAccount(context.Background(), clientset, testNamespace, "learner-workload"); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if err := ensureServiceAccount(context.Background(), clientset, testNamespace, "learner-workload"); err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
}

// TestEnsureLearnerWorkloadRole_NeverGrantsRBACAccess is a
// security-relevant boundary check: the learner's Role must never grant
// access to Nodes, Namespaces, RBAC objects, or other cluster-scoped/
// privilege-escalation-relevant resources -- only the fixed,
// namespace-scoped workload resource list this fixture defines.
func TestEnsureLearnerWorkloadRole_NeverGrantsRBACAccess(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureLearnerWorkloadRole(context.Background(), clientset, testNamespace, "learner-workload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	role, err := clientset.RbacV1().Roles(testNamespace).Get(context.Background(), "learner-workload", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected role: %v", err)
	}

	forbiddenResources := map[string]bool{
		"roles":               true,
		"rolebindings":        true,
		"clusterroles":        true,
		"clusterrolebindings": true,
		"serviceaccounts":     true, // a learner must not be able to mint/read tokens for other SAs
		"nodes":               true,
		"namespaces":          true,
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if forbiddenResources[resource] {
				t.Errorf("SECURITY REGRESSION: learner Role grants access to %q, which would let a learner escalate privileges or escape namespace isolation", resource)
			}
		}
	}
}

// TestEnsureLearnerWorkloadRole_GrantsExpectedWorkloadVerbs confirms the
// Role is actually usable for what content's own kubectl instructions
// assume (apply/create/expose/patch/delete), not accidentally
// read-only -- the converse of the security-focused tests above: a Role
// that's TOO restrictive silently breaks every K8s-track lab.
func TestEnsureLearnerWorkloadRole_GrantsExpectedWorkloadVerbs(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureLearnerWorkloadRole(context.Background(), clientset, testNamespace, "learner-workload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	role, err := clientset.RbacV1().Roles(testNamespace).Get(context.Background(), "learner-workload", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected role: %v", err)
	}

	var grantedOnDeployments []string
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "deployments" {
				grantedOnDeployments = append(grantedOnDeployments, rule.Verbs...)
			}
		}
	}
	for _, want := range []string{"create", "update", "patch", "delete", "get", "list"} {
		found := false
		for _, v := range grantedOnDeployments {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected deployments to grant verb %q, got %v", want, grantedOnDeployments)
		}
	}
}

func TestEnsureRoleBinding_UsesNamespaceScopedRoleRef(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureRoleBinding(context.Background(), clientset, testNamespace, "learner-workload", "learner-workload", "learner-workload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	binding, err := clientset.RbacV1().RoleBindings(testNamespace).Get(context.Background(), "learner-workload", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected binding: %v", err)
	}
	if binding.RoleRef.Kind != "Role" {
		t.Errorf("SECURITY REGRESSION: RoleRef.Kind=%q -- must always be Role (namespace-scoped), never ClusterRole", binding.RoleRef.Kind)
	}
}

func TestBuildKubeconfigYAML_ProducesParseableStructure(t *testing.T) {
	yaml := buildKubeconfigYAML("https://k8s.example:6443", []byte("fake-ca-data"), testNamespace, "fake-token-value")

	// Not a full YAML parse (avoids pulling in a YAML dependency just for
	// this test) -- structural substring checks that the fields we
	// control actually made it into the output correctly, which is what
	// a real bug (e.g. a swapped fmt.Sprintf argument) would break.
	for _, want := range []string{
		"server: https://k8s.example:6443",
		"namespace: " + testNamespace,
		"token: fake-token-value",
		"current-context: learner",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("expected kubeconfig to contain %q, got:\n%s", want, yaml)
		}
	}
}

// TestEnsureAPIServerEgressAllowed_UsesIPBlockNotNamespaceSelector is a
// regression test for a real bug caught live against this project's k3s
// cluster: an earlier version of this policy used a namespaceSelector
// peer targeting the `default` namespace, which can only ever match
// egress to POD ips -- but this project's (and most self-managed
// clusters') `kubernetes` Service has no pod backing it at all (its
// Endpoints point at a bare node IP), so a namespaceSelector/podSelector
// peer can never match it regardless of which namespace it names. Only
// ipBlock can express "reach the API server wherever its real endpoint
// lives." A future edit that reintroduces a namespaceSelector/
// podSelector peer here would silently reintroduce the exact
// "connection refused" bug this session found and fixed.
//
// Also asserts NO port restriction on the rule -- a second, narrower
// regression: this k3s's kube-router netpol engine was confirmed live to
// silently drop traffic for an ipBlock peer combined with a Ports
// filter, even though either alone works correctly (see this function's
// doc comment for the full bisection). A future edit that reintroduces
// `Ports: [...port 443...]` here would look more "correctly scoped" but
// would silently reintroduce that exact connection-refused bug against
// this cluster's actual NetworkPolicy engine.
func TestEnsureAPIServerEgressAllowed_UsesIPBlockNotNamespaceSelector(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureAPIServerEgressAllowed(context.Background(), clientset, testNamespace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	policy, err := clientset.NetworkingV1().NetworkPolicies(testNamespace).Get(context.Background(), "allow-k8s-api-server", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected policy: %v", err)
	}
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 1 {
		t.Fatalf("expected exactly one egress rule with one peer, got %+v", policy.Spec.Egress)
	}
	peer := policy.Spec.Egress[0].To[0]
	if peer.IPBlock == nil {
		t.Fatal("SECURITY/CORRECTNESS REGRESSION: expected an ipBlock peer -- namespaceSelector/podSelector peers can never match a node-backed Service endpoint, which is how the real k8s API server is exposed on this and most self-managed clusters")
	}
	if peer.NamespaceSelector != nil || peer.PodSelector != nil {
		t.Error("expected namespaceSelector/podSelector to be unset when using an ipBlock peer")
	}
	if peer.IPBlock.CIDR != "0.0.0.0/0" {
		t.Errorf("expected ipBlock CIDR 0.0.0.0/0 (API server's real address is topology-dependent, not a fixed CIDR this code can predict), got %q", peer.IPBlock.CIDR)
	}
	if len(policy.Spec.Egress[0].Ports) != 0 {
		t.Errorf("CORRECTNESS REGRESSION: expected no port restriction -- this cluster's kube-router netpol engine silently drops traffic for an ipBlock peer combined with a Ports filter (confirmed live), got %+v", policy.Spec.Egress[0].Ports)
	}
}

func TestEnsureAPIServerEgressAllowed_Idempotent(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureAPIServerEgressAllowed(context.Background(), clientset, testNamespace); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if err := ensureAPIServerEgressAllowed(context.Background(), clientset, testNamespace); err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
}

func TestBuildKubeconfigYAML_Base64EncodesCAData(t *testing.T) {
	yaml := buildKubeconfigYAML("https://k8s.example:6443", []byte("fake-ca-data"), testNamespace, "token")
	// The raw CA bytes must never appear verbatim -- only the base64
	// encoding (certificate-authority-data is documented K8s kubeconfig
	// convention as base64, and a caller pasting raw bytes in would
	// produce an unparseable kubeconfig).
	if strings.Contains(yaml, "fake-ca-data") {
		t.Error("expected CA data to be base64-encoded, found raw bytes in output")
	}
	// base64("fake-ca-data") = ZmFrZS1jYS1kYXRh
	if !strings.Contains(yaml, "ZmFrZS1jYS1kYXRh") {
		t.Error("expected base64-encoded CA data in output")
	}
}
