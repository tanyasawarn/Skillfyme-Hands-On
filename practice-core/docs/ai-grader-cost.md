# AI grader — cost model (₹100/user context)

The Claude AI grader (`ClaudeAiGrader`, `@anthropic-ai/sdk`, forced
tool-use) grades the incident-note artifact on **PRODUCTION_SIM attempts
only**. This is the cost per user per month and the levers.

## Per grading call

| Prompt component | ~tokens | cached? |
|---|---|---|
| System prompt (security + rubric-type framing) | ~350 | **yes** (`cache_control`) |
| Grading tool `input_schema` (derived from the rubric) | ~500 | **yes** (`cache_control`) |
| Rubric criteria + level descriptors + exemplars | ~1,200 | in the user prompt (not yet cached — see levers) |
| Deterministic ground truth (fault id, constraint summary, validator results) | ~400 | no (varies per attempt) |
| Command sequence (≤ 50 cmds) | ~600 | no |
| Learner's incident note | ~800 | no |
| **Input / call** | **~3,850** | |
| Output (tool_use JSON, 3 criteria) | ~700 | |

Model: `claude-sonnet-4-5` (`ANTHROPIC_GRADER_MODEL`). Pricing $3/M in,
$15/M out; cache write +25%, cache read −90%.

## Per graded artifact

`grade()` runs `sampleCount` calls (default **3**, `ANTHROPIC_GRADER_SAMPLE_COUNT`),
+ ~10% for retries → ~3.3 effective calls.

| Config | $/artifact | ₹/artifact |
|---|---|---|
| 3 samples, no caching | $0.073 | ₹6.0 |
| **3 samples, system+tool cached (current)** | **$0.052** | **₹4.3** |
| 1 sample, system+tool cached (post-calibration) | $0.017 | ₹1.4 |
| 1 sample, cached, `claude-haiku-4-5` | $0.005 | ₹0.4 |

## Per user per month

`memory.md` §10.5: 6 attempts/user/mo, ~20% sims → **1.2 graded incident
notes/user/mo**.

| Config | ₹/user/mo |
|---|---|
| 3 samples, no caching | ₹7.3 |
| **3 samples, cached (current default)** | **₹5.2** |
| 1 sample, cached (after `rub.incident-note.v2` calibration passes) | ₹1.7 |
| 1 sample, cached, haiku | ₹0.5 |
| steady-state: 10% audit instead of 100% grade (doc §6.5) | ₹0.5 → ₹0.05 |

**Current cost: ~₹5/user/month.** Fits ₹100 with the Sysbox host (~₹52
at 100 users) leaving large headroom.

## Levers, in order of ROI

1. **`ANTHROPIC_GRADER_SAMPLE_COUNT=1`** after `rub.incident-note.v2`
   passes calibration (`evaluation/phase1/rubric-calibration/rub.incident-note.v2/rub-calibration.md`
   records a passing run). −67%. The self-consistency flag matters most
   while a rubric is still being tuned; a calibrated, stable rubric
   doesn't need 3 samples per grade.
2. **Move the rubric block into a cached prefix.** The rubric criteria +
   exemplars (~1,200 tokens) are identical across every artifact for a
   rubric but currently sit in the per-call user prompt. Restructuring
   the user prompt so the rubric block leads and gets its own
   `cache_control` would cache ~1,200 more input tokens. −~15% more.
   (Not done yet — needs the user-prompt builder split so the varying
   part, artifact + facts, is a clean suffix.)
3. **`claude-haiku-4-5`** — −70% on top. Re-run calibration to confirm
   kappa still ≥ 0.60 before switching; incident-note grading is a
   structured, rubric-anchored task haiku may handle fine.
4. **Drop to the doc's ~10% random audit** once calibrated and trusted —
   the 100%-grade state is only the *pre-calibration* policy
   (`human_review.policy: ALWAYS_PROVISIONAL_UNTIL_CALIBRATED`). −90%.

## What's already done (this pass)

- `cache_control: ephemeral` on the system prompt and the grading tool
  (`claude-ai-grader.service.ts`). ~30% input cut, zero quality change.
- `ANTHROPIC_GRADER_SAMPLE_COUNT` env knob, clamped [1, 5].
