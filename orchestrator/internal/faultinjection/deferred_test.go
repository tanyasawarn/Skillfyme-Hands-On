package faultinjection

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// tierUnavailableFaults and noBaselineFaults must stay in sync with
// deferred.go's registerUnsupported calls -- duplicated here rather than
// introspecting the registry by reason, so a change to deferred.go's
// classification is caught by an explicit test diff, not silently.
var tierUnavailableFaults = []string{
	"f.cloud.iam-overpermissive-role",
	"f.iam.missing-ecr-pull",
}

var noBaselineFaults = []string{}

func TestDeferredFaults_ReturnErrUnsupportedMechanism(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	all := append(append([]string{}, tierUnavailableFaults...), noBaselineFaults...)
	all = append(all, "f.k8s.hpa-metrics-unavailable")

	for _, id := range all {
		handler, ok := registry[id]
		if !ok {
			t.Errorf("%s: expected to be registered (as deferred), but is absent from registry -- would fall through to ErrNoHandler instead of a typed deferred reason", id)
			continue
		}
		_, err := handler(context.Background(), clientset, testNamespace, map[string]string{})
		var unsupported ErrUnsupportedMechanism
		if !errors.As(err, &unsupported) {
			t.Errorf("%s: expected ErrUnsupportedMechanism, got %v", id, err)
			continue
		}
		if unsupported.FaultID != id {
			t.Errorf("%s: ErrUnsupportedMechanism.FaultID = %q, want %q", id, unsupported.FaultID, id)
		}
	}
}

func TestDeferredFaults_TierUnavailableReason(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	for _, id := range tierUnavailableFaults {
		handler := registry[id]
		_, err := handler(context.Background(), clientset, testNamespace, nil)
		var unsupported ErrUnsupportedMechanism
		if !errors.As(err, &unsupported) {
			t.Fatalf("%s: expected ErrUnsupportedMechanism, got %v", id, err)
		}
		if unsupported.Reason != ReasonTierUnavailable {
			t.Errorf("%s: Reason = %q, want %q", id, unsupported.Reason, ReasonTierUnavailable)
		}
	}
}

func TestDeferredFaults_NoBaselineFixtureReason(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	for _, id := range noBaselineFaults {
		handler := registry[id]
		_, err := handler(context.Background(), clientset, testNamespace, nil)
		var unsupported ErrUnsupportedMechanism
		if !errors.As(err, &unsupported) {
			t.Fatalf("%s: expected ErrUnsupportedMechanism, got %v", id, err)
		}
		if unsupported.Reason != ReasonNoBaselineFixture {
			t.Errorf("%s: Reason = %q, want %q", id, unsupported.Reason, ReasonNoBaselineFixture)
		}
	}
}

func TestHPAMetricsUnavailable_StaysDeferredWithMetricsContractReason(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	handler, ok := registry["f.k8s.hpa-metrics-unavailable"]
	if !ok {
		t.Fatal("f.k8s.hpa-metrics-unavailable must remain registered (as deferred), not absent")
	}
	_, err := handler(context.Background(), clientset, testNamespace, map[string]string{"hpa": "checkout"})
	var unsupported ErrUnsupportedMechanism
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedMechanism, got %v", err)
	}
	if unsupported.Reason != ReasonMetricsContractPending {
		t.Errorf("Reason = %q, want %q -- f.k8s.hpa-metrics-unavailable must stay deferred pending its own metrics-degradation contract design, not be reclassified as a generic missing-fixture case", unsupported.Reason, ReasonMetricsContractPending)
	}
}

