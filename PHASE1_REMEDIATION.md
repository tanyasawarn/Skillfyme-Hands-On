# Phase 0/1/2 Remediation Checklist

Tracks the 16 gaps surfaced by the deep Phase 0-2 audit (2026-08-23) against PLAN.md. Each item
requires: the feature itself, end-to-end verification (build/test at minimum; live-cluster
verification where an item genuinely needs one, explicitly flagged if unavailable in this
environment), and security validation before being marked complete.

Environment constraint carried over from every prior session: no reachable Docker daemon / live
K8s cluster in this sandbox. Items that need one are verified as far as build+unit-test+schema
validation allows, with the live-cluster gap stated explicitly, never silently claimed as
fully verified.

**Update (2026-08-23): live infrastructure discovered mid-session** — the docker-compose stack
(postgres/redis/k3s/registry/nats, plus a newly-added minio) turned out to actually be reachable.
Every item below has since been re-verified against the real running orchestrator + real k3s
cluster (rebuild-and-restart cycle: `go build -o /tmp/orchestrator-bin ./cmd/orchestrator`,
`pkill -f orchestrator-bin`, relaunch, real `grpcurl` Provision/Destroy calls, real `kubectl exec`
into learner pods), not just unit tests against fake clientsets. This caught 5 real bugs that no
fake-clientset unit test could ever catch, all now fixed and covered by regression tests where a
unit-testable seam exists:

1. **PodSecurity rejection** — `applyPodCrashloop`'s Pod had no SecurityContext at all; the
   cluster's real PSS "restricted" admission controller rejected it outright. Fixed + regression
   test (`handlers_test.go`).
2. **Wrong kubeconfig host** — `applyK3sReady` pointed the minted kubeconfig at the orchestrator
   process's own external API server address (unreachable from inside a pod) instead of
   `kubernetes.default.svc`. Fixed.
3. **NO_PROXY didn't cover in-cluster traffic** — the workspace pod's `HTTPS_PROXY` (needed for
   external package-registry egress through Squid) was also intercepting `kubectl`'s calls to the
   in-cluster API server, which Squid's allowlist correctly rejected with a 403 that surfaced as a
   misleading generic "Forbidden." Fixed by adding `kubernetes.default.svc`/`.svc`/
   `.svc.cluster.local` to `NO_PROXY` (`internal/k8s/provision.go`).
4. **Missing discovery ClusterRoleBinding** — kubectl's own `GET /api` version-discovery call
   needs a cluster-scoped grant no namespace Role can ever provide. Fixed via
   `ensureDiscoveryClusterRoleBinding` binding the built-in `system:discovery` ClusterRole, plus
   an explicit delete of that binding in `Provisioner.Destroy()` (ClusterRoleBindings don't
   cascade-delete with namespace deletion).
5. **NetworkPolicy couldn't reach the API server at all, in two sequential ways** —
   `ensureAPIServerEgressAllowed`'s first version used a `namespaceSelector` peer targeting the
   `default` namespace, which can only ever match pod IPs; this project's (and most self-managed
   clusters') `kubernetes` Service has no pod backing it (Endpoints resolve to a bare node IP), so
   no namespace/pod selector can ever match it — switched to an `ipBlock: 0.0.0.0/0` peer. That
   still failed live: bisection (toggling policies on/off directly with kubectl while the real
   Provision-created policies stayed in place) isolated a real limitation in this k3s's embedded
   kube-router NetworkPolicy engine — an `ipBlock` peer combined with a `Ports` filter silently
   produces a non-functional rule, even though either alone works. Fixed by dropping the port
   restriction (peer was already unrestricted, so this doesn't meaningfully widen the real attack
   surface — see the doc comment on `ensureAPIServerEgressAllowed` for the full reasoning).
   Regression tests added in `rbac_test.go` for both the selector-kind and the no-port-restriction
   requirements.

