package faultinjection

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplyEgressProxyAllowlistTooStrict_DeletesNamespacePolicy(t *testing.T) {
	clientset := fake.NewSimpleClientset(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: testNamespace},
	})

	result, err := applyEgressProxyAllowlistTooStrict(context.Background(), clientset, testNamespace, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	_, err = clientset.NetworkingV1().NetworkPolicies(testNamespace).Get(context.Background(), "allow-egress-proxy", metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected allow-egress-proxy to be deleted, but it still exists")
	}
}

func TestApplyEgressProxyAllowlistTooStrict_IdempotentWhenAlreadyAbsent(t *testing.T) {
	clientset := fake.NewSimpleClientset() // no NetworkPolicy pre-seeded

	result, err := applyEgressProxyAllowlistTooStrict(context.Background(), clientset, testNamespace, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error on already-absent policy: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true (fault's target end-state already holds), got %+v", result)
	}
}

// TestApplyEgressProxyAllowlistTooStrict_NeverTouchesPlatformNamespace is a
// security-relevant regression guard: this fault was previously left
// unregistered specifically because an earlier design would have had to
// mutate the SHARED practiceengine-platform namespace's Squid config,
// affecting every concurrently-running learner environment. This test
// asserts the real implementation only ever touches the caller-supplied
// namespace's own NetworkPolicy object -- the shared platform namespace
// is never referenced by this handler at all (confirmed by inspection:
// no egressProxyNamespace constant usage in handlers_batch4.go), and
// this test seeds a NetworkPolicy of the same name in the platform
// namespace to prove it survives untouched.
func TestApplyEgressProxyAllowlistTooStrict_NeverTouchesPlatformNamespace(t *testing.T) {
	const platformNamespace = "practiceengine-platform"
	clientset := fake.NewSimpleClientset(
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: testNamespace},
		},
		&networkingv1.NetworkPolicy{
			// Same name, different (shared/platform) namespace -- must
			// survive this call untouched regardless of what happens to
			// the target namespace's own copy.
			ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: platformNamespace},
		},
	)

	if _, err := applyEgressProxyAllowlistTooStrict(context.Background(), clientset, testNamespace, map[string]string{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := clientset.NetworkingV1().NetworkPolicies(platformNamespace).Get(context.Background(), "allow-egress-proxy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("platform namespace's NetworkPolicy must survive untouched, but Get failed: %v", err)
	}
}

// TestApplyEgressProxyAllowlistTooStrict_NamespaceParamOverride confirms
// the optional namespace param override targets exactly the specified
// namespace and no other -- another blast-radius guard: an
// attacker-controlled or mistaken namespace override must not silently
// widen scope beyond the one namespace named.
func TestApplyEgressProxyAllowlistTooStrict_NamespaceParamOverride(t *testing.T) {
	const overrideNamespace = "env-other-learner"
	clientset := fake.NewSimpleClientset(
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: testNamespace}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: overrideNamespace}},
	)

	if _, err := applyEgressProxyAllowlistTooStrict(context.Background(), clientset, testNamespace, map[string]string{
		"namespace": overrideNamespace,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The override namespace's policy should be gone...
	if _, err := clientset.NetworkingV1().NetworkPolicies(overrideNamespace).Get(context.Background(), "allow-egress-proxy", metav1.GetOptions{}); err == nil {
		t.Error("expected override namespace's policy to be deleted")
	}
	// ...but the caller's own namespace (testNamespace) must be untouched,
	// since the param explicitly redirected the target.
	if _, err := clientset.NetworkingV1().NetworkPolicies(testNamespace).Get(context.Background(), "allow-egress-proxy", metav1.GetOptions{}); err != nil {
		t.Errorf("expected the caller's own namespace policy to survive untouched when namespace param overrides target, got: %v", err)
	}
}

func TestFaultRegistry_EgressProxyFaultNowRegistered(t *testing.T) {
	if _, ok := registry["f.cloud.egress-proxy-allowlist-too-strict"]; !ok {
		t.Error("expected f.cloud.egress-proxy-allowlist-too-strict to now be a real registered handler")
	}
}
