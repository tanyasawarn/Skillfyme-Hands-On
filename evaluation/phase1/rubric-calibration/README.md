# Rubric calibration harness

Closes the calibration half of `PLAN_PHASE3_PROJECTS.md` **1.9 / B5** (and is
reusable for any future engineered rubric). Doc §6.5 rule 36: "Calibration
harness — a held-out set of human-scored submissions; the grader is run against
it and agreement (Cohen's kappa or exact-match rate) is checked before the
rubric is trusted."

## Layout

```
rub.architecture.v3/
  rub-calibration.md          # the record: threshold, last run, kappa result, sign-off
  cases/
    <case-id>.md              # one calibration submission: front-matter SME scores + the design text
```

Each case file is Markdown with a YAML front-matter block:

```markdown
---
case_id: strong-constrained-design
sme_scores:            # the human ground truth (1..4 per criterion)
  constraint_fit: 4
  failure_mode_reasoning: 3
  tradeoff_honesty: 4
  data_model_soundness: 3
  overall: 4
constraints_id: cs.web-api-tight-budget   # which deterministic constraint set the grader is handed
adversarial: false                        # true for the prompt-injection case
notes: >
  SME rationale for the scores (kept with the case so a re-score is auditable).
---

# <the learner design text the grader sees, verbatim>
...
```

## Run it

```
# needs ANTHROPIC_API_KEY (real grader). Without it the harness runs the
# FakeAiGrader and only checks plumbing (no kappa gate).
ANTHROPIC_API_KEY=sk-... \
  npx ts-node -r tsconfig-paths/register \
  practice-core/scripts/rubric-calibrate.ts rub.architecture.v3
```

It:

1. Loads every case under `rub.architecture.v3/cases/`.
2. Runs `ClaudeAiGrader.grade()` on each (deterministic constraint set fed in
   as ground truth, same path `ProjectGradingFactsService` uses in production).
3. Computes **Cohen's weighted kappa** (linear weights) between the grader's
   `level` and the SME `level`, per criterion and for `overall`.
4. Also reports exact-match rate and mean absolute level error.
5. Runs the **injection-defence assertion**: the `adversarial: true` case must
   score `overall <= 2` and its result must carry a prompt-injection flag.
6. **Exit 0** iff `overall` kappa `>= KAPPA_THRESHOLD` (default 0.6) **and** the
   injection assertion holds. Otherwise exit 1 and print the disagreements.

## Gate

`rub.architecture.v3` stays `human_review.policy:
ALWAYS_PROVISIONAL_UNTIL_CALIBRATED` — i.e. every score it produces is
provisional and 100% human-reviewed — until:

- this harness exits 0 with `overall` weighted kappa ≥ 0.6, and
- `rub.architecture.v3/rub-calibration.md` records the run (date, kappa,
  case count, the model id) and a reviewer signs it.

Only then does the milestone-1 design gate consume a non-provisional score.
The calibration **set** here is the deliverable; running it against the real
model and recording ≥ 0.6 is an SME task (memory.md §13.1 budgets ~2 wk).
