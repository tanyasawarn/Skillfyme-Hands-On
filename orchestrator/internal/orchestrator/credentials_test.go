package orchestrator

import (
	"context"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNamespace = "env-test"

// stubTokenRequestReactor registers a reactor for the ServiceAccounts
// "token" create-subresource action -- k8s.io/client-go/kubernetes/fake
// has no built-in support for TokenRequest's CreateToken subresource
// (confirmed by inspection: fakeServiceAccounts.CreateToken just invokes
// the generic subresource-create action against the fake's object
// tracker, which doesn't know how to synthesize a TokenRequest.Status).
// Without this reactor, CreateToken returns an empty TokenRequest and a
// "serviceaccounts \"\" not found" error, not a real Status.Token --
// this is a test-double gap in client-go's fake package itself, not a
// bug in mintValidatorCredential's real logic.
func stubTokenRequestReactor(clientset *fake.Clientset, token string) {
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateAction)
		if !ok || action.GetSubresource() != "token" {
			return false, nil, nil // not our subresource -- let the default chain handle it
		}
		req, ok := createAction.GetObject().(*authenticationv1.TokenRequest)
		if !ok {
			return false, nil, nil
		}
		req.Status = authenticationv1.TokenRequestStatus{Token: token}
		return true, req, nil
	})
}

func TestSplitByAPIGroup(t *testing.T) {
	core, apps := splitByAPIGroup([]string{"pods", "deployments", "services", "statefulsets"})
	wantCore := map[string]bool{"pods": true, "services": true}
	wantApps := map[string]bool{"deployments": true, "statefulsets": true}

	if len(core) != len(wantCore) {
		t.Errorf("expected %d core resources, got %v", len(wantCore), core)
	}
	for _, r := range core {
		if !wantCore[r] {
			t.Errorf("unexpected core resource %q", r)
		}
	}
	if len(apps) != len(wantApps) {
		t.Errorf("expected %d apps resources, got %v", len(wantApps), apps)
	}
	for _, r := range apps {
		if !wantApps[r] {
			t.Errorf("unexpected apps resource %q", r)
		}
	}
}

func TestEnsureReadOnlyRole_UsesDefaultsWhenNoScopesGiven(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureReadOnlyRole(context.Background(), clientset, testNamespace, "validator-readonly", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	role, err := clientset.RbacV1().Roles(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected role to exist: %v", err)
	}
	if len(role.Rules) == 0 {
		t.Fatal("expected at least one rule when using default resources")
	}
}

// TestEnsureReadOnlyRole_NeverGrantsWriteVerbs is the core security
// property this file exists to guarantee: doc §6.2 says "read-only
// operations" -- this asserts every single rule this function can ever
// produce, across every scope combination, contains only get/list/watch,
// regardless of what scopes the caller supplies (scopes name WHICH
// resources, never verbs).
func TestEnsureReadOnlyRole_NeverGrantsWriteVerbs(t *testing.T) {
	allowedVerbs := map[string]bool{"get": true, "list": true, "watch": true}
	scopeSets := [][]string{
		nil,
		{"pods"},
		{"deployments", "statefulsets"},
		{"pods", "services", "deployments", "configmaps", "events", "endpoints"},
		{"pods", "pods", "pods"}, // duplicate scopes shouldn't produce duplicate write-capable rules either
	}

	for i, scopes := range scopeSets {
		clientset := fake.NewSimpleClientset()
		roleName := "validator-readonly"
		if err := ensureReadOnlyRole(context.Background(), clientset, testNamespace, roleName, scopes); err != nil {
			t.Fatalf("case %d: unexpected error: %v", i, err)
		}
		role, err := clientset.RbacV1().Roles(testNamespace).Get(context.Background(), roleName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("case %d: expected role: %v", i, err)
		}
		for _, rule := range role.Rules {
			for _, verb := range rule.Verbs {
				if !allowedVerbs[verb] {
					t.Errorf("case %d (scopes=%v): SECURITY REGRESSION: rule granted non-read-only verb %q", i, scopes, verb)
				}
			}
		}
	}
}

func TestEnsureReadOnlyRoleBinding_BindsToNamespaceScopedRole(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureReadOnlyRoleBinding(context.Background(), clientset, testNamespace, "validator-readonly", "validator-readonly", "validator-readonly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	binding, err := clientset.RbacV1().RoleBindings(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected binding to exist: %v", err)
	}
	if binding.RoleRef.Kind != "Role" {
		t.Errorf("expected RoleRef.Kind=Role (namespace-scoped), got %q -- a ClusterRole reference here would break the namespace-only isolation guarantee", binding.RoleRef.Kind)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != testNamespace {
		t.Errorf("expected exactly one subject scoped to %s, got %+v", testNamespace, binding.Subjects)
	}
}

func TestEnsureReadOnlyServiceAccount_NeverAutoMounted(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	if err := ensureReadOnlyServiceAccount(context.Background(), clientset, testNamespace, "validator-readonly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sa, err := clientset.CoreV1().ServiceAccounts(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected SA to exist: %v", err)
	}
	// This SA must never be automounted into any pod -- it's created
	// solely so MintValidatorCredentials has something to request a
	// TokenRequest against, never referenced by a Pod spec anywhere
	// (confirmed by inspection: internal/k8s/provision.go's
	// createWorkspacePod only ever sets ServiceAccountName: "workspace",
	// a different SA entirely).
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("expected AutomountServiceAccountToken=false")
	}
}

