package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CredentialStore holds minted validator credentials server-side, keyed
// by the opaque ref MintCredentialsResponse.credential_ref returns --
// contracts/orchestrator.proto's own field comment is explicit that the
// raw token is "never the raw secret in logs," which this design honors
// literally: the real token is generated, stored here, and only the ref
// ever crosses the RPC boundary or hits a log line. There is currently no
// RPC in orchestrator.proto that exchanges a ref back for the real token
// (ExecValidator, the actual validator-execution path, doesn't use this
// mechanism at all -- see mintValidatorCredential's doc comment) --
// adding one is a contracts/ change gated on joint review per PLAN.md's
// cross-cutting ownership rules, out of scope here. This store still
// makes the minting side real and internally consistent: a real K8s
// ServiceAccount token is genuinely created and held, not a fake
// reference pointing at nothing, so a future resolve-by-ref RPC could be
// added as a pure additive change without redoing this half.
type CredentialStore struct {
	mu      sync.Mutex
	entries map[string]credentialEntry
}

type credentialEntry struct {
	token     string
	expiresAt time.Time
}

func NewCredentialStore() *CredentialStore {
	return &CredentialStore{entries: make(map[string]credentialEntry)}
}

// Put stores a minted token under ref, expiring at expiresAt. Overwriting
// an existing ref is a caller bug (refs are UUIDs, doc §6.2 "minted per
// run") -- panics rather than silently discarding one live credential in
// favor of another, since that would be a real security-relevant surprise
// (a caller holding the old ref would silently start authenticating as
// something else).
func (s *CredentialStore) Put(ref, token string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[ref]; exists {
		panic(fmt.Sprintf("CredentialStore: ref %s already has a stored credential -- refs must be unique per mint", ref))
	}
	s.entries[ref] = credentialEntry{token: token, expiresAt: expiresAt}
}

// Resolve returns the real token for ref, or ("", false) if the ref is
// unknown or has expired. Expired entries are evicted lazily on lookup
// rather than via a background sweep -- this store's entry count is
// bounded by concurrent-attempt volume (one entry per in-flight
// MintValidatorCredentials call, TTL default 300s per doc §6.2), not
// large enough to need proactive GC for a Phase 1/2 deployment.
func (s *CredentialStore) Resolve(ref string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[ref]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		delete(s.entries, ref)
		return "", false
	}
	return entry.token, true
}

// mintValidatorCredential is the real implementation behind
// MintValidatorCredentials -- doc §6.2: "minted per run with a 5-minute
// lifetime... a scoped K8s exec token good only for read-only operations
// inside the one environment's namespace." Previously a stub returning a
// random UUID with nothing behind it; this mints a real, short-lived K8s
// ServiceAccount token via the TokenRequest API, scoped by a read-only
// Role bound only within the target namespace.
//
// Honest framing (see this file's own tests and MintValidatorCredentials'
// doc comment in server.go): ExecValidator, the actual validator-execution
// path this orchestrator uses today, does NOT consume this credential --
// it runs entirely inside the orchestrator process using the
// orchestrator's own cluster-wide client, an intentional shortcut around
// the doc's original design (a separate, externally-credentialed
// Validator Runner process). This function makes MintValidatorCredentials
// mint something real and usable by a caller who DOES present it to the
// K8s API directly, rather than a fake reference with nothing behind it
// -- closing the "stub returns fake data" gap even though the consuming
// side of the doc's original two-sided design isn't built.
func mintValidatorCredential(ctx context.Context, clientset kubernetes.Interface, namespace, envID string, ttlSeconds int64, scopes []string) (token string, err error) {
	saName := "validator-readonly"
	roleName := "validator-readonly"
	bindingName := "validator-readonly"

	if err := ensureReadOnlyServiceAccount(ctx, clientset, namespace, saName); err != nil {
		return "", fmt.Errorf("ensuring validator ServiceAccount: %w", err)
	}
	if err := ensureReadOnlyRole(ctx, clientset, namespace, roleName, scopes); err != nil {
		return "", fmt.Errorf("ensuring validator Role: %w", err)
	}
	if err := ensureReadOnlyRoleBinding(ctx, clientset, namespace, bindingName, roleName, saName); err != nil {
		return "", fmt.Errorf("ensuring validator RoleBinding: %w", err)
	}

	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			// ExpirationSeconds bounds the token's real lifetime at the
			// K8s API server level -- this is not merely an
			// advisory/cosmetic TTL the caller could ignore; a request
			// against this token after it expires is rejected by the
			// API server's own authentication layer, independent of
			// anything this orchestrator does afterward.
			ExpirationSeconds: &ttlSeconds,
			// Audiences deliberately left unset (defaults to the
			// cluster's default audience) -- this token is meant to
			// authenticate directly against this cluster's own API
			// server, not to be presented to an external service that
			// would need audience-scoping.
		},
	}
	result, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, saName, tokenReq, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("requesting token for ServiceAccount %s/%s: %w", namespace, saName, err)
	}
	return result.Status.Token, nil
}

