# evaluation/

**Status: intentionally empty.** The Evaluation logic is a **bounded module inside the
`practice-core` monolith**, not a separately deployed service — and that is a deliberate,
enforced decision, not an unfinished stub.

Real implementation: [`practice-core/src/modules/evaluation/`](../practice-core/src/modules/evaluation/)
— scoring engine, criteria registry, validator-run orchestration, artifact/rubric grading
pipeline, the AI-grader adapter.

## Why it lives in the monolith

D6 in the architecture doc names three *genuinely* extracted services: the Environment
Orchestrator (Go — its own language, credentials, and blast radius), the Evaluation workers,
and the AI Gateway. Of those three, **only the Environment Orchestrator has actually been
extracted.** Evaluation has not, on purpose:

- Its transactional boundaries are shared with the rest of Practice Core. `EvaluationService`,
  `ValidatorRunnerService` and `ArtifactService` read and write `attempt_score`,
  `attempt_events` and `learner_activity_state` **in the same transactions** as the attempt
  and mastery modules. That is exactly the condition D6 gives for keeping modules together:
  *"share transactional boundaries and are meant to stay in one deployable, so keeping them
  with one owner avoids splitting a single transaction across two people's PRs."*
- The two forces that would justify extraction do not bite yet:
  1. **Submission-burst scaling** ("everyone submits at 9pm") — there is no production load.
  2. **Untrusted-code isolation** — the `STATIC_ANALYSIS` / `TEST_SUITE` validator types that
     run learner code in the evaluation path are Phase 3 scope and not implemented. Today's
     validators are deterministic assertions executed by the Orchestrator against read-only
     credentials, not code execution inside Evaluation.

Building a hollow `evaluation/` service now — a thin HTTP/queue shell around code that still
shares a transaction with `attempt.service` — would add a distributed-transaction failure mode
for zero operational benefit and misrepresent how much of D6's target architecture exists.

## The module boundary is enforced

"Bounded module" only means something if the boundary is real. Code outside
`src/modules/evaluation/` may import the Evaluation module **only through its public seam** —
the files that `EvaluationModule` exports:

| Seam file | What it is |
|---|---|
| `evaluation.module` | the NestJS module (DI wiring) |
| `evaluation.service` | scoring / evaluation entry point |
| `artifact.service` | artifact submission + AI-graded rubric pipeline |
| `validator-runner.service` | validator-run orchestration |
| `validator-executor.interface` | the `VALIDATOR_EXECUTOR` injection token + its types |

A deep import of any other file under `evaluation/` (the scoring engine, the criteria
registry, the graders, the repositories) from a sibling module is a **lint error**, checked by
[`practice-core/eslint.boundaries.mjs`](../practice-core/eslint.boundaries.mjs) and run in CI
as `npm run lint:boundaries` (CI-blocking — separate from the broad, currently report-only
`npm run lint`).

Widening the seam is a deliberate act: add the basename to `SEAM` in `eslint.boundaries.mjs`
**and** to `EvaluationModule`'s `exports:` array in the same change.

## When to revisit extraction

Extract Evaluation into a real standalone service when **either** of these becomes true:

1. Submission bursts create a scaling need that a shared Practice Core deployment cannot
   absorb (queue-driven workers with their own HPA), **or**
2. A validator type that executes learner-supplied code in the evaluation path lands
   (`STATIC_ANALYSIS`, `TEST_SUITE`), requiring its own node pool and isolation boundary.

At that point the seam above is already the extraction interface: replace the in-process calls
with a NATS work queue + result event, give Evaluation its own tables or scoped write access,
and deploy it separately. The module structure makes that a refactor, not a rewrite.