func TestMintValidatorCredential_ReturnsRealToken(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	stubTokenRequestReactor(clientset, "stub-token-value")
	token, err := mintValidatorCredential(context.Background(), clientset, testNamespace, "test-env", 300, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "stub-token-value" {
		t.Errorf("expected the reactor's stub token to flow through unchanged, got %q", token)
	}

	if _, err := clientset.CoreV1().ServiceAccounts(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{}); err != nil {
		t.Errorf("expected ServiceAccount to have been created: %v", err)
	}
	if _, err := clientset.RbacV1().Roles(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{}); err != nil {
		t.Errorf("expected Role to have been created: %v", err)
	}
	if _, err := clientset.RbacV1().RoleBindings(testNamespace).Get(context.Background(), "validator-readonly", metav1.GetOptions{}); err != nil {
		t.Errorf("expected RoleBinding to have been created: %v", err)
	}
}

func TestMintValidatorCredential_IdempotentAcrossRepeatedCalls(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	stubTokenRequestReactor(clientset, "stub-token-value")
	if _, err := mintValidatorCredential(context.Background(), clientset, testNamespace, "test-env", 300, nil); err != nil {
		t.Fatalf("first mint: unexpected error: %v", err)
	}
	// A second mint for the same namespace (e.g. a second validator run
	// in the same attempt) must not fail trying to recreate SA/Role/
	// RoleBinding that already exist.
	if _, err := mintValidatorCredential(context.Background(), clientset, testNamespace, "test-env", 300, nil); err != nil {
		t.Fatalf("second mint: unexpected error: %v", err)
	}
}

// --- CredentialStore ---

func TestCredentialStore_PutThenResolve(t *testing.T) {
	store := NewCredentialStore()
	store.Put("ref-1", "real-token-value", time.Now().Add(time.Minute))

	token, ok := store.Resolve("ref-1")
	if !ok {
		t.Fatal("expected ref-1 to resolve")
	}
	if token != "real-token-value" {
		t.Errorf("expected real-token-value, got %q", token)
	}
}

func TestCredentialStore_ResolveUnknownRef(t *testing.T) {
	store := NewCredentialStore()
	_, ok := store.Resolve("never-stored")
	if ok {
		t.Fatal("expected unknown ref to fail to resolve")
	}
}

// TestCredentialStore_ExpiredCredentialDoesNotResolve is a real security
// property: doc §6.2's whole "5-minute lifetime" design is worthless if
// an expired credential still resolves. This confirms the store itself
// enforces expiry, not just relying on the K8s API server's own
// TokenRequest expiry (defense in depth: this store also stops handing
// out a token past its own recorded expiry).
func TestCredentialStore_ExpiredCredentialDoesNotResolve(t *testing.T) {
	store := NewCredentialStore()
	store.Put("ref-expired", "real-token-value", time.Now().Add(-time.Second)) // already expired

	_, ok := store.Resolve("ref-expired")
	if ok {
		t.Fatal("SECURITY REGRESSION: an expired credential resolved successfully")
	}
}

func TestCredentialStore_ExpiredEntryIsEvicted(t *testing.T) {
	store := NewCredentialStore()
	store.Put("ref-expired", "token", time.Now().Add(-time.Second))
	store.Resolve("ref-expired") // triggers lazy eviction

	if len(store.entries) != 0 {
		t.Errorf("expected expired entry to be evicted from the store, got %d entries remaining", len(store.entries))
	}
}

func TestCredentialStore_PutPanicsOnDuplicateRef(t *testing.T) {
	store := NewCredentialStore()
	store.Put("ref-1", "token-a", time.Now().Add(time.Minute))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected Put to panic on a duplicate ref (a caller bug that must not silently overwrite a live credential)")
		}
	}()
	store.Put("ref-1", "token-b", time.Now().Add(time.Minute))
}
