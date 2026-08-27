package fixture

import (
	"context"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Deliberately NOT shared with internal/orchestrator/credentials.go's
// near-identical RBAC-object helpers (ensureReadOnlyServiceAccount etc.)
// -- sharing would require this package to import internal/orchestrator
// (or vice versa), but internal/orchestrator needs to call THIS package
// (Provision()'s fixture-apply step), which would create an import
// cycle. Small, honest duplication of the "ensure a K8s object exists,
// no-op if already there" pattern rather than a forced shared dependency
// across a package boundary the call graph doesn't actually allow.

func ensureServiceAccount(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	autoMount := false
	obj := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace},
		AutomountServiceAccountToken: &autoMount,
	}
	_, err := clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// learnerWorkloadResources: the resource kinds every K8s-track activity's
// instructions/hints actually reference via kubectl (surveyed across
// content/activities/lab.k8s.*.yaml's instructions_md/hints text) --
// Deployments, Services, Pods, ConfigMaps, Secrets, Endpoints for
// read-back, plus ReplicaSets/StatefulSets since kubectl rollout/scale
// operate through them. Deliberately excludes Nodes, Namespaces, and any
// RBAC object -- a learner's own kubectl must never be able to see other
// namespaces, other learners' environments, or grant itself broader
// access than this Role already has.
var learnerWorkloadCoreResources = []string{"pods", "services", "configmaps", "secrets", "endpoints", "events", "persistentvolumeclaims"}
var learnerWorkloadAppsResources = []string{"deployments", "replicasets", "statefulsets", "daemonsets"}

// learnerWorkloadVerbs: full workload-author CRUD (doc's own kubectl
// vocabulary survey: apply/create/expose/patch/set/delete), explicitly
// NOT cluster-admin -- no access to RBAC objects (can't self-escalate),
// no access to any resource outside this fixed list, no access outside
// this one namespace (RoleBinding only, never ClusterRoleBinding -- see
// ensureRoleBinding).
var learnerWorkloadVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