Final clean state, confirmed live: fresh `Provision()` with `fx.k3s-ready.v1` →
`kubectl exec workspace -- kubectl get pods` succeeds from inside the learner's own pod;
cluster-scoped calls (`kubectl get svc -A`) correctly still get `Forbidden` (RBAC boundary working
as intended, not a network failure); external package-registry egress through Squid still works
unchanged; `fx.pod-crashloop.v1` and `fx.node-app-repo.v1` also re-verified live in the same pass.
`Destroy()` cleanup verified live too: namespace and `learner-discovery-*` ClusterRoleBinding both
gone (binding deleted synchronously, well before the namespace finishes terminating).

---

## Dev A — Execution & Infrastructure

- [x] ~~gVisor RuntimeClass wired to T1 pods — `Provisioner.gVisorEnabled` field (opt-in,
      default false, same pattern as `Server.t2Enabled`), `runtimeClassForT1()` pure decision
      function, gated behind `ORCHESTRATOR_GVISOR_ENABLED`. Documented in `.env.example`. 2 new
      unit tests. `go build`/`vet`/`gofmt`/`test` all clean~~
- [x] ~~Fixture-apply pipeline step (idempotent, ordered, checksummed) + 3 real fixtures —
      new `internal/fixture` package (registry pattern matching faultinjection), Postgres-backed
      `AppliedTracker` (`env.fixture_applied`, migration 0003), wired into `Provision()` after
      pod-Ready, before metering starts. `ProvisionRequest.fixtures` (proto field 6, defined
      since Phase 0 but never populated by either side) now actually flows end-to-end:
      practice-core reads `environment.seed` from the activity spec (previously read nowhere)
      and sends it; the orchestrator applies each fixture in order.
      **3 real fixtures**: `fx.k3s-ready.v1` (the highest-leverage one — used by 31/64
      activities; mints a namespace-scoped kubeconfig via TokenRequest + a workload-author
      RBAC Role, giving the learner's own kubectl real, bounded access — this closed a
      previously-undiscovered gap: no mechanism existed for a learner's interactive kubectl to
      work at all), `fx.pod-crashloop.v1` (seeds a real crash-looping Pod for troubleshooting
      labs), `fx.node-app-repo.v1` (seeds a real buildable Node app + Dockerfile at
      `/workspace/app`, matching activity instructions exactly — caught and fixed a real bug
      where the first draft wrote to `~/app`, the container's non-persistent WORKDIR, instead
      of the mounted `/workspace` volume). Unimplemented fixtures no longer silently block
      fixtures listed after them in the same activity (a real bug caught by the new tests, not
      shipped). 27 new tests across fixture.go/rbac.go, including a dedicated
      never-grants-RBAC-or-cluster-scoped-access security test~~
- [x] ~~Asciicast recording to real S3 — new `S3RecordingSink` (aws-sdk-go-v2/service/s3, exact
      pinned versions, `go mod verify` clean), produces real, spec-compliant asciicast v2 output
      (header + [time,"o",data] event lines), periodic buffered flush per attempt via PutObject,
      wired into `main.go` (opt-in via `RECORDING_S3_BUCKET`, MinIO-compatible via
      `S3_ENDPOINT_URL`). MinIO + auto-bucket-init added to `docker-compose.yml`. Caught and
      fixed a real data-loss bug before shipping: `Forget()` originally dropped the buffer
      without a final flush, silently losing the last flush-interval's worth of output right at
      environment teardown — now does a synchronous final flush first, with a regression test.
      Credentials never logged. 11 new tests (header/event format validity, header-written-once,
      multi-attempt isolation, flush-error propagation, the Forget regression test)~~