// ensureReadOnlyServiceAccount is separate from
// internal/k8s.Provisioner.applyServiceAccount's "workspace" SA --
// deliberately a DIFFERENT ServiceAccount, never mounted into the
// learner's own pod (that pod only ever gets "workspace", which has
// AutomountServiceAccountToken: false and no RoleBinding at all). This
// SA exists purely so MintValidatorCredentials has something to request
// a TokenRequest against; a learner's compromised workspace container
// has no path to this SA's token, since it's never mounted anywhere and
// the workspace SA carries zero RBAC permissions of its own.
func ensureReadOnlyServiceAccount(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
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

// readOnlyVerbs is the fixed, non-negotiable verb set for the validator
// Role -- doc §6.2's own framing ("read-only operations"). Not derived
// from req.Scopes at all: scopes (see mintValidatorCredential's caller)
// name WHICH resources the credential can read (e.g. "pods", "services"),
// never grant write verbs -- a caller-supplied scope list is content/
// request data, and letting it influence verbs would let a malformed or
// adversarial scopes list escalate from "read-only" to something else.
var readOnlyVerbs = []string{"get", "list", "watch"}

// defaultReadOnlyResources is used when the caller supplies no scopes at
// all (an empty req.Scopes) -- a sane default read-only surface for
// diagnosing an environment's state (matches K8S_ASSERT's own resource
// vocabulary in orchestrator/internal/validation), not "everything."
var defaultReadOnlyResources = []string{"pods", "services", "deployments", "configmaps", "events", "endpoints"}

// ensureReadOnlyRole creates (or leaves alone, if already present) a
// namespace-scoped Role granting get/list/watch on the caller-requested
// resource types (or defaultReadOnlyResources if none were requested).
// deployments/replicasets need the apps API group; everything else here
// is core-group.
func ensureReadOnlyRole(ctx context.Context, clientset kubernetes.Interface, namespace, name string, scopes []string) error {
	resources := scopes
	if len(resources) == 0 {
		resources = defaultReadOnlyResources
	}

	coreResources, appsResources := splitByAPIGroup(resources)

	rules := []rbacv1.PolicyRule{}
	if len(coreResources) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: coreResources,
			Verbs:     readOnlyVerbs,
		})
	}
	if len(appsResources) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{"apps"},
			Resources: appsResources,
			Verbs:     readOnlyVerbs,
		})
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

// appsGroupResources is the fixed set of resource names that belong to
// the "apps" API group rather than the core group -- deliberately a
// small, explicit allowlist (not a lookup against a live API discovery
// call) so this package's behavior is deterministic and testable without
// a live cluster.
var appsGroupResources = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"replicasets":  true,
	"daemonsets":   true,
}

func splitByAPIGroup(resources []string) (coreResources, appsResources []string) {
	for _, r := range resources {
		if appsGroupResources[r] {
			appsResources = append(appsResources, r)
		} else {
			coreResources = append(coreResources, r)
		}
	}
	return coreResources, appsResources
}

// ensureReadOnlyRoleBinding binds the read-only Role to the read-only
// ServiceAccount, both scoped to the same single namespace -- a
// RoleBinding (not ClusterRoleBinding) is what makes "good only for
// read-only operations inside the ONE environment's namespace" (doc
// §6.2) an enforced fact, not just a naming convention: this SA's token
// is rejected by the K8s API server for any request against a
// different namespace's resources, regardless of what the caller asks
// for.
func ensureReadOnlyRoleBinding(ctx context.Context, clientset kubernetes.Interface, namespace, bindingName, roleName, saName string) error {
	obj := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
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