func ensureLearnerWorkloadRole(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: learnerWorkloadCoreResources, Verbs: learnerWorkloadVerbs},
		{APIGroups: []string{"apps"}, Resources: learnerWorkloadAppsResources, Verbs: learnerWorkloadVerbs},
		// pods/log and pods/exec are subresources, listed separately
		// since RBAC treats them as distinct resource names -- without
		// this, "kubectl logs"/"kubectl exec" (both surveyed as
		// content-authored expectations) would 403 even though the
		// learner has get/list/watch on pods themselves.
		{APIGroups: []string{""}, Resources: []string{"pods/log", "pods/exec"}, Verbs: []string{"get", "create"}},
	}
	obj := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Rules:      rules,
	}
	_, err := clientset.RbacV1().Roles(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureRoleBinding(ctx context.Context, clientset kubernetes.Interface, namespace, bindingName, roleName, saName string) error {
	obj := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role", // namespace-scoped, never ClusterRole -- see this file's package doc
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: namespace},
		},
	}
	_, err := clientset.RbacV1().RoleBindings(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// buildKubeconfigYAML renders a minimal, valid kubeconfig authenticating
// via a bearer token, scoped to the given namespace as the default
// context namespace (kubectl commands without an explicit -n still
// resolve to this namespace -- doesn't by itself PREVENT a learner from
// passing -n other-namespace, but the RBAC RoleBinding does: the token
// simply has no permissions there regardless of what namespace the
// command targets). caData is base64-encoded inline (embedded, not a
// separate file reference) so this is a single self-contained file with
// no other files it depends on.
func buildKubeconfigYAML(host string, caData []byte, namespace, token string) string {
	caB64 := base64.StdEncoding.EncodeToString(caData)
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
  - name: practiceengine
    cluster:
      server: %s
      certificate-authority-data: %s
contexts:
  - name: learner
    context:
      cluster: practiceengine
      namespace: %s
      user: learner
current-context: learner
users:
  - name: learner
    user:
      token: %s
`, host, caB64, namespace, token)
}

func int64Ptr(v int64) *int64 { return &v }

// ensureDiscoveryClusterRoleBinding binds the built-in system:discovery
// ClusterRole (read-only access to /api, /apis, /healthz, /openapi --
// NON-resource URLs, no access to any actual K8s resource) to the
// learner ServiceAccount -- kubectl's own first call on every invocation
// is GET /api for API-version discovery, which no namespace-scoped
// Role/RoleBinding can ever grant (confirmed live: this was missing and
// kubectl failed with "Forbidden" on /api even though the real resource
// RBAC below was already correct).
//
// This IS a ClusterRoleBinding, not namespaced like every other RBAC
// object this fixture creates -- unavoidable, since discovery
// permissions are inherently cluster-scoped in K8s's own RBAC model.
// Named per-environment (binding name is "learner-discovery-<namespace>",
// a deterministic pattern internal/k8s.Provisioner.Destroy also knows
// so it can delete this cluster-scoped object explicitly on teardown --
// namespace deletion alone does NOT cascade-delete it, confirmed live
// during this session's testing) so concurrent learners' bindings never
// collide. system:discovery itself grants nothing beyond "can this
// identity see what API groups/versions exist" -- the actual
// resource-level boundary (which pods/services/etc this identity can
// read or write, scoped to exactly one namespace) is still fully
// enforced by the namespace-scoped Role/RoleBinding
// ensureLearnerWorkloadRole/ensureRoleBinding create; this does not
// widen that boundary at all.
func ensureDiscoveryClusterRoleBinding(ctx context.Context, clientset kubernetes.Interface, namespace, saName string) error {
	bindingName := "learner-discovery-" + namespace
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:discovery",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: namespace},
		},
	}
	_, err := clientset.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// ensureAPIServerEgressAllowed adds a NetworkPolicy allowing egress from
// this namespace to the K8s API server -- caught missing by a real live
// test against this project's k3s cluster: the namespace's default-deny
// NetworkPolicy (internal/k8s/provision.go's applyDefaultDenyNetworkPolicy)
// only allow-lists the egress proxy and kube-system DNS, so a learner's
// kubectl (even with a correct kubeconfig) would still be blocked at the
// network layer without this.
//
// Originally written as a namespaceSelector peer targeting the `default`
// namespace (on the theory that the `kubernetes` Service always lives
// there) -- wrong, and a SECOND real bug caught live in this same
// session: NetworkPolicy's namespaceSelector/podSelector peers can only
// ever match pod IPs on the cluster's pod network (that's how the
// NetworkPolicy spec itself defines a peer). This project's k3s topology
// has NO pod backing the `kubernetes` Service at all (`kubectl get
// endpoints kubernetes -n default` shows a bare node IP, e.g.
// 172.18.0.5:6443, not a pod IP) -- confirmed live: with the
// namespaceSelector version of this policy, `kubectl exec ... kubectl
// get pods` failed with a raw "connection refused" from the pod's own IP
// straight to the node IP, even though the identical request succeeded
// from an unrestricted pod in the `default` namespace, isolating the
// NetworkPolicy (not RBAC, not the proxy, both already ruled out earlier
// in this same debugging chain) as the blocker. Any topology where the
// API server is reachable only via a node IP (kubeadm, k3s, kind, most
// non-managed clusters) has this same problem -- a managed cluster (EKS/
// GKE) where the control plane sits behind a real pod-routable path
// wouldn't need this ipBlock fallback, but detecting that distinction at
// runtime isn't worth it for what is already the last-resort, most
// specific rule in this namespace's default-deny egress policy set (see
// applyDefaultDenyNetworkPolicy).
//
// ipBlock 0.0.0.0/0 with NO port restriction -- a THIRD real bug caught
// live in this same session, this time in the cluster's NetworkPolicy
// engine itself, not this code: this project's k3s ships kube-router's
// netpol controller (the embedded default under the flannel backend),
// and bisection confirmed live (toggling port-restriction on and off via
// kubectl-applied test policies while this namespace's other real
// NetworkPolicies stayed in place) that kube-router here correctly
// honors an ipBlock peer alone, and correctly honors a Ports filter
// alone (see allow-egress-proxy's port 3128/53 rules, which do work),
// but silently produces a non-functional iptables rule for the
// *combination* of an ipBlock peer with a Ports filter -- traffic that
// should have been allowed still hit "connection refused." Dropping the
// port restriction here is a deliberate workaround for that engine bug,
// not a correctness choice this code would make against an engine
// without it. This does widen what the rule technically permits (any
// port to 0.0.0.0/0, not just 443), but not the real attack surface: the
// peer was already "anywhere," and a learner's container has no
// credentials or path to originate meaningful outbound traffic other
// than through HTTP_PROXY/HTTPS_PROXY (still routed through Squid's own
// allowlist, doc §5.2's isolation model) or this one direct route to
// wherever the API server's real address is.
func ensureAPIServerEgressAllowed(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-k8s-api-server", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}},
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

// ensureIntraNamespacePodTrafficAllowed adds a NetworkPolicy allowing
// ingress+egress between pods within the SAME namespace. Caught missing
// by a real live test (this session, re-verifying f.ansible.* under the
// real T1 network baseline instead of a bare permissive test namespace):
// the real default-deny + egress-proxy-allowlist pair a T1 environment
// actually gets (internal/k8s/provision.go) allows egress ONLY to the
// Squid proxy, DNS, and (via ensureAPIServerEgressAllowed) the K8s API
// server -- it does NOT allow pods in the same namespace to reach each
// other at all. That's silently fine for single-pod fixtures, but every
// multi-pod fixture (the Ansible runner reaching its own SSH target
// pods, Jenkins agent<->controller, Logstash->Elasticsearch, Prometheus
// scraping its target, Jaeger's frontend/backend/collector chain) needs
// it and was never actually exercised against this real restriction
// until now.
//
// Scoped to same-namespace only (an empty podSelector peer within this
// namespace), not a blanket allow -- each environment's namespace is
// already the real tenant-isolation boundary (default-deny is what
// prevents cross-environment/cross-learner traffic), so pods a single
// environment's own fixtures created are not a security boundary
// between each other the way cross-namespace traffic would be. No port
// restriction, matching ensureAPIServerEgressAllowed's own documented
// reasoning: this uses a plain podSelector peer (not ipBlock), so the
// ipBlock+Ports combination bug documented above doesn't directly apply
// here, but different fixtures use different ports (SSH 2222, Jenkins
// JNLP, Elasticsearch 9200, etc.) and keeping this one rule
// port-agnostic avoids needing a fixture-specific policy per pair.
// PracticeEngineNetworkIsolatedLabel: a pod carrying this label is
// EXCLUDED from ensureIntraNamespacePodTrafficAllowed's blanket allow --
// exported so f.k8s.networkpolicy-overblocks-traffic's own handler
// (internal/faultinjection) can apply it to its fault's target pod(s)
// before/when injecting the fault.
//
// Found necessary live (this session): K8s NetworkPolicy has no deny
// primitive -- every policy selecting a pod is purely additive (union of
// allows). ensureIntraNamespacePodTrafficAllowed's blanket allow, needed
// by every OTHER multi-pod fixture (Ansible, Jenkins, DinD, Gitea,
// Istio, Terraform), silently made
// f.k8s.networkpolicy-overblocks-traffic's own narrower allow-list a
// no-op in any environment where BOTH exist -- the missing-dependency
// pod was still reachable via the blanket rule regardless of what the
// fault's own policy said. The only way for a narrower rule to actually
// bind is for the blanket rule to never have applied to that pod in the
// first place, not for a later rule to "win" (there is no such
// precedence in NetworkPolicy).
const PracticeEngineNetworkIsolatedLabel = "practiceengine.dev/network-isolated"

func ensureIntraNamespacePodTrafficAllowed(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	notIsolated := metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: PracticeEngineNetworkIsolatedLabel, Operator: metav1.LabelSelectorOpDoesNotExist},
		},
	}
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-intra-namespace", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			// Applies to every pod EXCEPT ones explicitly opted out via
			// PracticeEngineNetworkIsolatedLabel -- an isolated pod gets
			// no ingress/egress-allow FROM this rule for ITSELF.
			PodSelector: notIsolated,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			// The peer selector ALSO excludes isolated pods -- excluding
			// them only from PodSelector (above) would stop the rule
			// applying TO an isolated pod, but every OTHER (non-isolated)
			// pod's own egress rule would still list "any pod" as a
			// valid peer and so still be allowed to reach it. Both sides
			// must exclude the isolated pod for it to actually lose
			// blanket reachability in either direction.
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &notIsolated}}},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &notIsolated}}},
			},
		},
	}
	_, err := clientset.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