// TestEgressProxyFault_NowRegisteredAsRealHandler documents the resolution
// of what was previously a permanent blocker. handlers_batch4.go re-scoped
// this fault to a mechanism that IS safely per-namespace (deleting the
// namespace's own allow-egress-proxy NetworkPolicy, rather than editing
// the shared Squid ConfigMap every namespace's proxy path depends on) --
// content/faults/f.cloud.egress-proxy-allowlist-too-strict.yaml was
// bumped to v2 with a corrected canonical_diagnostic_path to match.
// TestApplyEgressProxyAllowlistTooStrict_NeverTouchesPlatformNamespace
// (handlers_batch4_test.go) is the regression guard against reintroducing
// the original blast-radius problem.
func TestEgressProxyFault_NowRegisteredAsRealHandler(t *testing.T) {
	if _, ok := registry["f.cloud.egress-proxy-allowlist-too-strict"]; !ok {
		t.Error("f.cloud.egress-proxy-allowlist-too-strict should now be registered as a real handler -- see handlers_batch4.go")
	}
}

func TestFullFaultRegistry_All35Accounted(t *testing.T) {
	wired := []string{
		"f.k8s.memory-limit-too-low",
		"f.k8s.wrong-service-selector",
		"f.k8s.readiness-probe-too-aggressive",
		"f.k8s.configmap-key-renamed",
		"f.k8s.taint-blocks-scheduling",
		"f.k8s.resourcequota-blocks-deploy",
		"f.k8s.pvc-storageclass-missing",
		"f.k8s.networkpolicy-overblocks-traffic",
		"f.k8s.statefulset-ordinal-stuck",
		"f.k8s.rollout-stuck-bad-image-tag",
		"f.load.traffic-spike",
		"f.cloud.egress-proxy-allowlist-too-strict",
	}
	// dynamicWired: faults registered via registerDynamic (CRD-backed,
	// no generated typed client -- see faultinjection.DynamicHandler's
	// doc comment) rather than register. Checked against dynamicRegistry,
	// not registry.
	dynamicWired := []string{
		"f.tekton.task-missing-workspace-binding",
		"f.prometheus.scrape-target-down",
		"f.prometheus.alert-rule-syntax-silent-fail",
		"f.jaeger.missing-trace-context-propagation",
		"f.elk.logstash-pipeline-blocked",
		"f.jenkins.agent-offline",
		"f.jenkins.stale-cached-dependency",
		"f.helm.values-override-not-applied",
		"f.ansible.inventory-host-unreachable",
		"f.tf.state-lock-orphan",
		"f.tf.state-drift-manual-change",
		"f.tf.module-version-pin-mismatch",
		"f.gitlab.protected-branch-blocks-push",
		"f.docker.dockerfile-wrong-workdir",
		"f.docker.network-not-attached",
		"f.docker.swarm-service-image-pull-fail",
		"f.github.actions-secret-not-passed",
		"f.istio.mtls-mode-mismatch",
		"f.istio.virtualservice-weight-sum-invalid",
		"f.gitops.argocd-out-of-sync-manual-drift",
	}
	deferred := append(append([]string{}, tierUnavailableFaults...), noBaselineFaults...)
	deferred = append(deferred, "f.k8s.hpa-metrics-unavailable")

	for _, id := range wired {
		if _, ok := registry[id]; !ok {
			t.Errorf("%s: expected a real wired handler, not found in registry", id)
		}
	}
	for _, id := range dynamicWired {
		if _, ok := dynamicRegistry[id]; !ok {
			t.Errorf("%s: expected a real wired DynamicHandler, not found in dynamicRegistry", id)
		}
	}
	for _, id := range deferred {
		if _, ok := registry[id]; !ok {
			t.Errorf("%s: expected a deferred (ErrUnsupportedMechanism) registration, not found in registry", id)
		}
	}

	// 12 typed-wired + 20 dynamic-wired + 3 deferred = 35. No more
	// intentionally-absent faults -- every one of the 35 manifests now
	// resolves to either a real handler (typed or dynamic) or a typed
	// deferred reason.
	total := len(wired) + len(dynamicWired) + len(deferred)
	if total != 35 {
		t.Fatalf("test's own accounting is wrong: %d != 35 -- update this test alongside content/faults/", total)
	}
}
