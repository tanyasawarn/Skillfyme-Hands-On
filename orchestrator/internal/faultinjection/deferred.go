package faultinjection

// Faults that have been triaged and found to need infrastructure this
// package doesn't have yet -- registered against ErrUnsupportedMechanism
// (not left absent, which would fall through to the less specific
// ErrNoHandler) so InjectFault can tell a caller *why* in a stable way.
// See ErrUnsupportedMechanism's doc comment in faultinjection.go for the
// two reasons in play here.
//
// Coverage: 12 fully wired via Handler/registry (handlers.go,
// handlers_batch2.go, handlers_batch3.go's f.load.traffic-spike,
// handlers_batch4.go's f.cloud.egress-proxy-allowlist-too-strict), 20
// wired via DynamicHandler/dynamicRegistry (handlers_batch5.go through
// handlers_batch16.go -- Tekton, Prometheus x2, Jaeger, ELK, Jenkins x2,
// Helm, Ansible, Terraform x3, Gitea, Docker/GitHub-Actions x4, Istio
// x2, ArgoCD -- each backed by a real fixture, see internal/fixture/), 1
// explicitly deferred pending a separate contract
// (f.k8s.hpa-metrics-unavailable), 2 deferred on tier AND a real
// resource gap this codebase cannot vend yet (AWS-IAM-flavored faults,
// no real AWS account-vending) -- 35 total accounted for, none left
// unregistered any more.
func init() {
	// ReasonMetricsContractPending: cluster-infrastructure target, no
	// per-fault params can express what to degrade -- needs its own
	// contract, not fixture work. Explicitly kept deferred per its own
	// prior triage; do not wire ahead of that design decision.
	registerUnsupported("f.k8s.hpa-metrics-unavailable", ReasonMetricsContractPending)

	// ReasonTierUnavailable: min_tier: T2_ISOLATED_MICROVM in every one
	// of these YAMLs (contracts/fault.schema.json). T2's DRIVER CODE is
	// real (internal/k8s/provision.go's applyT2PodShape, real Kata
	// RuntimeClass assignment, gated behind ORCHESTRATOR_T2_ENABLED) --
	// the earlier version of this comment claiming "the T2 driver has
	// not been built" was stale by the time it was checked this
	// session. The actual, live-verified gap is narrower: THIS dev
	// environment has no Kata-capable node, so a real Provision(T2)
	// call genuinely fails at K8s admission with `RuntimeClass "kata"
	// not found` (see internal/k8s/provision_t2_live_test.go, a real
	// live test asserting exactly this). f.istio.mtls-mode-mismatch,
	// f.istio.virtualservice-weight-sum-invalid, AND
	// f.gitops.argocd-out-of-sync-manual-drift now have real handlers
	// (handlers_batch15.go's Istio pair backed by fx.istio-minimal.v1's
	// real istiod control plane; handlers_batch16.go's ArgoCD fault
	// backed by fx.argocd-minimal.v1's real Argo CD core install +
	// fx.gitea-repo.v1 as the tracked Git source -- both live-verified
	// against a T1-shaped test namespace directly) gated at the RPC
	// layer by faultinjection.RequiresT2 + server.go's InjectFault tier
	// check, not left deferred -- the T2-unschedulability gap is proven
	// separately and does not block building/verifying the handlers
	// themselves. The 2 remaining AWS-IAM-flavored faults stay deferred:
	// their content is genuinely ARN/policy-specific (not a
	// K8s-RBAC-reframable claim, unlike ArgoCD which turned out to be
	// buildable the same way the Istio pair was), and Phase 2 has no
	// real AWS account-vending yet -- see each fault's own content YAML.
	registerUnsupported("f.cloud.iam-overpermissive-role", ReasonTierUnavailable)
	registerUnsupported("f.iam.missing-ecr-pull", ReasonTierUnavailable)

	// ReasonNoBaselineFixture: min_tier: T1_SHARED_CONTAINER (so tier
	// isn't the blocker), but each targets a tool/object the *learner*
	// installs or configures as part of the lab's own tasks -- a
	// Jenkins agent, a Terraform state lock, a Prometheus scrape
	// config, a Helm release, an Ansible inventory, a Tekton TaskRun,
	// an ELK pipeline, a Jaeger trace collector, a GitHub Actions
	// workflow, a GitLab branch policy, a Docker build/network/Swarm
	// service. None of these exist as an object in the environment at
	// InjectFault time (which runs at T0, before the learner has done
	// anything) because no blueprint or fixture provisions them --
	// confirmed by checking every relevant activity's environment.blueprint
	// (e.g. lab.jenkins.basics.yaml uses bp.linux.v1, a bare-Linux
	// blueprint with no Jenkins server). Applying these for real needs
	// Production Sim fixtures that stand the target tool up first --
	// content/fixture work, not a Go handler -- so this package is
	// honest that it cannot fabricate a baseline that doesn't exist,
	// the same stance notFoundResult already takes for K8s objects a
	// learner hasn't created yet, just one level earlier (the whole
	// *tool* is missing, not just one object inside it).
	// f.docker.dockerfile-wrong-workdir, f.docker.network-not-attached,
	// f.docker.swarm-service-image-pull-fail, f.github.actions-secret-not-passed:
	// now wired for real, see handlers_batch14.go -- fx.dind-workspace.v1
	// provisions a real privileged Docker-in-Docker daemon first.
	// f.tekton.task-missing-workspace-binding: now wired for real, see
	// handlers_batch5.go -- fx.tekton-pipeline.v1 provisions a real Task +
	// PVC-backed workspace baseline first.

	// f.cloud.egress-proxy-allowlist-too-strict was, until this batch,
	// deliberately left UNREGISTERED: its original content (v1) claimed
	// a Squid-side ACL-deny mechanism that would require editing the
	// ONE shared squid.conf ConfigMap every concurrently-provisioned
	// learner namespace depends on -- a real cross-tenant blast-radius
	// violation, not a missing-fixture or missing-tier gap. handlers_
	// batch4.go resolves this for real by re-scoping the fault to a
	// mechanism that IS safely per-namespace: deleting the namespace's
	// OWN allow-egress-proxy NetworkPolicy (applyEgressProxyAllowlist,
	// internal/k8s/provision.go) rather than touching shared Squid
	// state. content/faults/f.cloud.egress-proxy-allowlist-too-strict.yaml
	// was bumped to v2 with a corrected canonical_diagnostic_path
	// (connection-timeout symptom, not the v1 HTTP-403-from-proxy
	// framing that mechanism can no longer honestly claim). Now
	// registered as a real handler, not deferred -- see handlers_batch4.go.
}
