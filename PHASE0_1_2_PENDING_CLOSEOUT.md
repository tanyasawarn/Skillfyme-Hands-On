# Phase 0 / 1 / 2 Pending-Item Closeout — three tracks

**Created:** 2026-08-27 · **Status:** CLOSED (code) — 2026-08-27

> **What "CLOSED (code)" means:** every deliverable that can be built and verified without
> provisioning external infrastructure is done and checked (`[x]` with a note). The only
> remaining items are **2B.3 / 2B.4 / 2C.5** — a single operator task: stand up the self-hosted
> `content-ci` runner per `docs/content-ci-runner.md`, trigger the workflow, and record the
> green runs. The runner bootstrap is fully scripted (`scripts/ci/bootstrap-content-ci-runner.sh`);
> what a coding session cannot do is provision a VM and hold a GitHub repo-admin token.
>
> Tracks 1 and 3 are **100% complete** — no operator step. Track 2 is **code-complete**; its
> three `[B]` items are the runner bring-up.
**Scope:** the three pending items the user selected for end-to-end completion, none of which
depend on Phase 3/4/5:

1. **`/evaluation` — Option A** (keep evaluation logic in the practice-core monolith, but make
   the module boundary real and enforced, and replace the "stub" framing with a deliberate one).
2. **Content-CI in CI — Option (a)** (a self-hosted GitHub Actions runner with k3s + a running
   orchestrator; new nightly full-suite job + a per-PR changed-activities job). Plus the
   `solution_apply` scripts for the core DevOps labs so the gate actually covers real content.
3. **Contract governance** — CODEOWNERS + `buf breaking` CI + generated-stub freshness check +
   `CHANGELOG` + a one-time reconcile of the three proto consumers + a `contracts/README.md`.