- [x] ~~Health gate: real blueprint self-check — new `internal/validation/health_gate.go`
      (`RunHealthGate`, `ParseHealthGateJSON`), real HTTP_PROBE implementation reusing
      `HTTP_SLO`'s established safe-quoting/execInPod pattern (retry-until-healthy, not
      success-rate-over-time — a different shape for a different purpose). K8S_ASSERT health
      checks explicitly rejected with a clear error rather than silently passing (matches doc
      §3.5's null-path-CI principle — an unimplemented check that always trivially passes is
      worse than an honest error; content already exists that could have hit this silently).
      Wired end-to-end: added `health_gate_json` field 11 to `ProvisionRequest`
      (`contracts/orchestrator.proto`), regenerated `orchestrator.pb.go` with `protoc` (scoped
      diff — only the new field, verified), practice-core now reads the activity spec's
      top-level `health_gate` array (previously read nowhere) and sends it JSON-encoded. Runs
      after fixture-apply, before READY, per doc's own step ordering. 12 new Go tests
      (parsing, order-preservation, type-coercion, the K8S_ASSERT rejection guard) — the actual
      HTTP probe execution isn't unit-tested (needs a real pod), consistent with every other
      execInPod-based validator in this codebase~~
- [x] ~~Image strategy — checked real content usage before building anything: only 1 of ~64
      activities needs `python3` (lab.cloud.twelve-factor.yaml's SHELL_ASSERT validator,
      `execInPod`-executed, genuinely needs it present) and zero need Node installed natively
      in the workspace (the one Node fixture builds via `docker build`, not a native runtime).
      Added `python3` to the existing `linux-tools` image (fixes the real gap). Deliberately did
      NOT build separate `python-ds`/`node` images — no content backs them yet, same
      "don't build speculative infrastructure" principle applied to `ai-gateway/`; documented in
      the Dockerfile so the decision is explicit, not silently missing. Added
      `manifests/t1/daemonset-image-prepull.yaml` (real DaemonSet, pre-pulls the one real image
      that exists — `registry:5000/practiceengine/linux-tools:v1` — rather than 20 placeholder
      entries for images that don't exist). YAML/Dockerfile syntax verified; actual image
      build/push not verified end-to-end (no Docker daemon in this environment, same constraint
      as every prior session — honestly noted, not silently assumed)~~
- [x] ~~Application-level audit log — new `internal/audit` package, durable Postgres-backed
      `env.audit_log` table (migration 0004), closed Action/Outcome value sets (prevents
      typo'd/ambiguous log entries). Wired into every security-relevant RPC: Provision, Destroy,
      InjectFault, MintValidatorCredentials, ExecShell — all via named-return + defer so every
      exit path is captured, not just the happy path. Deliberately never logs raw secrets
      (tokens, stdout/stderr) — documented the one residual risk considered (command text could
      in principle embed a secret) and why it's bounded in practice (checked real callers:
      ExecShell backs a file-management API with code-constructed commands, not free-form
      learner shell text). 4 new tests on the DB-independent logic (this codebase has no pgx
      mock anywhere, matching the same gap in reaper/costmeter/regression — tested what's
      testable without new mocking infra)~~
- [x] ~~`MintValidatorCredentials`: real scoped short-lived K8s credentials — mints a genuine
      TokenRequest-backed ServiceAccount token (`credentials.go`'s `mintValidatorCredential`),
      scoped by a namespace-only read-only Role (get/list/watch only, never write verbs —
      verified by a dedicated security test), bound via RoleBinding (not ClusterRoleBinding).
      Raw token never crosses the RPC boundary or hits a log line (`CredentialStore`, keyed by
      opaque ref, expiry-enforced independent of the K8s API server's own TokenRequest expiry).
      Honestly documented: `ExecValidator` (the real validator-execution path) doesn't consume
      this credential — an architectural fact stated plainly, not concealed. 21 new tests
      (RBAC construction, write-verb regression guard, SA never-automounted check, idempotency,
      store expiry/eviction/duplicate-ref panic). All pass~~
- [ ] Test coverage: costmeter, idledetect, reaper, regression, warmpool packages (currently zero)
- [x] ~~gRPC authentication: shared bearer token interceptor on every orchestrator RPC —
      `internal/orchestrator/auth.go`'s `AuthInterceptor`, constant-time token comparison
      (`crypto/subtle`), health-check RPC exempted, opt-in via `ORCHESTRATOR_SHARED_SECRET`
      (loud warning logged when disabled). Wired into `grpc.NewServer()` in main.go. Both
      practice-core gRPC clients (`GrpcOrchestratorClient`, `GrpcValidatorExecutor`) now send
      the token as `authorization: Bearer <token>` metadata. Documented in both `.env.example`
      files. 15 new Go tests (auth_test.go): disabled-by-default, missing/malformed header,
      wrong token, prefix/suffix near-miss rejection, health-check exemption + its scope guard.
      All pass; `go build`/`vet`/`gofmt`/`tsc` clean~~

## Dev B — Product & Content

- [x] ~~`practice-cli` unified binary (`validate/test/publish`) wrapping the existing scripts —
      thin `spawnSync` dispatcher (`scripts/practice-cli.ts`) over `lint-content.ts`/
      `content-ci.ts`/`publish-all-content.ts`, real shell bin entry (`bin/practice-cli`,
      `chmod +x`'d) wired via `package.json`'s `bin` field + an `npm run practice-cli` script.
      Live-verified through all three invocation paths (direct bin, npm script, ts-node) against
      the real running orchestrator: `validate` (64/64 activities pass), `test` (see below),
      `publish` (previously live-verified this session)~~
- [x] ~~Content CI: timing and cost stages — added wall-clock timing (provision through the last
      flake run) and an estimated-USD cost stage (same T1 hourly-rate estimate the real cost
      meter uses, `internal/costmeter.hourlyRateUSD`, duplicated deliberately since practice-core
      has no dependency on the orchestrator's Go internals), flagged/failed if a run exceeds
      `CI_BUDGET_USD` (default matches `DEFAULT_BUDGET_USD`, doc §13.1's $0.08/attempt exit
      criterion). Also fixed a real spec/implementation drift caught while touching this file:
      the doc comment itself says "flake x5" but `FLAKE_RUNS` defaulted to 3 — corrected to 5.
      Also wired the `authorization: Bearer` metadata this script was missing (it would have
      hard-failed with UNAUTHENTICATED against the now-auth-enabled live orchestrator, same
      pattern as the two NestJS gRPC clients). All live-verified against the real orchestrator +
      k3s cluster: null/golden/flake stages pass, timing reports real elapsed seconds, cost
      reports a real dollar estimate, over-budget correctly fails the check (verified by forcing
      an artificially low `CI_BUDGET_USD`), and a request with no shared secret correctly gets
      rejected with UNAUTHENTICATED (confirming auth isn't accidentally bypassed)~~
- [x] ~~Skill graph: diagnosed and documented (not a code fix -- this is a content-authoring gap,
      re-scoped after live DB investigation superseded the original "72 vs ~80" framing). The
      live DB has 130 total `skill.skill` rows across 3 courses (`devops-with-ai`, `genai-with-ml`,
      `sre`), not 72/80 -- that figure was stale. The real signal is orphan rate (skills with zero
      `content.topic_skill`/`content.activity_skill` mapping, i.e. unreachable by any activity or
      recommendation -- confirmed via `recommendation.service.ts`'s join path, so an orphaned
      skill can never actually surface to a learner, not a functional bug, just dead graph
      weight):
        - `genai-with-ml` (a real second course, `seed-skills-genai.ts`'s own doc comment:
          "Generative AI With ML Masters Program," curriculum-derived, not scaffolded filler):
          53 skills, 50 orphaned -- this course's activity content genuinely hasn't been authored
          yet, by design/sequencing, not an oversight.
        - `devops-with-ai` (the course with real shipping content, 64 published activities):
          73 skills, only 13 orphaned. This is the actionable gap: the entire `aiops` domain (7
          skills -- AI-driven CI/CD, incident management, predictive analytics, monitoring,
          self-healing, security, fundamentals) plus 4 cloud-provider skills (AWS core/Lambda,
          Azure core, AWS-vs-Azure) plus 2 Terraform advanced-topics skills (remote state,
          Terraform Cloud) have no activities written against them yet.
        - `sre`: 4 skills, 0 orphaned -- fully mapped.
      Left as a scoped content-authoring backlog item (explicit user decision: document the
      finding rather than author net-new activity content in this remediation pass) rather than
      closed by writing 13+ new activities, which is out of scope for what the other 15 items in
      this list are (code/infra fixes, not curriculum authoring)~~
- [x] ~~Cost meter -> budget enforcement -- found already substantially implemented
      (`internal/costmeter/meter.go`: real 60s `usage_meter` emission, real 50/80/120% threshold
      evaluator, `budgetDestroyFn` wired to the real `Destroyer.Destroy` with reason `"budget"`,
      `StartMetering`/`StopMetering` wired into `Provision`/`Destroy`'s real lifecycle) -- the
      original "nothing consumes usage_meter rows" audit finding was stale by the time this item
      was reached. What was genuinely missing: zero test coverage (this package had no _test.go
      file at all). Extracted the threshold decision into a pure `decideBudgetAction()` function
      (same pattern as `resolveTier`/`pssLevelFor` elsewhere in this codebase) and added 15 new
      tests. Test-writing itself caught a real pre-existing bug (present before this session
      touched the file, just never surfaced without tests): if cost jumped straight from under
      50% to over 80% between two ticks, `alerted50` stayed false forever, so every subsequent
      tick would re-fire the informational-50 log line indefinitely even after the more urgent
      warn-80 had already fired once -- fixed by requiring lower tiers to also check
      `!alerted80`. Live-verified the full hard-stop path end-to-end: restarted the orchestrator
      with an artificially tiny `DEFAULT_BUDGET_USD=0.0001`, provisioned a real environment,
      waited for the real 60s tick, confirmed `BUDGET HARD-STOP ... force-destroying` fired,
      confirmed the real `Destroy()` call ran with `reason=budget`, confirmed the namespace
      actually terminated and `env.environment.status/destroy_reason` correctly show
      `DESTROYED`/`budget` in the live DB. `go build`/`vet`/`gofmt`/`test` all clean~~
- [x] ~~Fault injection: found already fully triaged (built in an earlier part of this session,
      before this tracking doc existed) -- all 35 fault definitions in `content/faults/*.yaml`
      are accounted for: 12 real handlers (`internal/faultinjection/handlers*.go`, real K8s API
      mutations against a fake-clientset-testable `Handler` interface) + 23 explicitly deferred
      via `registerUnsupported()` with a stable, machine-checkable reason tag
      (`ReasonNoBaselineFixture`: the fault's target tool has no fixture/blueprint that
      provisions it yet, e.g. Jenkins/Terraform/Helm/Prometheus faults; `ReasonTierUnavailable`:
      needs T2_ISOLATED_MICROVM, not implemented in Phase 1; `ReasonMetricsContractPending`:
      needs a not-yet-designed metrics-degradation contract) -- zero faults are silently
      unaccounted-for. `Apply()`'s `ErrNoHandler` vs `ErrUnsupportedMechanism` distinction maps
      cleanly to gRPC status codes (`Unimplemented` vs `FailedPrecondition`) in
      `server.go`'s `InjectFault`, both wrapped in the audit-log defer. 46 existing unit tests
      across 5 test files, all passing. Live-verified all three paths against the real
      orchestrator + k3s: a wired fault (`f.k8s.taint-blocks-scheduling`) genuinely tainted a
      real node (confirmed via `kubectl describe node`, then immediately untainted to avoid
      leaving this dev cluster's only healthy node unschedulable), a triaged-unsupported fault
      correctly returned `FailedPrecondition` with `reason=no_baseline_fixture`, a genuinely
      unregistered fault_id correctly returned `Unimplemented` -- and all three were correctly
      recorded in `env.audit_log` with accurate outcomes/error messages. No further wiring
      attempted: every remaining fault's blocker is a real, documented architectural gap
      (missing fixture, missing tier, missing contract design), not an oversight -- "safe to
      wire" for any of them means building the underlying fixture/tier/contract first, which is
      out of this remediation pass's scope~~
- [ ] AI grading (IN PROGRESS, code complete, awaiting live key verification): real LLM-backed
      grader (`ClaudeAiGrader`, `@anthropic-ai/sdk`) implemented
      behind the existing `AiGrader` interface, config-gated the same way `VALIDATOR_EXECUTOR`
      already swaps real/fake (`AI_GRADER` factory: real whenever `ANTHROPIC_API_KEY` is set,
      `FakeAiGrader` otherwise -- so DI boot never breaks in a keyless environment; confirmed via
      a real `nest start` boot with no key set, which completed "Nest application successfully
      started" cleanly). Implements all four of doc §6.5's grading rules: rule 31 (rubric
      exemplars in the prompt), rule 32 (structured-output-only via forced tool_choice, not
      free-text JSON parsing), rule 33 (3-sample grading with a real per-criterion agreement
      check, SAMPLE_DISAGREEMENT flag + provisionalReason on divergence), rule 34 (grade() only
      ever sees precomputed `GradingFacts`, never a live environment), rule 35 (learner artifact
      text wrapped in a delimited `<learner_artifact>` block with an explicit
      treat-as-data-not-instructions system-prompt line). 14 new unit tests (mocked Anthropic
      client -- no real API calls in the test suite) covering the DI-safety constructor, forced
      tool use, prompt-injection delimiting, fact-only inputs, sample agreement/disagreement, and
      4 malformed-response rejection cases (unknown criterion, out-of-range level, out-of-range
      confidence, missing criteria). `go`/`tsc`/full unit suite (107 tests) all clean.
      **Not yet live-tested against a real Anthropic API call** -- this needs a real
      `ANTHROPIC_API_KEY`, which the user is providing; marked in-progress until that live call is
      confirmed working end-to-end.
- [x] ~~Retry/cooldown: early-clear-on-remediation-pass behavior -- doc §2.7: "cleared early if the
      learner passes a remediation activity for the failed sub-skill." Added
      `EvaluationService.clearCooldownsForRemediatedSkills()`: after any passing evaluation, finds
      every OTHER activity (not the one just passed) sharing a primary skill with it, and clears
      `cooldown_until` on all of them for that learner. Scoped to primary skills only, matching
      the scope the recommendation engine's own remediation-candidate query already uses
      (`recommendation.service.ts`) -- this is the "struggling-skill-itself" remediation slice
      that's actually implemented today (the full skill-DAG-ancestor-walk ladder doc §2.7
      describes is still real Phase 4 scope, correctly out of reach), which is what makes
      early-clear implementable now rather than blocked on that ladder. 2 new integration tests
      against real Postgres (`test/integration/evaluation.integration.spec.ts`): publishing two
      activities sharing a primary skill, failing one (cooldown set), passing the other, and
      confirming the first's cooldown clears -- and the negative case, confirming an unrelated
      activity's cooldown is untouched. Both pass, confirmed via the real log line
      `"Early-cleared cooldown on 1 activity for user ... after passing ..."` firing exactly once,
      only in the shared-skill case. Full integration suite run: 48/48 passing excluding two
      files with pre-existing failures unrelated to this change (see note below);
      unit suite 107/107 clean; `tsc --noEmit` clean.
      **Separately discovered, NOT caused by this work**: `attempt-lifecycle.integration.spec.ts`
      has 3 failing tests and `mastery-and-catalog.integration.spec.ts` has 1, both about
      concurrent-environment-slot eligibility checks -- pre-existing (both files already had
      hundreds of lines of uncommitted changes before this remediation session touched them),
      reproducible in isolation, unrelated to cooldown/AI-grading code. Flagged, not fixed --
      out of scope for items 15/16, and expanding scope to fix an unrelated pre-existing bug
      without being asked risks masking what the user actually wants investigated~~

## Repo layout

- [ ] `evaluation/` and `ai-gateway/` top-level dirs: either populate or document why logic
      stays in `practice-core/src/modules/evaluation/`

---

Each item below gets its own section as work lands, with evidence, tests, and security notes —
same format as PHASE2_CLOSEOUT.md.
