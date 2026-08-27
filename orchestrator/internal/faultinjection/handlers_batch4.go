package faultinjection

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Fourth batch: f.cloud.egress-proxy-allowlist-too-strict, the fault
// previously left deliberately unregistered because its original content
// (v1) claimed a mechanism (a Squid-side 403 ACL deny visible in proxy
// access logs) that the platform's shared, single-Squid-deployment
// architecture cannot safely produce scoped to one learner -- editing
// the shared squid.conf ConfigMap would deny that domain for every
// concurrently-running environment, a real cross-tenant blast-radius
// violation (see deferred.go's git history / PHASE2_CLOSEOUT.md for the
// full prior triage).
//
// This batch resolves it for real, not by finding a way around that
// constraint, but by re-scoping the fault to a mechanism that IS safely
// per-namespace: the namespace's OWN allow-egress-proxy NetworkPolicy
// (internal/k8s/provision.go's applyEgressProxyAllowlist), which every
// environment gets its own independent copy of at provision time.
// Removing it blocks that one namespace's path to the egress proxy
// entirely -- a connection-timeout symptom, not the original v1 content's
// HTTP-403 framing (Squid-side ACL denies require the connection to
// reach Squid at all, which a missing NetworkPolicy prevents by design).
// content/faults/f.cloud.egress-proxy-allowlist-too-strict.yaml was
// bumped to v2 and its canonical_diagnostic_path rewritten to match this
// real mechanism honestly, rather than shipping a handler whose
// diagnostic instructions contradict what it actually does.
func init() {
	register("f.cloud.egress-proxy-allowlist-too-strict", applyEgressProxyAllowlistTooStrict)
}

// applyEgressProxyAllowlistTooStrict: content/faults/f.cloud.egress-proxy-allowlist-too-strict.yaml
// params: namespace (optional override, defaults to the environment's own namespace).
func applyEgressProxyAllowlistTooStrict(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	ns := nonEmptyParam(params["namespace"], namespace)

	policies := clientset.NetworkingV1().NetworkPolicies(ns)
	existing, err := policies.Get(ctx, "allow-egress-proxy", metav1.GetOptions{})
	if isNotFound(err) {
		// Idempotent in the other direction: the policy is already
		// absent (either a prior application of this same fault, or a
		// namespace that genuinely never got one) -- the fault's
		// intended end-state already holds, so this is a successful
		// no-op, not an error. Distinct from every other handler's
		// notFoundResult, which reports "target to BREAK doesn't exist
		// yet" as a failure -- here the missing object IS the broken
		// state this fault wants.
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("checking allow-egress-proxy networkpolicy in namespace %s: %w", ns, err)
	}

	if err := policies.Delete(ctx, existing.Name, metav1.DeleteOptions{}); err != nil {
		return Result{}, fmt.Errorf("deleting allow-egress-proxy networkpolicy in namespace %s: %w", ns, err)
	}

	// Verifiable immediately: the policy either still exists or it
	// doesn't -- no propagation delay like the endpoint-controller-backed
	// faults in handlers.go. The default-deny NetworkPolicy
	// (applyDefaultDenyNetworkPolicy) stays in place regardless, so
	// deleting only allow-egress-proxy is what re-establishes the
	// symptom: every outbound path this namespace had is now blocked
	// again, having lost its one carve-out.
	_, verifyErr := policies.Get(ctx, "allow-egress-proxy", metav1.GetOptions{})
	verified := isNotFound(verifyErr)
	return Result{Applied: true, SymptomVerified: verified}, nil
}