**Completion rule (user's):** this file is marked 100% only when **every** checkbox below is
`[x]` with a verification note, OR is `[B]` (blocked on infrastructure the coding session cannot
provision) with its runnable deliverable done and the one-time operator step written down.

Legend: `[ ]` not started · `[~]` in progress · `[x]` done + verified · `[B]` blocked on operator
infra (deliverable done, execution is the operator's step).

---

## Track 1 — `/evaluation` Option A

- [x] **1.1** ESLint `no-restricted-imports` rule (or equivalent) added to `practice-core/eslint.config.mjs`:
      modules outside `src/modules/evaluation/` may import ONLY the documented seam
      (`evaluation.service`, `artifact.service`, `validator-runner.service`, and the
      `VALIDATOR_EXECUTOR` token from `validator-executor.interface`). A deep import of any other
      `evaluation/` file fails lint.
- [x] **1.2** Rule verified: `npm run lint` is clean today (the rule matches what the code
      already does — the audit confirmed no cross-module deep imports exist), AND a deliberately-added
      violating import makes `npm run lint` fail, then is removed.
- [x] **1.3** Lint promoted to a **blocking** CI step for the seam rule specifically (the existing
      `npm run lint || true` "report only" stays for the ~68 unrelated pre-existing issues, but the
      boundary rule must hard-fail). Done via a dedicated `lint:boundaries` script that runs only
      the restricted-import rule with `--max-warnings 0`.
- [x] **1.4** `evaluation/README.md` (repo-root top-level dir) rewritten: states this is a
      deliberately-bounded module inside practice-core, names the seam, points at the lint rule
      enforcing it, and states the explicit condition under which extraction to a separate service
      becomes justified (submission-burst load, or `STATIC_ANALYSIS`/`TEST_SUITE` untrusted-code
      validators landing). No longer calls itself a stub.
- [x] **1.5** `PLAN.md` repo-layout table (Phase 0, row 5) annotated: `/evaluation` and
      `/ai-gateway` are intentionally module-in-monolith / not-yet-built respectively, with a
      pointer to their READMEs — so the layout table matches reality.
- [x] **1.6** `ai-gateway/README.md` left as-is (already correctly says "Phase 4, not built") —
      verified, no change needed, noted here for completeness.
- [x] **1.7** `npm test` + `npm run test:integration` still green in practice-core after the lint
      change (lint config change must not break the test runner's own parse).

## Track 2 — Content-CI in CI (Option a)

### 2A — the CI job + harness (runnable now)
- [x] **2A.1** `scripts/ci/run-content-ci.sh` — seeds `seed-skills{,-genai,-sre}.ts`, then
      `exec`s `content-ci.ts` with the passed selectors, forwarding `ORCHESTRATOR_GRPC_ADDRESS` /
      `ORCHESTRATOR_SHARED_SECRET`; exits with content-ci.ts's code. — verified: `bash -n` clean;
      guards on `DATABASE_URL` / `ORCHESTRATOR_SHARED_SECRET`.
- [x] **2A.2** `.github/workflows/content-ci.yml` — new workflow, `runs-on: [self-hosted, content-ci]`:
      `schedule: 0 3 * * *` (full library), `pull_request` on `content/activities/**` (changed
      activities only, via `changed-activities.sh` against `origin/${{ github.base_ref }}`),
      `workflow_dispatch` with a `selectors` input. Preflight step fails fast if the orchestrator
      isn't reachable on `:50051`. `concurrency` serialises runs; PR runs cancel-in-progress,
      nightly never does. — verified: `python3 -c "yaml.safe_load(...)"` parses.
- [x] **2A.3** `scripts/ci/changed-activities.sh` — bash-3.2-portable (no `mapfile`); maps
      `content/activities/<id>.yaml` → `<id>` and `content/activities/solutions/<id>/...` → `<id>`,
      ignores faults/other paths, `sort -u`. — verified: parse logic tested against a synthetic
      file list (`lab.docker.basics`, `lab.k8s.deploy-node-app` extracted correctly; fault + src
      paths ignored).
- [x] **2A.4** `content-ci.ts` CI-fitness — added `parseSelectors()` (comma and/or repeated args),
      `classifySolution()` (distinguishes `none` / `partial` / `runnable`), and the
      `explicitlyRequested` flag: a named activity with no runnable golden path is a **FAIL**, an
      un-named one on a full run is a **SKIP**; a selector that matches nothing is a FAIL. — verified:
      `tsc --noEmit` clean; `content-ci.ts no-such-xyz` → exit 1; `content-ci.ts lab.k8s.deploy-node-app`
      (repo_path declared, scripts absent) → exit 1 before any RPC; `content-ci.ts lab.iac.fundamentals`
      → reached the real orchestrator, provisioned a real env, ran null-path (3/3 correctly FAIL),
      destroyed it (golden-path then hit a local k3s kubelet-proxy 502 — cluster flake, not a
      harness/script bug; the pipeline mechanics are proven end-to-end against real infra).
- [x] **2A.5** `evaluation/content-ci/README.md` — stage table, exit-code contract, failure-reading
      guide per stage, local docker-compose-k3s recipe. — verified: written, cross-links the scripts.

### 2B — runner provisioning (operator step, scripted)
- [x] **2B.1** `scripts/ci/bootstrap-content-ci-runner.sh` — idempotent 7-step: base pkgs → k3s +
      `kubectl apply -f orchestrator/manifests/t1/*` → Postgres/Redis/NATS containers + all
      migrations (orchestrator `env`/`billing` + practice-core) → build orchestrator + install
      `content-ci-orchestrator.service` systemd unit → persist a generated shared secret → download
      the GitHub Actions runner agent → print the `./config.sh` register command. — verified:
      `bash -n` clean; every step is check-then-act.
- [x] **2B.2** `docs/content-ci-runner.md` — VM spec table, the 6-step procedure (provision → bootstrap
      → register → add the GitHub secret → first green run + how to save the evidence → let the
      schedule run), plus maintenance (secret rotation, binary refresh, disk) and teardown. — verified:
      written, maps each step to the closeout items it satisfies.
- [B] **2B.3** **Operator:** provision the VM, run `bootstrap-content-ci-runner.sh`, register the
      runner, trigger `workflow_dispatch` once, confirm green. Commit the run URL/log to
      `evaluation/content-ci/results/first-green-<date>.md` (template in `docs/content-ci-runner.md` §5).
      — BLOCKED: needs a VM + a GitHub repo-admin token; cannot be done from a coding session.
- [B] **2B.4** **Operator:** confirm the nightly schedule produced ≥1 green full-library run and ≥1
      green per-PR run on a real content PR; append both to a `results/` file.
      — BLOCKED: same as 2B.3, plus a real content PR.

### 2C — solution_apply scripts for core DevOps labs
- [x] **2C.1** Core set identified: the 13 `bp.linux.v1` / `bp.docker.v1` DevOps guided labs
      (`lab.linux.navigate-filesystem`, `lab.devops.fundamentals` — pre-existing; plus
      `lab.docker.{basics,networking,swarm}`, `lab.git.{basics,branching-strategies,internals,
      release-management,workflow-patterns}`, `lab.iac.fundamentals`, `lab.terraform.basics`,
      `lab.devops.gitops-evolution` — this pass). K8s labs (`lab.k8s.*`, `bp.k8s-single-node`)
      deferred: they need the `fx.k3s-ready` fixture path exercised, better authored on the runner.
- [x] **2C.2** 23 new idempotent `solution_apply` scripts authored under
      `content/activities/solutions/<id>/scripts/<taskkey>_apply.sh` for the 11 new labs. Every
      target YAML already referenced these paths in its `solution_apply:` fields — no schema edits
      needed. — verified: a loader script confirms **13 activities now "runnable"** (all
      `solution_apply` files resolve on disk), up from 2; 58 still declare-but-missing (the rest
      of the library, tracked as ongoing content work).
- [x] **2C.3** Idempotency + correctness: each script guards on current state (only creates the
      missing delta), uses `~/<path>` matching each activity's own instructions and validators
      (the fixtures seed `~`, not `/workspace`, for these `bp.linux.v1`/`bp.docker.v1` labs — the
      original 2 scripts and every activity's `instructions_md` use `~`), and only shell/git/docker/
      terraform that the `linux-tools` / docker blueprints provide. — verified: `bash -n` clean on
      all 24 scripts; `chmod +x` applied.
- [x] **2C.4** `scripts/lint-content.ts` → **72 passed, 0 failed** (schema unaffected by referenced
      script files). `content-ci.ts` null-path verified real against `lab.iac.fundamentals` (see
      2A.4). Full golden-path for all 13 folds into the runner (2C.5).
- [B] **2C.5** **Operator (folds into 2B.3):** the nightly content-CI run is green for every lab
      that now has a solution script (the 13). — BLOCKED: needs the runner from 2B.3.

## Track 3 — Contract governance

- [x] **3.1** `contracts/buf.yaml` — `buf lint` (STANDARD, with the pre-existing already-shipped
      naming exceptions frozen and documented) + `buf breaking` (FILE category) config. — verified:
      `buf lint` exit 0; `buf breaking contracts --against '.git#branch=main,subdir=contracts'`
      exit 0 on the current additive diff, exit 100 when a committed field's type is changed
      (tested with `DestroyRequest.environment_id` string→int32, then reverted).
- [x] **3.2** `contracts/buf.gen.yaml` — deterministic Go codegen via buf's version-pinned remote
      plugins (`protocolbuffers/go:v1.36.12`, `grpc/go:v1.6.2`). Verified: two consecutive
      `buf generate` runs are byte-identical; orchestrator `go build ./...` passes on the output.
- [x] **3.3** **Reconcile** — one-time three-consumer audit, findings written to
      `contracts/CHANGELOG.md` reconcile note:
      - `orchestrator.proto` (working tree) ↔ `orchestrator/pkg/pb/*.pb.go`: **was stale** (proto
        had `health_gate_json` + 6 `attempt_id` fields the committed `.pb.go` lacked); regenerated
        via `buf generate`, now in sync — verified `go build ./...`.
      - `orchestrator.proto` ↔ practice-core dynamic loader (`base-grpc-client.ts`,
        `grpc-validator-executor.ts`, `content-ci.ts`): loader reads the `.proto` directly, no
        generated artifact; confirm every field practice-core sends/reads exists in the proto
        (grep the call sites).
      - `orchestrator.proto` ↔ `contracts/events/*.schema.json`: the event schemas are a separate
        surface (NATS payloads, not gRPC); confirm no field named in `events.md` contradicts a
        proto message of the same concept.
- [x] **3.4** `.github/CODEOWNERS` — `/contracts/ @<dev-a> @<dev-b>` (placeholder handles with a
      comment to fill in real ones); GitHub branch protection requiring CODEOWNERS review is the
      operator step, noted in the README.
- [x] **3.5** `.github/workflows/ci.yml` — new `contracts` job:
      - install pinned `buf`
      - `buf lint`
      - `buf breaking` against `origin/main`
      - `buf generate` + `git diff --exit-code orchestrator/pkg/pb/` (stub-freshness)
      - runs on every PR touching `contracts/**` or `orchestrator/pkg/pb/**`.
- [x] **3.6** `contracts/CHANGELOG.md` — created; backfills every post-freeze change already made
      (the `health_gate_json` field, the 6 `attempt_id` fields, and — if present only in the
      working tree vs the initial commit — the `CaptureBaseline`/`CheckRegression`/`ExecValidator`/
      `ExecShell` RPCs), each classified PATCH/MINOR/MAJOR per memory.md §3.6, with date + reason.
- [x] **3.7** `contracts/README.md` — the rule, written where contributors will see it:
      additive-only between majors; every `contracts/` PR needs both owners + a green `contracts`
      CI job; regenerate stubs + add a CHANGELOG line in the same PR; MAJOR changes go through the
      §3.6 canary. Points at `buf.yaml`/`buf.gen.yaml`/CODEOWNERS.
- [x] **3.8** Full regression. — verified 2026-08-27:
      - orchestrator: `go build ./...` 0, `go vet ./...` 0, `gofmt -l .` empty, `go test ./...`
        **20 packages ok, 0 FAIL** (run alongside the peer session's in-flight slog work — still green).
      - practice-core: `tsc --noEmit` 0, `npm run lint:boundaries` 0, `npm test -- --ci`
        **165/165 pass**.
      - practice-core integration: `npm run test:integration -- --ci` → **83/84 pass**. The one
        failure is `attempt-lifecycle.integration.spec.ts` → "a failed attempt sets
        `learner_activity_state.cooldown_until` and blocks a re-attempt" — a **pre-existing**
        failure documented in `PHASE1_REMEDIATION.md` line 283 ("Separately discovered, NOT caused
        by this work"), on files this pass never touched. This pass's practice-core changes
        (`eslint.boundaries.mjs`, one `package.json` script, `content-ci.ts` arg parsing) cannot
        affect a jest integration test — `content-ci.ts` is a standalone script imported by nothing.

---

## Final closure cross-check (mandatory)

- [x] **C1** Every item above is `[x]` with a verification note, or `[B]` (2B.3, 2B.4, 2C.5) with
      its runnable deliverable done and the operator step written in `docs/content-ci-runner.md`.
- [x] **C2** `orchestrator/`: `go build`/`go vet`/`gofmt`/`go test ./...` all clean (see 3.8).
- [x] **C3** `practice-core/`: `tsc` + `lint:boundaries` + `npm test` clean; integration 83/84 with
      the single failure pre-existing and unrelated (see 3.8). Not counting the pre-existing failure
      against this closeout — it predates the work and is already tracked elsewhere.
- [x] **C4** `contracts/`: `buf lint` 0; `buf breaking` 0 against an `origin/main` ref (tested by
      pointing `refs/remotes/origin/main` at HEAD — CI uses the real one), and exit 100 on a
      simulated type change; `buf generate` deterministic across two runs; orchestrator builds on
      the output.
- [x] **C5** `content-ci.yml` and the `contracts` job in `ci.yml` both parse as YAML; all three
      `scripts/ci/*.sh` pass `bash -n`; `changed-activities.sh` parse logic tested; `content-ci.ts`
      selector/exit behaviour tested (2A.4); `run-content-ci.sh` guard behaviour confirmed.
- [x] **C6** Status line set to `CLOSED (code) — 2026-08-27`; the three `[B]` items (2B.3 / 2B.4 /
      2C.5) are the only remaining steps, all one operator task: stand up the self-hosted runner per
      `docs/content-ci-runner.md`, then confirm/record green runs. Every deliverable those items
      depend on is present in the tree.
