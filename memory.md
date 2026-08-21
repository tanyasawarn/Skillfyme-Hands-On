# Practice Engine — End-to-End Technical & Product Architecture

**Document type:** Architecture blueprint (implementation-ready)
**Audience:** Engineering leadership, platform team, curriculum/content team, security & finance
**Scope:** Product architecture, learning architecture, execution infrastructure, evaluation, AI layer, data model, security, cost, roadmap

## Table of Contents

| Part | Contents |
|---|---|
| 0 | Executive summary and the twelve decisions that shape everything |
| I | Product architecture and learning architecture |
| II | Curriculum mapping, skill graph, mastery and adaptive engine |
| III | Content model, authoring CMS, versioning |
| IV | Activity lifecycle and attempt tracking |
| V | Execution environment architecture |
| VI | Validation architecture and scoring engine |
| VII | AI mentor and AI evaluation architecture |
| VIII | System architecture, component architecture, APIs, database model |
| IX | Security architecture |
| X | Cloud cost architecture |
| XI | Observability and admin analytics |
| XII | MVP, phase roadmap, technology choices, scalability, risks |

---

# Part 0 — Executive Summary

## 0.1 What we are actually building

Three products wearing one coat:

1. **A workload execution platform.** Untrusted people run arbitrary Linux, Docker, Kubernetes, Terraform and cloud commands. This is the hard part, and it is an infrastructure and security problem, not a learning problem.
2. **A measurement platform.** Turning "the learner did stuff in a sandbox" into a defensible, reproducible statement about skill. This is a data modelling and validation problem.
3. **A curriculum-integrated learning product.** Sequencing, recommendation, mastery, and progression. This is a graph and algorithms problem.

Most teams that attempt this fail on #1 (cost and security) or on the *content supply chain* (they build a beautiful engine and can only author six labs a quarter). The architecture below is deliberately shaped around those two failure modes.

## 0.2 The single most important framing

*The Practice Engine is not "labs with a terminal." It is a **claim-generation system**.*

*Every learner action produces **evidence**; every evidence stream feeds **validators**; validators emit **signals**; signals feed a **scoring engine**; scores update a **skill mastery model**; the mastery model drives **recommendation**. Everything else — terminal, editor, cloud accounts, AI mentor — is plumbing that feeds this pipeline.*

If you design the evidence → signal → score → mastery pipeline first, every other component has a clear contract. If you design the terminal first, you will retrofit measurement forever.

## 0.3 The twelve decisions

Each is expanded later with What → Why → How → Trade-offs.

| # | Decision | Summary |
|---|---|---|
| D1 | **Tiered execution model** | Four environment tiers (browser-WASM → shared container → sandboxed VM/K8s → real vended cloud account). Activity authors declare requirements; the orchestrator picks the cheapest tier that satisfies them. |
| D2 | **Deterministic-first validation** | An LLM never decides pass/fail on anything mechanically checkable. AI grades only open-ended artifacts (design docs, reasoning, code quality) and always as *advisory-with-rubric*, never as sole authority on a certification-bearing outcome. |
| D3 | **Evidence is event-sourced and immutable** | `attempt_events` is an append-only log. Scores are *derived*, recomputable, and versioned. This makes re-grading, rubric changes, appeals and audits tractable. |
| D4 | **Content-as-code with a DB projection** | Activity definitions live as versioned specs (YAML/JSON) in Git, validated by CI, published into Postgres as immutable versions. Admin UI is a front-end over the same schema. |
| D5 | **Two separate graphs** | The *Curriculum graph* (Course→Module→Topic→Subtopic) is a business/marketing artifact and changes often. The *Skill graph* is a DAG of capabilities and changes slowly. Activities bind to both. Never conflate them. |
| D6 | **Modular monolith + three extracted services** | One API monolith (bounded modules) plus three genuinely separate services: **Environment Orchestrator** (Go), **Evaluation Workers** (Python), **AI Gateway** (Python). They have different scaling, blast-radius, and language profiles. |
| D7 | **Environments are cattle, workspaces are pets** | The runtime environment is disposable and reconstructable. The learner's *work* (files, git repo, IaC state, DB dump) is snapshotted to object storage and restorable into a fresh environment. |
| D8 | **Cost is a first-class architectural constraint** | Every activity carries a cost budget; every attempt carries a metered spend; hard kill-switches at learner, course and tenant level. Target: median guided lab under $0.05, production sim under $0.60, full cloud project under $6.00 per attempt. |
| D9 | **Zero standing cloud credentials in learner environments** | Credentials are brokered: short-lived STS/OIDC sessions scoped to a vended sandbox account governed by SCPs, with automatic nuke on TTL. |
| D10 | **Mastery via Bayesian Knowledge Tracing + Elo difficulty** | Skill mastery is a probability, not a percentage of labs completed. Activities carry an Elo-style difficulty that self-calibrates from population data. |
| D11 | **AI mentor never sees the solution in Lab/Sim modes** | Solution artifacts live in a separate index accessible only to the grader context. The mentor works from spec + live environment state + learner transcript, with a hint-escalation ladder. |
| D12 | **Phase 1 ships one vertical slice, not one horizontal layer** | MVP = Guided Labs on a single execution tier for a single course track, with full evidence→score→mastery pipeline. Depth over breadth. |

## 0.4 Non-negotiable constraints derived from the brief

- Thousands of concurrent learners → environment density and warm pooling matter more than per-environment richness.
- Real cloud (AWS/Azure/GCP) work → account vending + nuke + SCP is mandatory; you cannot do this with IAM users in one account.
- "Evaluate the process, not just the result" → command/action telemetry must be captured at the environment boundary, not trusted from the client.
- "Progressively reduce guidance" → guidance level must be a *runtime parameter*, not baked into content. One activity spec, multiple assistance profiles.

---

# Part I — Product Architecture and Learning Architecture

## 1.1 The pedagogical spine

The brief states the progression:

```
Learn → Follow → Implement → Troubleshoot → Design → Build → Defend
```

Map this to modes and to what the system withholds:

| Stage | Mode | What the platform provides | What it withholds | Primary evidence |
|---|---|---|---|---|
| Learn | (Course content — outside Practice) | Explanation | — | — |
| Follow | **Guided Lab L1–L2** | Step list, exact goal per step, hints, validation per step | Nothing much; it's scaffolding | Per-step validator results |
| Implement | **Guided Lab L2–L3** | Goal per step, no exact commands | Commands, syntax | Terminal transcript + end-state assertions |
| Troubleshoot | **Production Implementation L3–L4** | Broken scenario, symptoms, access | Root cause, remediation path | Diagnostic action sequence + recovery assertions |
| Design | **Project L4** | Requirements, constraints, acceptance criteria | Architecture, tech choice | Design document + architecture rubric |
| Build | **Project L4–L5** | Requirements only | Everything | Repo + live infra + automated acceptance tests |
| Defend | **Project L5 — viva** | Reviewer questions (AI or human) | — | Transcript scored against reasoning rubric |

**Architectural consequence:** the three modes are not three subsystems. They are three **configurations of one Activity Runtime** differing along five axes:

```
guidance_density        high ────────────────────► none
environment_complexity  single container ────────► multi-service real cloud
fault_injection         none ────────────────────► scripted + chaotic
validation_granularity  per-step ────────────────► terminal acceptance only
ai_persona              tutor ───► senior engineer ───► reviewer
```

Building one runtime with five knobs is dramatically cheaper than building three engines. This is the highest-leverage product-architecture decision in the document.

**Trade-off:** a unified runtime is initially more abstract and slightly slower to ship the first Guided Lab. It pays back by Phase 2. If you build three engines you will triple your validator code, your telemetry code, and your content tooling.

## 1.2 Practice navigation and information architecture

```
Practice
├── Home (Continue / Recommended / Mastery snapshot / Recent)
├── Guided Labs           → catalog, filtered by course & skill & difficulty
├── Production Implementations
├── Projects
├── Skills                → skill graph view, mastery per skill, "practice this skill"
└── History                → all attempts, scores, feedback, re-open workspace
```

Design rules:

4. **Home is a decision surface, not a dashboard.** Its job is to make the next click obvious. Maximum three primary CTAs: *Continue attempt*, *Recommended next*, *Fix a weak skill*.
5. **Mode is a filter, not a silo.** A learner searching "Kubernetes CrashLoopBackOff" should see a Lab, a Sim and a Project. Mode selection is how *ready* they feel, not what they want to learn about.
6. **The catalog is generated from the skill graph**, not hand-curated per course. A single Sim can appear under DevOps, SRE and Cloud Engineering because it maps to shared skills.

## 1.3 Mode specifications

### 1.3.1 Guided Lab

**Purpose:** convert declarative knowledge into procedural fluency with low failure cost.

Contract:

- Environment pre-seeded to a known **initial state** (a "fixture") — never an empty box.
- Decomposed into **tasks**; each task has 1..n **validators**.
- Validation is available on demand *and* runs automatically on a debounce after relevant activity.
- **Hint ladder** per task: nudge → conceptual → directive → reveal-command (each level carries a scoring penalty).
- A task can be **skipped** — the system then applies a "repair action" to bring the environment to the post-task state so the learner can continue. This is essential; without it, one stuck learner abandons the whole lab.
- Completion criteria: all *required* tasks pass. Optional tasks contribute to score, not completion.

**Key architectural implication: the repair action / state-forcing mechanism.** Every task must ship with an idempotent `solution_apply` script that the platform can run to force the post-state. This is also your content QA harness (see §3.5) and the basis of automated content testing.

### 1.3.2 Production Implementation (Simulation)

**Purpose:** diagnostic and remediation skill under realistic ambiguity.

Contract:

- Environment provisioned to a **broken state** via a **fault injection manifest** applied after a healthy baseline is confirmed. Break *after* verifying healthy — otherwise you cannot distinguish "authored fault" from "provisioning flake."
- Learner receives a **ticket**: symptom description, business impact, SLO context, access credentials, a runbook link that is deliberately incomplete.
- Optional **escalating pressure**: a scripted secondary event at T+N minutes (e.g. second replica dies) for L4+.
- The learner must produce: a **fix** (validated deterministically) and an **incident note** (root cause, remediation, prevention) — validated by AI rubric.
- **Process evidence matters:** the sequence of diagnostic commands is scored against a *diagnostic efficiency* signal (did they look at logs/events/describe before restarting blindly?).

Fault library is a first-class reusable asset — see §3.4.

### 1.3.3 Project Implementation

**Purpose:** design judgement, integration ability, production thinking, and defence.

Contract:

- **Requirements pack**: business, functional, non-functional, constraints, acceptance criteria, evaluation rubric, stretch goals — all authored, all versioned.
- Long-running: days to weeks. Therefore the environment is **suspendable** and the workspace is **durable** (Git repo provisioned per learner in the platform's Git server).
- **Milestone gates**: design submission → infra submission → implementation → hardening → final. Each gate has its own validators and rubric slice. This prevents the "learner disappears for 3 weeks and submits nothing" failure and gives the recommendation engine intermediate signal.
- **Acceptance test suite** runs against the learner's deployed system (black-box HTTP/CLI probes, chaos probes, security scans).
- **Defence step**: structured viva. AI reviewer asks 5–8 questions derived from the learner's *own* submitted architecture, scored on a reasoning rubric. Human review sampled.

## 1.4 Difficulty model

Five levels, defined by *what is withheld and what is injected*, not by vague "hard/harder."

| Level | Name | Instructions | Hints | AI | Environment | Faults | Time | Expected output |
|---|---|---|---|---|---|---|---|---|
| L1 | Guided | Exact commands shown | Free, no penalty | Full tutor, may show syntax | Single container, pre-seeded | None | Generous (3× median) | Correct end state |
| L2 | Assisted | Goal per step, no commands | 3-level ladder, small penalty | Tutor; explains errors, no full commands | 1–3 containers | Trivial (typos in provided config) | 2× median | End state + brief rationale |
| L3 | Independent | Outcome only, tasks listed | 2-level ladder, real penalty | Socratic only; no solutions | Multi-service, real tooling | 1 authored fault | 1.3× median | End state + written approach |
| L4 | Production | Ticket / requirements only | 1 nudge, heavy penalty | Senior-engineer persona; asks more than answers | Real cloud sandbox, multi-component | 1–2 faults + 1 timed escalation | Hard SLA | Fix + incident note / working system + design doc |
| L5 | Expert | Requirement + hostile constraints (cost cap, no downtime, legacy component) | None | Reviewer only, post-submission | Full multi-account/multi-cluster | Chaotic + interacting faults | Hard SLA + interruptions | System + design + defence |

**How difficulty is stored:** `difficulty_level` (L1–L5) is the *authored* label. `difficulty_elo` is the *measured* difficulty, recalibrated nightly from attempt outcomes (§2.6). Recommendation uses Elo; catalog display uses the label. When measured Elo diverges from the label by more than a threshold, flag to content admins — that is one of your best content-quality signals.

## 1.5 Learner journey (end to end)

```
[1] Learner completes a Topic in the course
     │ curriculum-progress event
     ▼
[2] Recommendation Engine re-scores candidate activities
     │
     ▼
[3] Practice Home: "Recommended — Guided Lab: Deploy a Node.js app to Kubernetes (L2, ~35 min)"
     │ learner clicks Start
     ▼
[4] Eligibility check (prereq closure, quota, cost budget, concurrent-env limit)
     │ pass
     ▼
[5] Attempt created (status=PROVISIONING). Environment claimed from warm pool.
     │ WebSocket: provisioning progress
     ▼ p50 3s (pooled) / p95 45s (cold cloud)
[6] Workspace opens: instructions pane | terminal | editor | task checklist | mentor
     │
     ▼
[7] Learner works. Every command, file write, validator run, hint request is an event.
     │ autosave + periodic workspace snapshot
     ▼
[8] Learner runs "Check" (or auto-validation fires)
     │ Validator job runs OUT-OF-BAND with read-only access
     ▼
[9] Task results stream back. Failures return authored diagnostic feedback.
     │
     ▼
[10] All required tasks pass → Submit
     │
     ▼
[11] Evaluation Engine: collect signals → apply rubric v_n → score → feedback
     │ AI portions run async; deterministic portion is instant
     ▼
[12] Result page: score breakdown, per-criterion feedback, model solution (now unlocked),
     skill mastery delta, "what to do next"
     │
     ▼
[13] Mastery update (BKT) → Recommendation refresh → optional badge/certificate
     │
     ▼
[14] Environment destroyed; workspace snapshot retained per policy; attempt sealed
```

**Abandonment path** (critical, and usually forgotten): at step 7, if the learner closes the tab —

- Environment enters `IDLE` after N minutes without input.
- At `IDLE + M`, workspace is snapshotted and the environment is **destroyed** (not paused — paused compute still costs).
- Attempt status → `SUSPENDED`. It appears in "Continue Practice."
- On resume, a fresh environment is provisioned and the snapshot restored. The learner sees "restoring your work."
- Suspended attempts expire after the activity's `resume_window` (e.g. 7 days for labs, 30 for projects), then are auto-submitted or auto-abandoned per activity policy.

Defaults: labs N=15min, M=10min. Projects N=30min, M=5min (projects are expensive; kill fast, restore is cheap because the work is in Git).

---

# Part II — Curriculum Mapping, Skill Graph, and the Adaptive Engine

## 2.1 The two-graph principle (D5)

**What:** Maintain a *Curriculum Hierarchy* and a *Skill Graph* as separate structures, joined only through activities and through an explicit `topic_skill` mapping table.

**Why:**

- The curriculum hierarchy is a **product packaging artifact**. "DevOps With AI" is a course you sell; its module order reflects marketing, cohort scheduling and instructor availability. It gets reshuffled.
- The skill graph is an **epistemic artifact**. "You need container networking before you can debug a Kubernetes Service" is true regardless of which course a learner bought.
- Nine courses in the brief overlap heavily (DevOps / SRE / Cloud / MLOps share 60%+ of skills). Modelling prerequisites in the curriculum tree means authoring the same prerequisite nine times and drifting nine ways.

**How:**

```
Course ──1:N── Module ──1:N── Topic ──1:N── Subtopic
                                  │
                                  └──M:N── Skill (topic_skill: coverage_weight, bloom_level)

Skill ──M:N (self, DAG)── Skill  (skill_edge: type, strength)

PracticeActivity ──M:N── Skill   (activity_skill: weight, is_primary, bloom_level)
PracticeActivity ──M:N── Topic   (activity_topic: relevance)  ← for catalog placement
```

**Trade-offs:** Two graphs means a mapping burden and a governance question ("who owns the skill taxonomy?"). Mitigate with (a) a small skill taxonomy — target 300–600 skills across all nine courses, not 5,000; (b) a required review step when a new skill is proposed; (c) an admin report showing orphan skills (no activity) and orphan topics (no skill).

## 2.2 The required mapping chain

The brief's chain — Course → Module → Topic → Subtopic → Skill → Practice Activity → Difficulty → Assessment → Mastery — resolves like this:

```
Course "DevOps With AI"
└ Module "Kubernetes"
  └ Topic "Deployments"
    └ Subtopic "Rolling updates & rollbacks"
        ├ topic_skill → skill:k8s.deployments       (weight .5, bloom: apply)
        ├ topic_skill → skill:k8s.yaml               (weight .2, bloom: apply)
        └ topic_skill → skill:k8s.troubleshooting    (weight .3, bloom: analyze)

skill:k8s.deployments
  ├ activity_skill ← lab:k8s-deploy-node-app        (L2, primary, weight 1.0)
  ├ activity_skill ← sim:k8s-failed-rollout          (L4, primary, weight .6)
  └ activity_skill ← project:k8s-deploy-platform     (L4, secondary, weight .3)

Attempt(lab:k8s-deploy-node-app) → Score 0.86
  → evidence apportioned to skills by activity_skill.weight
  → BKT update on skill:k8s.deployments  P(mastery) .41 → .63
  → mastery feeds recommendation + dashboard + certification gate
```

**Storage:** Postgres. The skill graph is small enough (hundreds of nodes, low thousands of edges) that a dedicated graph DB is unjustified. Use a recursive CTE for prerequisite closure and materialise the closure into `skill_closure(ancestor_id, descendant_id, depth)` refreshed on skill-graph publish. Closure lookups then become a single indexed read — important because the recommendation engine hits it on every request.

**Trade-off:** materialised closure must be rebuilt on every graph edit. At this scale rebuild is sub-second; do it synchronously inside the publish transaction.

## 2.3 Skill graph data model and edge semantics

Not all skill edges mean the same thing. Encode the type — it changes the recommendation math.

| Edge type | Meaning | Effect on recommendation |
|---|---|---|
| `REQUIRES` | Hard prerequisite. Cannot meaningfully attempt without it. | Gate: activity ineligible if `P(mastery) < 0.5` on any REQUIRES ancestor |
| `BUILDS_ON` | Soft prerequisite. Helpful, not blocking. | Penalty on ranking score; surfaced as "you may want to review X first" |
| `SIBLING` | Commonly co-occurring, no ordering | Diversity/exploration bonus |
| `SPECIALIZES` | Narrower instance of a broader skill | Mastery propagates *upward* at reduced weight |
| `SUPERSEDES` | Newer tech replacing older | Deprecation & content-lifecycle signal |

Example encoded chain:

```
linux.cli ──REQUIRES──► docker.basics ──REQUIRES──► docker.networking
    │                        │
    └──REQUIRES──► k8s.core ◄────┘
                       │
         ┌──SPECIALIZES────────────┼──REQUIRES──► k8s.deployments
         │                         │                   │
k8s.deployments.rollout            │                REQUIRES
         ▼                         ▼                   ▼
   k8s.networking ──────► k8s.troubleshooting
                                    │
                                 REQUIRES
                                    ▼
                              k8s.production
```

**Mastery propagation rules:**

- `SPECIALIZES` child mastery raises parent by `0.3 × delta` (capped) — demonstrating rollout skill is weak evidence for general deployment skill.
- Parent mastery does *not* propagate down. Knowing "Kubernetes" generally says nothing about your rollout-debugging.
- `REQUIRES` mastery is never inferred from descendants; if you passed an advanced lab, that is *some* evidence you know the prerequisite — apply a small upward nudge (`0.15 × delta`) but never enough to satisfy a gate on its own.

## 2.4 Mastery model — Bayesian Knowledge Tracing (D10)

**What:** For each (learner, skill) pair, maintain P(L) — the probability the learner has mastered the skill — updated after each evidence event.

**Why not "% of labs completed":** completion percentage cannot express uncertainty, cannot decay, cannot distinguish "passed one easy lab" from "passed three hard ones," and cannot handle partial credit. It also produces the classic failure where a learner is 100% "complete" and cannot do the job.

**How — four parameters per skill** (authored initially, calibrated later):

| Param | Meaning | Typical |
|---|---|---|
| `p_init` | Prior mastery before any evidence | 0.15 |
| `p_transit` | P(learn it from one practice event) | 0.20–0.35 |
| `p_slip` | P(fail despite mastery) | 0.10 |
| `p_guess` | P(pass without mastery) | 0.08 (low — you can't fake a working deployment) |

Update on evidence (score `s ∈ [0,1]` on an activity with weight `w` toward the skill):

1. Binarise with partial credit: treat as evidence strength `e = w × (s - pass_threshold)/(1 - pass_threshold)`, clipped.
2. Difficulty-adjust: scale `p_guess` down and `p_slip` up when activity Elo >> learner Elo, i.e. passing a hard thing is stronger evidence; failing a hard thing is weak evidence.
3. Bayes update `P(L | evidence)`.
4. Apply learning transit: `P(L)' = P(L) + (1 - P(L)) × p_transit × [evidence was a genuine attempt]`
5. Persist to `skill_mastery` with the evidence id (auditability).

**Decay:** apply a half-life decay so mastery ages. Suggested half-life 120 days for tool skills (kubectl syntax), 365 days for conceptual skills (distributed systems reasoning). Store `decay_half_life_days` on the skill. Decay is computed lazily at read time from `last_evidence_at` — do not run a nightly job over every learner×skill row.

**Mastery bands for UI and gating:**

| P(L) | Band | Meaning |
|---|---|---|
| < 0.30 | Not started / Novice | |
| 0.30–0.55 | Developing | |
| 0.55–0.75 | Competent | Satisfies REQUIRES gates |
| 0.75–0.90 | Proficient | Eligible for L4 activities |
| > 0.90 | Mastered | Certification-eligible; enters spaced-repetition rotation |

**Trade-offs:** BKT needs parameter tuning and is opaque to learners ("why is my mastery 0.62?"). Mitigate by *displaying* bands and evidence ("Competent — based on 3 labs and 1 production sim"), never raw probabilities. Alternative considered: Item Response Theory (better calibrated, needs far more data) and simple Elo (simpler, but no per-skill uncertainty). BKT is the right complexity for this stage; the data model (evidence events) supports swapping to IRT later without migration.

## 2.5 Adaptive Practice Engine — recommendation architecture

Four-stage pipeline, classic and defensible:

```
┌─ 1. CANDIDATE GENERATION ────────────────────────────────────────────┐
│ Sources (union, each capped):                                        │
│  a. Curriculum-adjacent: activities on topics the learner just       │
│     completed or is currently in                                     │
│  b. Remediation: activities whose PRIMARY skill has low mastery      │
│     AND that skill has a failed/struggled attempt in last 30d        │
│  c. Spaced repetition: mastered skills past their review-due date    │
│  d. Progression: next-difficulty activity for skills at Competent+   │
│  e. Unblocking: activities that unlock the most downstream skills    │
│     (highest betweenness in the skill DAG among reachable nodes)     │
│  → ~200 candidates                                                   │
└──────────────────────────────┬───────────────────────────────────────┘
                                ▼
┌─ 2. ELIGIBILITY FILTER (hard rules) ─────────────────────────────────┐
│ ✗ REQUIRES-prereq mastery < 0.55                                     │
│ ✗ Already passed and not review-due                                  │
│ ✗ Not published / not in learner's enrolled courses (unless explore) │
│ ✗ Environment tier unavailable / over cost budget for learner today  │
│ ✗ Retry cooldown active                                              │
│  → ~40 candidates                                                    │
└──────────────────────────────┬───────────────────────────────────────┘
                                ▼
┌─ 3. SCORING ─────────────────────────────────────────────────────────┐
│ score = Σ wᵢ · fᵢ  (weights tenant-configurable)                     │
│                                                                        │
│ f1 MasteryGap        Σ(target − P(L)) × activity_skill.weight   .30  │
│ f2 DifficultyMatch    gaussian(learner_elo − activity_elo, σ)   .20  │
│    peaked so P(success) ≈ 0.75 — the "desirable difficulty" band    │
│ f3 CurriculumAlign    1.0 current topic, .6 prior, .2 future    .20  │
│ f4 UnlockValue        normalised downstream skill count         .10  │
│ f5 Recency/Spacing    review-due urgency (SM-2-ish curve)       .10  │
│ f6 ModeBalance        penalise 4th consecutive same-mode        .05  │
│ f7 CostFit            penalise expensive tiers when budget tight .05 │
│                                                                        │
│ Penalties: recent failure on same activity (×0.4 unless remediation  │
│ path), learner explicitly dismissed (×0.1 for 30d)                   │
└──────────────────────────────┬───────────────────────────────────────┘
                                ▼
┌─ 4. RE-RANK & PACKAGE ───────────────────────────────────────────────┐
│ • Diversity: max 2 per skill, max 3 per mode in top 8                │
│ • Serendipity slot: 1 exploration item outside current topic         │
│ • Duration fit: at least one item under 20 min ("I have a break")    │
│ • Attach EXPLANATION per item (required — see below)                 │
└────────────────────────────────────────────────────────────────────────┘
```

**Every recommendation must carry a human-readable reason.** Store it as structured data (`reason_code` + params), render in UI: *"Recommended because you struggled with pod scheduling in your last attempt."* This is not cosmetic — it is how you debug the recommender, how learners trust it, and how you collect feedback signal (thumbs-down on a reason is a labelled training example later).

**Why rules before ML:** you will not have enough attempt data for a learned ranker until Phase 4 at the earliest. A transparent weighted-feature ranker is debuggable, tunable by curriculum staff, and produces the exact feature vectors you would need to train a model later. Log `(features, shown, clicked, started, completed, score)` from day one so the ML path is open.

**Trade-off:** hand-tuned weights drift out of calibration and invite bikeshedding. Mitigate by making weights a versioned config object with an A/B assignment key, and by tracking one north-star metric per variant (see §11.4).

## 2.6 Difficulty calibration (Elo)

Treat each attempt as a match: learner rating `R_l` vs activity rating `R_a`.

```
expected_pass = 1 / (1 + 10^((R_a − R_l)/400))
outcome       = 1 if passed on first submission, 0.5 if passed after retries, 0 if abandoned/failed
R_l' = R_l + K_l × (outcome − expected_pass)
R_a' = R_a − K_a × (outcome − expected_pass)
```

- `K_l` decays with attempt count (32 → 12) so ratings stabilise.
- `K_a` is small (4) and frozen once an activity has >500 attempts, so a cohort of beginners doesn't wreck a calibrated activity.
- Ratings are **per skill-cluster**, not global. A learner is 1450 in Kubernetes and 1100 in Terraform. Cluster = top-level skill graph domain (~12 domains).

**Uses:** difficulty-match feature (f2), content quality alerts (authored L2 measuring as Elo 1600 → mis-labelled), cohort readiness reports.

## 2.7 Worked adaptive example (from the brief)

Learner fails Kubernetes troubleshooting three times.

```
Evidence:  3 failed attempts on sim:k8s-crashloop (Elo 1520), learner k8s Elo 1280
           Hint usage: max ladder reached each time
           Diagnostic-efficiency signal: 0.21 (restarted pods before reading events/logs)

Inference: P(L | k8s.troubleshooting) drops 0.48 → 0.19
           Sub-skill attribution from validator-level signals:
             ✗ k8s.observability.events (never ran `kubectl describe`)  P(L)=0.11
             ✗ k8s.pod.lifecycle (misread CrashLoopBackOff)             P(L)=0.22
             ✓ k8s.core                                                 P(L)=0.71

Diagnosis: the weak skill is not "troubleshooting" — it is `k8s.observability.events`
           and `k8s.pod.lifecycle`. The graph localises the failure.

Prescription (remediation ladder, auto-generated):
  1. lab:k8s-read-the-events         L1, 12 min → targets k8s.observability.events
  2. lab:k8s-pod-lifecycle-states    L2, 20 min → targets k8s.pod.lifecycle
  3. lab:k8s-debug-crashloop-guided  L2, 25 min → guided version of the failed sim
  4. sim:k8s-crashloop (RETRY)       L4         → gated: only offered when both
                                                    sub-skills reach P(L) ≥ 0.55
  5. project:k8s-production-platform L4         → long-horizon
```

**Note the crucial move:** the remediation ladder is generated by **walking down the skill DAG from the failed skill to its unmastered ancestors**, then selecting the lowest-difficulty activity per ancestor. It is not a hand-authored "if fail X then Y" table — that does not scale to hundreds of activities.

**Retry policy:** after a failure, the same activity is not immediately re-offered. Cooldown = `min(24h × 2^(retry_count-1), 7d)` OR "cleared early if the learner passes a remediation activity for the failed sub-skill." That second clause is the whole point — retries should be *earned by learning*, not by waiting.

---

# Part III — Content Architecture: Authoring, Schema, Versioning

## 3.1 The content supply chain is the real bottleneck

**Blunt assessment:** the engine is a 9-month build. Filling it with 400+ high-quality, validated, non-flaky activities across nine tracks is a multi-year, continuous operation. Architect for authoring throughput or the platform will ship empty.

Design responses:

7. **Content-as-code** (D4) — authors work in a repo with CI, not only a web form.
8. **Composable fixtures and faults** — reuse, not re-author.
9. **Automated content testing** — every activity has a "golden path" bot run in CI.
10. **AI-assisted authoring** — generate first drafts of tasks, hints, distractors and validator scaffolds from a topic + solution repo. Human approves. This is the single highest-ROI internal AI use case, far above the learner-facing mentor.

## 3.2 Activity specification schema

An `ActivityVersion` is an immutable document. Illustrative shape (YAML in Git, JSONB projection in Postgres):

```yaml
id: lab.k8s.deploy-node-app
version: 7
mode: GUIDED_LAB              # GUIDED_LAB | PRODUCTION_SIM | PROJECT
status: PUBLISHED

meta:
  title: Deploy a Node.js application to Kubernetes
  summary: Build, push and expose a containerised Node service on a K8s cluster.
  difficulty_level: L2
  estimated_minutes: 35
  locale: en
  authors: [ashwin@..., content-team]
  tags: [kubernetes, deployments, containers]

curriculum:
  primary_topic: topic.devops.k8s.deployments
  also_relevant: [topic.cloud.container-orchestration]
  courses: [course.devops-with-ai, course.sre]

skills:
  - {skill: k8s.deployments,   weight: 0.45, primary: true,  bloom: apply}
  - {skill: k8s.yaml,          weight: 0.20, primary: false, bloom: apply}
  - {skill: kubectl.cli,       weight: 0.20, primary: false, bloom: apply}
  - {skill: k8s.services,      weight: 0.15, primary: false, bloom: understand}

prerequisites:
  hard: [docker.basics, linux.cli]
  soft: [k8s.core]

objectives:
  - Build a container image from an application repo
  - Author a Deployment manifest with correct probes and resources
  - Expose the workload and verify external reachability

environment:
  tier: SHARED_CONTAINER        # BROWSER | SHARED_CONTAINER | ISOLATED_VM | CLOUD_ACCOUNT
  blueprint: bp.k8s-single-node.v4   # references an EnvironmentBlueprint
  resources: {cpu: "2", memory: 4Gi, disk: 10Gi}
  ttl_minutes: 90
  idle_timeout_minutes: 15
  network_policy: egress-allowlist-registry
  seed:
    - fixture: fx.node-app-repo.v2     # clones a starter repo into /workspace
    - fixture: fx.k3s-ready.v4         # cluster up, namespace created
  cost_budget_usd: 0.08

surfaces: [terminal, editor, k8s_dashboard_readonly]

tasks:
  - key: t1
    title: Build the container image
    required: true
    instructions_md: |
      The application source is in /workspace/app. Build an image tagged
      `node-app:v1`. Keep the final image under 300 MB.
    validators:
      - id: v.image-exists
        type: SHELL_ASSERT
        run: "docker image inspect node-app:v1"
        expect: {exit_code: 0}
        weight: 0.6
        on_fail: "No image tagged node-app:v1 was found. `docker images` lists what you have built."
      - id: v.image-size
        type: SHELL_JSON
        run: "docker image inspect node-app:v1 --format '{{.Size}}'"
        expect: {jsonpath: "$", op: "lt", value: 314572800}
        weight: 0.4
        severity: WARN     # WARN → scores but does not block completion
    hints:
      - level: 1, penalty: 0.02, text: "Docker needs a Dockerfile and a build context."
      - level: 2, penalty: 0.05, text: "The -t flag tags an image at build time."
      - level: 3, penalty: 0.12, text: "Run `docker build -t node-app:v1 .` from /workspace/app."
    solution_apply: scripts/t1_apply.sh   # forces post-state if learner skips
    telemetry_signals: [command_count, build_failures, time_to_first_success]

  - key: t2
    title: Push the image to the cluster registry
    ...

completion:
  rule: ALL_REQUIRED_TASKS_PASS
  min_score: 0.70

scoring:
  profile: sp.guided-lab.default   # see §6.4
  overrides:
    weights: {technical_correctness: 0.70, task_completion: 0.20, efficiency: 0.10}

ai_mentor:
  persona: tutor
  max_hints: 8
  token_budget: 12000
  solution_visibility: HIDDEN    # HIDDEN | AFTER_SUBMIT | ALWAYS
  policy: policy.lab.socratic.v3

reference_solution:
  repo_path: solutions/lab.k8s.deploy-node-app/
  visibility: AFTER_PASS_OR_EXHAUST

lifecycle:
  resume_window_days: 7
  max_attempts: null    # unlimited, cooldown-governed
  retire_after: null
```

Design notes:

- **Validators are declarative and typed**, not arbitrary scripts where possible. A small validator type catalogue (§6.2) means the platform can reason about them: estimate runtime, run them in parallel, cache them, and render consistent failure UI.
- **`on_fail` messages are authored content.** Generic "task failed" is the number one cause of lab abandonment. Make authored failure feedback a required field in CI lint.
- **`severity: WARN`** lets you score good practice (image size, resource limits, tagging) without blocking progress. Use it heavily; it is how you teach "production thinking" without frustration.
- **`solution_apply`** enables skip-forward, powers automated content testing, and is the reference implementation — one artifact, three uses.

## 3.3 Production Simulation additions

```yaml
mode: PRODUCTION_SIM
scenario:
  ticket_md: |
    **INC-4471 — P2** Checkout service intermittently 502s in prod-eu.
    Error budget burn: 4× normal. On-call escalated to you at 02:14.
  business_impact: "~8% of checkout attempts failing; revenue at risk."
  granted_access: [kubectl-readwrite-ns-checkout, logs-read, metrics-read]
  runbook_excerpt_md: "...deliberately incomplete..."

baseline:
  blueprint: bp.microservices-eks.v3
  health_gate:                      # MUST pass before faults are applied
    - {type: HTTP_PROBE, url: "http://checkout/healthz", expect_status: 200, retries: 30}

faults:
  - id: f.k8s.readiness-probe-too-aggressive
    params: {service: checkout, timeout_seconds: 1}
    apply_at: T0
  - id: f.k8s.memory-limit-too-low
    params: {service: checkout, limit: 96Mi}
    apply_at: T0
  - id: f.load.traffic-spike
    params: {rps: 400, duration_s: 180}
    apply_at: T+900              # escalation

resolution:
  validators:
    - {type: HTTP_SLO, url: "http://checkout/api/cart", success_rate_min: 0.995, window_s: 180}
    - {type: K8S_ASSERT, resource: deploy/checkout, jsonpath: "$.status.readyReplicas", op: gte, value: 3}
    - {type: NO_REGRESSION, snapshot: baseline.other-services}  # didn't fix by breaking something else
  artifacts_required:
    - {key: incident_note, type: MARKDOWN, rubric: rub.incident-note.v2}

process_signals:
  diagnostic_efficiency:
    good_actions: ["kubectl describe pod", "kubectl logs", "kubectl get events", "kubectl top"]
    bad_actions: ["kubectl delete pod --all", "kubectl rollout restart"]  # before diagnosis
    scoring: ratio_and_ordering
  blast_radius:
    forbidden: ["kubectl delete ns", "terraform destroy"]
```

The `NO_REGRESSION` validator and `blast_radius` signals are what make a simulation genuinely production-like. A learner who fixes the 502 by deleting the namespace and redeploying from scratch technically restores service and should not score well.

## 3.4 Fixtures, blueprints and the fault library

Three reusable content primitives, versioned independently of activities:

| Primitive | What it is | Example | Reuse factor |
|---|---|---|---|
| **EnvironmentBlueprint** | Declarative env definition: base images, node shape, pre-installed tooling, network policy, provisioning steps, warm-pool eligibility | `bp.k8s-single-node.v4`, `bp.microservices-eks.v3`, `bp.aws-sandbox-vpc.v2` | 1 blueprint : 30+ activities |
| **Fixture** | Seed state applied to a blueprint: repos, data, manifests, IAM baseline | `fx.node-app-repo.v2`, `fx.retail-db-1m-rows.v1` | 1 : 10 |
| **Fault** | Parameterised, idempotent, reversible breakage with a canonical diagnostic path | `f.k8s.readiness-probe-too-aggressive`, `f.iam.missing-ecr-pull`, `f.tf.state-lock-orphan` | 1 : 15 |

Each Fault carries metadata the platform uses:

```yaml
id: f.iam.missing-ecr-pull
targets: [aws.iam, k8s.imagepull]
symptom_class: ErrImagePull
skills_probed: [aws.iam.policies, k8s.pod.lifecycle, container.registry]
canonical_diagnostic_path:
  - "kubectl describe pod → see ErrImagePull / 403"
  - "check node/pod IAM role → aws sts get-caller-identity"
  - "check ECR repository policy / IRSA annotation"
expected_fix_class: [attach_policy, add_irsa_annotation, add_imagepullsecret]
detectability: medium
mean_time_to_diagnose_minutes: 12
```

`canonical_diagnostic_path` is used for the process-efficiency signal and to constrain the AI mentor's hints. `skills_probed` lets the recommendation engine target a *fault*, not just an activity — Phase 4+ can compose fresh simulations by selecting faults against weak skills.

**This is the composability payoff:** blueprint × fixture × fault-set = simulation. A library of 15 blueprints, 60 fixtures and 120 faults generates thousands of viable scenarios. Authoring economics change completely.

## 3.5 Content quality: automated testing (non-negotiable)

Every activity version must pass CI before it can be published:

1. **LINT** — schema valid, all required fields, every validator has `on_fail` text, every task has ≥1 validator, skills exist, prerequisites are a DAG
2. **PROVISION** — blueprint + fixtures provision successfully, 3 consecutive runs
3. **GOLDEN PATH** — run all `solution_apply` scripts in order → ALL validators must PASS
4. **NULL PATH** — provision, apply nothing → ALL required validators must FAIL (catches validators that pass trivially — the #1 content bug)
5. **FLAKE** — run steps 3–4 five times; any inconsistency fails the build
6. **TIMING** — record provision time, validation time; fail if > blueprint SLO
7. **COST** — estimate and record cost per attempt; fail if > declared budget × 1.2
8. **AI DRY-RUN** — run the mentor policy against the spec; assert no solution leakage (adversarial prompts from a fixed suite)

Steps 3 and 4 together are the core insight: a validator that passes on the solution *and* fails on the empty environment is doing real work. Most broken labs in the wild fail step 4.

**Trade-off:** this CI is expensive — it provisions real environments. Budget for it: a dedicated content-CI cluster, nightly full-suite runs, per-PR runs limited to changed activities. Expect content CI to be 5–15% of your total infra spend. It is worth every cent; a flaky lab destroys learner trust far faster than a missing one.

## 3.6 Versioning architecture (D3 + D4)

**Requirement from the brief:** existing attempts stay bound to the version they ran; admins can publish new versions; validation rules change safely; old attempts stay reproducible.

**Model:**

```
Activity (stable identity, slug, owner, retirement state)
  └── ActivityVersion (IMMUTABLE once PUBLISHED)
        ├─ spec_json               full frozen spec
        ├─ blueprint_version       pinned
        ├─ fixture_versions        pinned
        ├─ fault_versions          pinned
        ├─ scoring_profile_version pinned
        └─ rubric_versions         pinned

Attempt.activity_version_id → FK, never updated
```

**Rules:**

11. **Publishing creates a new version. Editing a published version is impossible** (enforced at DB level: `ALTER TABLE activity_version` with an update trigger rejecting changes when `status='PUBLISHED'`). Authors edit a `DRAFT` clone.
12. **Version semantics:**
    - `PATCH` — typo, clearer hint, better `on_fail` text. Safe to hot-apply to *in-flight* attempts (text only, never validators).
    - `MINOR` — new optional task, new hint level, relaxed WARN threshold. In-flight attempts continue on the old version; new attempts get the new one.
    - `MAJOR` — validator logic, required tasks, blueprint, scoring weights. Old version is frozen and remains provisionable.
13. **Old versions must remain runnable.** This means blueprints and images must be retained. Policy: retain provisionable artifacts for any version with an active or suspended attempt, plus 90 days after the last attempt; then mark `ARCHIVED_UNPROVISIONABLE` (attempt records survive; the environment cannot be rebuilt). Communicate this — "attempts older than X cannot be re-opened" is acceptable; silently failing to restore is not.
14. **Re-scoring:** because scores are derived from stored signals (D3), publishing a corrected rubric can trigger a **backfill re-score job** over affected attempts. Store both `score_original` and `score_current` with `rescored_at` and `rescore_reason`. Never silently mutate a learner's historical score — notify.
15. **Rollout:** new MAJOR versions go through canary — 5% of new attempts for 48h, monitored on pass-rate, median time, hint usage, abandonment. Auto-rollback on a >20% relative pass-rate drop.

**Trade-offs:** strict immutability plus artifact retention costs storage and operational discipline. The alternative (mutable content) makes every score unexplainable and every appeal unanswerable, and quietly corrupts your Elo calibration and mastery model. Not a real choice.

## 3.7 Admin / CMS architecture

Two authoring surfaces over one schema:

| Surface | Users | Capabilities |
|---|---|---|
| **Git + CLI** (`practice-cli validate/test/publish`) | Platform content engineers | Full power, bulk ops, blueprint & fault authoring, CI integration |
| **Web CMS** | Instructors, SMEs, curriculum designers | Guided forms, task builder, validator picker (typed catalogue only, no raw shell for non-engineers), hint ladder editor, rubric editor, preview & test-run, publish request |

The Web CMS **writes the same YAML** and opens a merge request. Both paths converge on CI. This avoids the classic split where the UI-authored content is second-class and untested.

CMS must include:

- **Live preview with a real environment** ("test this lab as a learner") — authors will not catch a broken validator by reading YAML.
- **Validator debugger**: run a single validator against the current env, see raw output, exit code, and the assertion evaluation.
- **Skill picker driven by the graph**, with a warning when the chosen skills have no prerequisite path from the learner's expected position.
- **Rubric editor** with a calibration mode: paste 3 sample submissions, see how the AI grader scores them, adjust the rubric. Rubrics that are not calibrated produce garbage grades.
- **Publish workflow**: Draft → In Review → Approved → Published → Deprecated → Retired, with a required reviewer distinct from the author.

---

# Part IV — Activity Lifecycle and Attempt Tracking

## 4.1 Two state machines, deliberately separated

The brief lists one lifecycle. In practice you need two, because content state and learner state have different owners, different transitions, and different failure modes.

**A. Content lifecycle (ActivityVersion):**

```
DRAFT ──► IN_REVIEW ──► APPROVED ──► PUBLISHED ──► DEPRECATED ──► RETIRED
  ▲            │                        │              │
  └────────────┘                        │              │  (no new attempts;
   (changes requested)                  │              │   existing continue)
                                         ▼              ▼
                                    CANARY (5%) ──► ROLLED_BACK
```

**B. Attempt lifecycle:**

```
                                          ┌──────────────────────────────────────────────┐
                                          │                                                │
CREATED ─► PROVISIONING ─► READY ─► IN_PROGRESS ─┬─► SUBMITTED ─► EVALUATING ─┬─► PASSED ──► COMPLETED
   │             │                       │        │                          │      │
   │             │                       │        └─► (auto-submit on TTL)   ├─► FAILED ──► (cooldown) ──► RETRY (new attempt)
   │             ▼                       ▼                 │                 │      │
   │      PROVISION_FAILED    SUSPENDED (env destroyed, workspace saved)      └─► EVAL_FAILED ──► (requeue / manual)
   │             │                       │                                          │
   │             └──► (auto-retry ×2) ───┘
   ▼
EXPIRED (never started within window)          ABANDONED (suspended past resume_window)
```

Explicit answers to the brief's questions:

**What happens when a learner starts an activity?**

16. `POST /v1/attempts` with `activity_id` + idempotency key.
17. Eligibility service checks: enrolment, prerequisite closure, retry cooldown, concurrent-environment quota, daily cost budget, tenant budget, activity published state.
18. Attempt row created (`CREATED`), `attempt_events` gets `ATTEMPT_CREATED`.
19. Provisioning request enqueued to the Environment Orchestrator with the pinned blueprint/fixture versions.
20. Orchestrator attempts a **warm-pool claim**; on miss, cold-provisions.
21. On `READY`, a scoped session token + connection endpoints (WS terminal URL, editor URL) are pushed over the learner's WebSocket.
22. Health gate runs (blueprint self-check). For sims, faults are applied only after the health gate passes.
23. Attempt → `IN_PROGRESS` on first learner interaction (not on READY — this keeps time-on-task honest).

**How is the environment provisioned?** See Part V.

**How is the learner's work stored?**

- Live: inside the environment's writable volume.
- Continuously: a `/workspace` git repo with an auto-commit hook every 60s of change (cheap, gives file-level history for free, and makes diffing for evaluation trivial).
- Periodically (every 5 min) and on suspend: tar of `/workspace` + declared stateful paths (e.g. `.terraform/`, DB dump) → object storage under `attempts/{id}/snapshots/{ts}.tar.zst`.
- Cloud-tier attempts additionally snapshot **infrastructure state**: `terraform state pull`, `kubectl get -A -o yaml` filtered, and a resource inventory from the cloud API. This is what makes a cloud attempt reproducible and auditable after the account is nuked.

**How is the environment reset?** Three levels:

- `RESET_TASK` — run the task's `solution_revert`/fixture re-apply for a single task.
- `RESET_ALL` — destroy and re-provision from blueprint+fixture, discarding the workspace (with a confirm dialog and a snapshot taken first).
- `RESET_KEEP_FILES` — re-provision and restore the latest `/workspace` snapshot. This is the one learners actually want after they wreck the cluster.

Resets are counted in `attempt.reset_count` and are a scoring signal (mild) and a content-quality signal (strong — labs with high reset rates have bad instructions).

**How is validation performed?** See Part VI. Critically: out-of-band, from a validator runner that the learner cannot reach or influence.

**How is score calculated?** See §6.4.

**How are retries handled?** A retry is a **new** Attempt row with `retry_of_attempt_id` and incremented `retry_index`, on the same activity version unless the learner explicitly opts into the latest. Cooldown per §2.7. Score history retains all attempts; `best_score` and `latest_score` are both materialised on `learner_activity_state`. Mastery updates use *all* attempts (a pass after three failures is genuinely weaker evidence than a first-try pass — the BKT evidence-strength term handles this).

**What happens when the learner leaves midway?** §1.5 abandonment path.

**How is progress persisted?** Three layers:

24. `attempt_events` (append-only, source of truth).
25. `attempt_task_state` (materialised per-task status, updated by validator results) — this is what the UI reads.
26. Workspace snapshots in object storage.

Rebuilding layer 2 from layer 1 must always be possible; write a replay tool in week one and test it in CI. It is your recovery mechanism for every data bug you will have.

## 4.2 Attempt event model (D3)

Append-only, partitioned by month, one table:

```
attempt_events(
  id           bigserial,
  attempt_id   uuid not null,
  seq          bigint not null,       -- monotonic per attempt
  occurred_at  timestamptz not null,
  actor        enum(LEARNER, SYSTEM, VALIDATOR, AI, ADMIN),
  type         text not null,
  payload      jsonb not null,
  PRIMARY KEY (attempt_id, seq)
) PARTITION BY RANGE (occurred_at);
```

Event taxonomy (non-exhaustive, but this is the vocabulary the whole platform speaks):

| Category | Types |
|---|---|
| Lifecycle | `ATTEMPT_CREATED`, `ENV_REQUESTED`, `ENV_READY`, `ENV_FAILED`, `ATTEMPT_STARTED`, `SUSPENDED`, `RESUMED`, `SUBMITTED`, `EVALUATED`, `SEALED` |
| Execution | `COMMAND_EXECUTED` {cmd, cwd, exit_code, duration_ms, stdout_hash}, `FILE_CHANGED` {path, op, diff_ref}, `EDITOR_SAVE`, `TERMINAL_SESSION_OPEN/CLOSE` |
| Validation | `VALIDATION_REQUESTED`, `VALIDATOR_RESULT` {validator_id, pass, detail}, `TASK_PASSED`, `TASK_FAILED`, `TASK_SKIPPED` |
| Assistance | `HINT_REQUESTED` {task, level}, `AI_MESSAGE` {role, tokens, policy_decision}, `SOLUTION_VIEWED` |
| Environment | `RESET`, `FAULT_INJECTED` {fault_id}, `IDLE_DETECTED`, `TTL_WARNING`, `ENV_DESTROYED`, `SNAPSHOT_TAKEN` |
| Scenario | `TICKET_OPENED`, `ESCALATION_FIRED`, `MILESTONE_SUBMITTED` |

**Command capture must happen server-side.** Never trust the browser to report commands. Capture at the terminal multiplexer (the PTY proxy sits between the WebSocket and the container exec), plus a shell audit hook (`PROMPT_COMMAND` / `auditd` / eBPF `execve` tracing) inside the environment for commands issued outside the web terminal (scripts, SSH). Reconcile both; discrepancies are an integrity signal.

## 4.3 What to store permanently vs. what to retire

| Data | Retention | Rationale |
|---|---|---|
| Attempt record, status, scores, per-criterion feedback | **Permanent** (life of account + legal) | Certification, transcript, appeals |
| Skill mastery history (evidence-level) | **Permanent**, downsampled after 2y | The learner record's core value |
| `attempt_events` — lifecycle, validation, hints, scoring | **Permanent** (moved to cold storage after 12m) | Auditability, re-scoring, analytics |
| `attempt_events` — `COMMAND_EXECUTED` full text | **13 months** hot, then hashed-only | Volume; and full command text is PII-adjacent |
| Terminal stdout/stderr streams | **30 days** (90 for projects) | Enormous volume; only needed for dispute windows and debugging |
| Terminal session recordings (asciinema-style) | **30 days**, or permanent if flagged for review/dispute | Great for support, expensive at scale |
| Workspace snapshots (intermediate) | **7 days**, keep only latest + submission | Storage |
| Workspace snapshot at submission | **24 months** (projects: permanent) | Evidence behind the score; portfolio value |
| AI mentor conversations | **13 months**; PII-redacted at write | Guardrail auditing, quality improvement |
| Cloud resource inventory at submission | **24 months** | Proof of what was built after nuke |
| Environment container/VM logs | **14 days** | Ops debugging only |
| Cost/metering records | **Permanent**, aggregated after 13m | Finance |
| Raw telemetry in the analytics store | **25 months** rolling | Cohort analysis over two academic years |

Learner-facing controls: **export** (all attempts, snapshots, feedback as an archive) and **deletion** (anonymise the attempt, purge workspace and transcripts, retain aggregate counters). Build these in Phase 1 — retrofitting deletion across an event-sourced system with object-storage snapshots is genuinely painful.

## 4.4 Concurrency and integrity rules

- **One active environment per learner by default** (configurable per tier; projects may allow 1 project + 1 lab). Prevents cost abuse and mirrors reality.
- **Attempt operations are idempotent** — every mutating endpoint takes an `Idempotency-Key`; duplicate submits are no-ops.
- **Optimistic concurrency on attempt state** via `version` column; the orchestrator and the API both write, so conflicts are real.
- **Validation is serialised per attempt** with a Redis lock; parallel "Check" clicks must not race the environment.
- **Integrity signals** (not enforcement, but recorded and reported to admins): impossible timing (validator passes 4s after env ready), identical workspace hashes across learners, command sequences copy-pasted verbatim from a known solution, submission from many IPs. Surface as a review queue; never auto-penalise on heuristics alone.

---

# Part V — Execution Environment Architecture

This is the hardest, most expensive, and most differentiating part of the platform.

## 5.1 Decision: four tiers, chosen by capability requirement (D1)

**What:** Four execution tiers. Activity blueprints declare the *minimum* tier that satisfies their capability requirements; the orchestrator provisions the cheapest available tier that qualifies.

**Why:** Capability requirements vary by two orders of magnitude in cost. Running a Python pandas exercise on a real EC2 instance is ~500× more expensive than running it in the browser, for identical learning value. A single-tier design forces you to either over-provision (bankrupt) or under-deliver (no real cloud work). Tiering is what makes "thousands of learners" arithmetically possible.

**The tiers:**

| Tier | Name | Runtime | Supports | Cost/hr (est.) | Start latency | Isolation |
|---|---|---|---|---|---|---|
| **T0** | Browser | WASM in the learner's tab | Python (Pyodide), SQL (sql.js/DuckDB-WASM), JS/TS, pandas, small ML inference, YAML/JSON linting, git basics | **$0.00** | <2s | Browser sandbox (learner's own machine) |
| **T1** | Shared container | Pod on shared multi-tenant K8s, gVisor-sandboxed | Linux CLI, Python/SQL at scale, Node/Java builds, Git, Ansible, plain Docker (rootless), Postgres/Redis sidecars, LocalStack-backed AWS basics, Terraform against LocalStack/Docker provider | **$0.02–0.06** | 2–5s (warm pool) | gVisor + NetworkPolicy + namespace + quota |
| **T2** | Isolated microVM | Firecracker/Kata microVM or dedicated node-per-tenant | Docker-in-Docker with real kernel features, k3s/kind full Kubernetes, systemd, eBPF, kernel tuning, multi-node K8s (nested), heavy Terraform, CI runners | **$0.10–0.35** | 8–20s (warm pool) / 60–90s cold | Hardware-virtualised; own kernel |
| **T3** | Cloud sandbox | Vended AWS account / Azure subscription / GCP project | Real EKS/AKS/GKE, real IAM, real VPC, ALB/NLB, RDS, S3, CloudWatch, real Terraform against real APIs, multi-service architectures, cost-visible work | **$0.40–4.00** | 30s (pooled account) / 5–15 min cold | Account/subscription boundary + SCP/Policy |

**Tier selection rule** (in the blueprint, evaluated by the orchestrator):

```
required_capabilities: [kernel.namespaces, docker.privileged, k8s.multi_node]
     │
     ├─ T0 offers: [wasm.python, wasm.sql, fs.virtual]                       ✗
     ├─ T1 offers: [linux.full, docker.rootless, k8s.none, net.egress]       ✗
     ├─ T2 offers: [linux.full, docker.privileged, k8s.k3s, kernel.tune]     ✓ ← selected
     └─ T3 offers: everything + cloud.aws.*                                 (overkill)
```

**Trade-offs:**

- Four tiers means four provisioning code paths, four teardown paths, four monitoring surfaces. Real complexity.
- Mitigation: a **single Environment Orchestrator interface** (`Provision / Connect / Snapshot / Restore / Validate / Destroy`) with four driver implementations. The rest of the platform only ever sees the interface. Build T1 first (Phase 1), T2 in Phase 2, T3 in Phase 3/5, T0 opportunistically (it is cheap and delightful — it makes "practice on your commute" possible).

## 5.2 Tier 1 in detail — the workhorse

Expect 70–80% of all attempts to run on T1. Optimise it hard.

```
┌─────────────────── Practice Cluster (regional, EKS/GKE) ─────────────────────┐
│                                                                                │
│  Node group: practice-t1 (spot, ARM64 where possible, 16–64 vCPU nodes)      │
│  RuntimeClass: gvisor    Taints: workload=learner:NoSchedule                  │
│                                                                                │
│  Namespace: env-{env_id}   (one namespace per environment)                    │
│   ├─ ResourceQuota     cpu 2, mem 4Gi, pods 6, pvc 1, services 2              │
│   ├─ LimitRange        default requests/limits, no unbounded containers       │
│   ├─ NetworkPolicy     default-deny ingress+egress; allow → egress-proxy only │
│   ├─ ServiceAccount    automountServiceAccountToken: false                    │
│   ├─ PodSecurity       restricted; no privileged, no hostPath, no hostNetwork,│
│   │                    runAsNonRoot, seccomp RuntimeDefault, drop ALL caps    │
│   │                                                                            │
│   ├─ Pod: workspace                                                           │
│   │    ├─ container: shell   (base image + activity toolchain layer)          │
│   │    ├─ container: editor  (OpenVSCode Server, optional surface)            │
│   │    └─ volume: /workspace (ephemeral PVC or emptyDir + snapshot sidecar)   │
│   │                                                                            │
│   └─ Pod(s): services   (postgres, redis, localstack — per fixture)           │
│                                                                                │
│  ┌─ Shared infra (platform namespace, NOT learner-reachable) ─────────────┐  │
│  │  session-broker (PTY proxy)      validator-runner (job executor)       │  │
│  │  snapshot-agent                  egress-proxy (allowlist)              │  │
│  │  registry-mirror / pull-through cache                                  │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

**Why namespace-per-environment rather than pod-per-environment:** you get ResourceQuota, NetworkPolicy and RBAC as native boundaries, multi-pod fixtures (app + db + cache) become trivial, and teardown is a single `delete namespace`. The cost is namespace churn — thousands of create/delete per hour stresses etcd. Mitigations: keep namespaces lean (no per-namespace CRDs), cap namespace lifetime, shard across multiple clusters at ~2,000 concurrent environments per cluster, and monitor etcd object count and API-server latency as first-class SLIs.

**Why gVisor:** learner-controlled code plus a shared kernel is the platform's largest security risk. gVisor's user-space kernel removes most of the container-escape class at ~5–15% syscall-heavy performance cost. For T1 workloads (editing, building, scripting) that cost is invisible. Where it isn't acceptable (Docker-in-Docker, kernel features), that workload belongs in T2 anyway — the tier boundary and the isolation boundary coincide, which is a satisfying property.

Alternatives considered: Kata Containers (stronger, heavier, slower start — chosen for T2 instead); plain runc with strict seccomp/AppArmor (fastest, but one kernel CVE away from a cross-tenant incident — unacceptable for a public platform).

**Image strategy:** a small set of base images (`ubuntu-tools`, `python-ds`, `node`, `jvm`, `cloud-cli`) plus activity-specific layers pulled from a regional pull-through cache. Pre-pull the top 20 images onto every node via a DaemonSet. Image pull is the single biggest contributor to cold-start p95; treat it as a performance budget item.

## 5.3 Tier 3 in detail — real cloud sandboxes (D9)

**What:** Real cloud work happens in vended, disposable accounts, never in shared accounts with IAM users.

**AWS pattern:**

```
Organization
├── OU: Platform            (control plane, billing, tooling)
├── OU: ContentCI           (accounts used by content CI)
└── OU: LearnerSandboxes    ← SCP-governed
     ├── acct-pool-0001 ─┐
     ├── acct-pool-0002 ├── Pool: AVAILABLE / IN_USE / NUKING / QUARANTINED
     ├── ...             │
     └── acct-pool-0400 ─┘
```

- **Account Pool Manager** (part of the Environment Orchestrator): maintains N warm, clean accounts per region. Claim → assign to attempt → set budget alarm → provision baseline via Terraform → hand out credentials → on TTL/idle/submit, revoke → nuke → verify empty → return to pool. An account that fails verification goes to `QUARANTINED` for human review, never straight back to the pool.
- **Credential brokering:** the learner never receives long-lived keys. The environment gets a **short-lived STS session (max 1h, auto-refreshed by a sidecar broker)** for a role in the sandbox account, obtained via OIDC federation from the platform's identity provider using the attempt ID as the subject. Refresh stops the instant the attempt leaves `IN_PROGRESS`.
- **Service Control Policies** (the real security boundary — IAM inside the account can be broken by the learner, SCPs cannot):
  - Deny all regions except the two allowed.
  - Deny expensive service/instance classes (GPU families, `*.24xlarge`, Redshift, EMR on-demand large, Bedrock provisioned throughput) unless the blueprint grants an exception.
  - Deny `organizations:*`, `account:*`, leaving the org, modifying CloudTrail/Config/GuardDuty, deleting the nuke role.
  - Deny creating IAM users with console access, creating access keys with no expiry, and `iam:CreateOpenIDConnectProvider` outside the allowed pattern.
  - Deny public S3 / public AMI sharing / public snapshot sharing (data exfiltration and abuse vector).
  - Deny SES, SNS SMS, and outbound mail primitives (spam abuse).
- **Nuke:** `aws-nuke`/`cloud-nuke` run from the platform account assuming a dedicated `PlatformNukeRole` (which SCP prevents the learner from deleting). Nuke runs on TTL, on submit, on idle-timeout, and nightly as a sweeper regardless of state. **Verification step is mandatory**: after nuke, enumerate resources via Config/Resource Explorer; non-empty → quarantine + page.
- **Cost guards:** per-account AWS Budgets at 50/80/100% of the activity's declared budget → EventBridge → orchestrator; at 100% the orchestrator revokes credentials and force-terminates. Plus an independent hourly Cost Explorer/CUR poll, because Budgets alone lag by hours.

**Azure:** subscription-per-learner under a Management Group with Azure Policy (equivalent of SCP), Entra Workload Identity Federation for credentials, `az group delete` sweep plus a subscription-level cleanup runbook. **GCP:** project-per-learner under a folder with Org Policy constraints, Workload Identity Federation, project deletion (with lien removal) as the nuke — GCP project deletion is the cleanest nuke of the three.

**Trade-offs:**

- Account pools have hard quota limits (AWS default ~10 accounts; raised to thousands via support). Account creation takes minutes, so pools must be pre-warmed and capacity-planned days ahead of cohort starts. This is a genuine operational commitment.
- Alternative considered — **shared account with per-learner IAM boundaries + tag-based isolation**: much cheaper and faster, but IAM permission-boundary design for arbitrary learner actions is extremely hard to get right, one mistake is cross-tenant, and it teaches learners a fake IAM model. Use it only for read-only "explore the console" activities, never for build activities.
- Alternative — **LocalStack / cloud emulators (T1)**: excellent for teaching API mechanics at near-zero cost and no credential risk; genuinely inadequate for IAM semantics, networking, quotas, eventual consistency and real failure modes. Use emulators for L1–L2 cloud labs, real accounts for L3+.

## 5.4 Connectivity architecture

```
Browser
  ├── xterm.js ───► WSS /v1/attempts/{id}/terminal?session=… ─┐
  ├── Monaco / iframe(OpenVSCode) ───► WSS /editor ────────────┤
  └── UI state ───► WSS /v1/attempts/{id}/stream ──────────────┤ (events, validation results,
                                                                 │  TTL warnings, mentor stream)
                                                                 ▼
                                          ┌────────────────────────────────┐
                                          │  WS Gateway (stateless, HPA)    │
                                          │  - JWT/session auth per socket  │
                                          │  - attempt-scoped authorization │
                                          │  - rate + backpressure limits   │
                                          └───────────┬────────────────────┘
                                                       ▼
                                          ┌────────────────────────────────┐
                                          │  Session Broker (stateful)      │
                                          │  - maps session → env endpoint  │
                                          │  - PTY multiplexing             │
                                          │  - TELEMETRY TAP ◄── command    │
                                          │    capture happens HERE         │
                                          │  - record asciicast to S3       │
                                          └───────────┬────────────────────┘
                                                       ▼
                                    K8s API `exec` / SSH to microVM / SSM to EC2
                                                       ▼
                                          Learner environment
```

Decisions:

- **WebSocket, not SSH from the browser.** SSH-in-browser means exposing SSH endpoints, managing keys, and losing the telemetry tap. Terminate at the platform, proxy inward over the control plane's own channel (K8s exec API for T1/T2, SSM/SSH-over-bastion for T3 EC2). Learners never get a routable address into their environment.
- **The telemetry tap lives in the Session Broker**, not in the client and not in the container. Client-side capture is trivially bypassed; container-side capture can be killed by the learner. Broker-side capture is authoritative. Supplement with in-container `auditd`/eBPF for out-of-band commands, treating divergence as a signal, not as the primary source.
- **Reconnection:** sessions survive network blips. Broker keeps the PTY alive for 120s after socket loss and replays a scrollback buffer on reconnect. Without this, every train tunnel destroys an attempt.
- **Editor:** Monaco (client-side, talking to a file API) for Phase 1 — simple, cheap, fast. **OpenVSCode Server inside the environment** for Phase 3+ — real extensions, language servers, integrated terminal, debugging; costs ~500MB RAM per environment, so gate it by tier and activity.
- **Exposing learner-run web apps:** each environment gets a wildcard subdomain (`{env_id}-{port}.preview.platform.io`) routed via the ingress to the environment's Service, authenticated by the learner's session cookie and scoped to that attempt. Never expose learner apps unauthenticated to the internet — you would be running a free malware/phishing host.

## 5.5 Provisioning pipeline and warm pools

```
Request(blueprint@v, fixtures[], tier, resources)
     │
     ├─► 1. POOL MATCH   key = hash(blueprint_version, resource_class, region)
     │       hit ──► claim (atomic CAS in Redis) ──────────────────┐
     │       miss ─► 2. COLD PROVISION                             │
     │                   T1: create ns → quota/netpol → create pods│
     │                   T2: request microVM from node pool        │
     │                   T3: claim account → terraform apply baseline
     │                                                              │
     ├─► 3. FIXTURE APPLY  (idempotent, ordered, checksummed) ◄────┘
     ├─► 4. HEALTH GATE    blueprint self-check; fail ⇒ discard & retry (max 2)
     ├─► 5. FAULT INJECT   (sims only, after gate passes)
     ├─► 6. REGISTER       endpoints, TTL, idle watcher, cost meter, audit sink
     └─► 7. READY          push over WS
```

**Warm pool policy:**

- Pool size per key = `predicted_demand(next 10 min) × safety_factor − currently_available`, floored at a per-key minimum during business hours, zero overnight per region.
- Demand prediction: simple and effective — last-4-weeks same-weekday-same-hour rate, plus enrolled-cohort schedule (you know when a live class covering Kubernetes runs; pre-warm 200 environments 15 minutes before).
- Warm environments are **fully provisioned including fixtures** but have no learner identity; claiming is an atomic Redis CAS plus a K8s label patch.
- Warm environments have their own shorter TTL (30 min) and are recycled — a stale warm pod is a correctness risk (drifted state, expired tokens).
- Target: **p50 time-to-ready ≤ 3s, p95 ≤ 20s for T1/T2; p95 ≤ 60s for T3 (pooled)**. Publish these as SLOs; they correlate directly with completion rate.

**Trade-off:** warm pools cost money for idle capacity. Model it: if a warm T1 pod costs $0.04/hr and pool utilisation is 60%, you burn $0.016 per delivered environment-hour on idle. That is cheap relative to a 45-second cold start dropping lab completion by several percent. Re-evaluate with real data; make pool floors a tunable config, not code.

## 5.6 Idle detection, TTL, and teardown

Three independent clocks per environment; whichever fires first wins:

| Clock | Trigger | Action |
|---|---|---|
| **Idle** | No stdin, no file write, no validation, no mentor message for `idle_timeout` (default 15 min) AND CPU < 5% for 5 min | Warn learner over WS at T−3min; then snapshot → suspend attempt → destroy environment |
| **TTL** | Wall-clock since READY exceeds `ttl_minutes` | Warn at T−10 and T−2; auto-submit (labs/sims) or snapshot-and-suspend (projects); destroy |
| **Budget** | Metered spend exceeds activity budget × 1.5, or learner daily budget exhausted | Immediate credential revoke, snapshot, destroy, notify learner and admin |

The **two-signal idle check** matters: a learner running a long `terraform apply` has no stdin but high CPU. Killing them mid-apply is the worst possible experience and leaves orphaned cloud resources. Require both silence and low CPU, and add an explicit "keep alive, I'm reading" button plus long-running-operation detection (an active `terraform`/`kubectl wait`/`docker build` process suppresses the idle clock).

**Teardown must be reliable, not best-effort.** Every environment is registered in a `environment_reaper` table with a hard deadline. A reaper job runs every 60s and force-destroys anything past deadline regardless of what the orchestrator thinks. Orphan detection sweeps the cluster/cloud for resources tagged with unknown or completed attempt IDs, hourly. **Assume the orchestrator will crash mid-provision** — the reaper is the thing that keeps your cloud bill finite.

## 5.7 Multi-tenancy model

For B2B (enterprises buying cohorts), tenancy applies at four layers:

| Layer | Isolation |
|---|---|
| Data | `tenant_id` on every row; Postgres Row-Level Security; separate object-storage prefixes with distinct KMS keys per tenant |
| Compute | Shared cluster, dedicated node groups for tenants requiring it (a paid tier); namespace-level quotas per tenant to prevent noisy-neighbour resource exhaustion |
| Cloud | Separate account pool OU per enterprise tenant when contractually required; otherwise shared pool with per-tenant budget accounting |
| Network | Per-environment default-deny; per-tenant egress allowlists; no cross-namespace resolution (CoreDNS policy + NetworkPolicy) |

Quota is enforced at three levels — tenant, course-cohort, learner — each with concurrent-environment caps and daily spend caps. Exceeding a cap queues the request with a clear message rather than failing opaquely.

---

# Part VI — Validation and Scoring Architecture

## 6.1 Decision: deterministic-first, AI-advisory (D2)

**What:** Anything mechanically checkable is checked mechanically. AI grades only artifacts with no mechanical ground truth (design rationale, incident notes, code quality judgement, architecture trade-offs) and always against an explicit rubric, with confidence reporting and sampled human review.

**Why:**

- **Correctness.** "Is the Deployment running with 3 ready replicas?" has one true answer available from the API. Asking an LLM to infer it from a transcript introduces error where none needed to exist.
- **Consistency and fairness.** Two learners with identical end states must receive identical deterministic scores. LLMs are not reproducible enough to underwrite certification.
- **Cost and latency.** A validator job is milliseconds and fractions of a cent. Grading every task with an LLM at scale is both slow and expensive.
- **Defensibility.** When a learner appeals, "validator `v.deploy-ready` returned readyReplicas=1, expected ≥3, at 14:32:07" ends the conversation. "The AI felt your approach lacked rigour" does not.
- **Adversarial robustness.** Learners *will* try prompt injection in filenames, commit messages and README files to influence an AI grader. Deterministic validators are immune.

Where AI genuinely earns its place: open-ended reasoning quality, architecture appropriateness given constraints, clarity of documentation, whether a root-cause analysis is actually correct rather than merely plausible, and the defence viva. These have no deterministic oracle and are exactly where the highest-value learning lives.

## 6.2 Validator type catalogue

Typed, declarative, sandboxed. Adding a new type is a platform change reviewed by engineering; authors compose existing types.

| Type | Runs where | Checks | Example |
|---|---|---|---|
| `SHELL_ASSERT` | Validator runner → env exec | exit code, stdout regex, stderr absence | `docker image inspect node-app:v1` |
| `SHELL_JSON` | Validator runner → env exec | JSONPath + comparison op | image size < 300MB |
| `FILE_EXISTS` / `FILE_CONTENT` | Env filesystem read | path presence, content regex, checksum, absence of secrets | `Dockerfile` has non-root `USER` |
| `FILE_PARSE` | Validator runner | YAML/JSON/HCL parse + structural assertion | Deployment has `resources.limits.memory` |
| `K8S_ASSERT` | K8s API (read-only SA) | resource existence, JSONPath on live status | `readyReplicas >= 3` |
| `K8S_EVENT_ABSENT` | K8s API | no CrashLoopBackOff/OOMKilled in window | stability check |
| `HTTP_PROBE` | Validator runner (in-network) | status, latency, body match, TLS | `/healthz` returns 200 |
| `HTTP_SLO` | Load generator | success rate over N seconds under load | ≥99.5% over 180s |
| `CLOUD_ASSERT` | Cloud API (read-only role) | resource exists, config matches | ALB has HTTPS listener; S3 bucket not public |
| `IAC_STATE` | Terraform state read | resources in state, no drift, no local state file | `terraform plan` returns no changes |
| `DB_QUERY` | DB connection | query result matches expectation | table has 1M rows, index exists |
| `TEST_SUITE` | Sandboxed runner | pytest/jest/go-test on learner repo | unit tests pass, coverage ≥ 80% |
| `STATIC_ANALYSIS` | Sandboxed runner | linters, `tfsec`/`checkov`, `trivy`, `bandit`, `semgrep` | no critical CVEs, no hardcoded secrets |
| `PERF_BENCH` | Load runner | p95 latency, throughput | p95 < 300ms at 100 rps |
| `CHAOS_PROBE` | Chaos runner | survives pod kill / AZ loss | service stays up when a replica is killed |
| `TELEMETRY_ASSERT` | Event store query | process facts | `kubectl describe` was run before any restart |
| `NO_REGRESSION` | Snapshot diff | baseline services still healthy | didn't fix by breaking something else |
| `AI_RUBRIC` | AI grader | rubric-scored artifact | incident note quality |

Execution rules:

- Validators run in a **Validator Runner** — a separate pod/job that the learner's environment cannot see, reach, or influence. It holds read-only credentials (K8s read-only SA, cloud read-only role, DB read replica) scoped to that one environment, minted per run with a 5-minute lifetime.
- Validators that must run *inside* the environment (checking local files, running `docker inspect`) do so via a control-plane exec channel, running as a **different, non-learner user**, with output captured and never echoed to the learner's terminal.
- Every validator declares a `timeout` and `retry` policy. Infrastructure eventual-consistency is real; `K8S_ASSERT` on `readyReplicas` should retry with backoff for up to 60s before failing. **A flaky validator is worse than no validator.**
- Results are structured: `{validator_id, status: PASS|FAIL|ERROR|SKIP, observed, expected, duration_ms, evidence_ref}`. `ERROR` (validator itself broke) is never scored against the learner — it pages the on-call and is excluded from scoring, and the learner is told "we couldn't check this; not counted against you."

## 6.3 Validation flow

```
Learner clicks "Check" (or debounce auto-check, or Submit)
     │
     ▼
Practice API → enqueue ValidationRun{attempt_id, scope: task|all, trigger}
     │ (Redis lock per attempt — serialised)
     ▼
Validator Runner
     ├─ mint short-lived read-only credentials for this environment
     ├─ resolve validator DAG for scope (some depend on others; skip dependents on fail)
     ├─ execute in parallel with per-validator timeout
     ├─ emit VALIDATOR_RESULT events (streamed to UI over WS as they land)
     └─ revoke credentials
     │
     ▼
Task state recompute → attempt_task_state updated → TASK_PASSED/TASK_FAILED events
     │
     ├─ on partial fail → return authored `on_fail` feedback + observed vs expected
     └─ on all-required-pass → enable Submit
```

Auto-validation triggers on relevant activity (file save matching a watched glob, a command matching a watched pattern) with a 10s debounce and a rate cap. It creates the "it just turned green" feedback loop that makes guided labs feel alive — but cap it, because validation is not free.

## 6.4 Scoring engine

**Model:** Signals → Criteria → Profile → Score.

```
┌─ SIGNALS (raw, stored, immutable) ────────────────────────────────────────┐
│ validator results · task pass/fail · time-on-task · command count         │
│ hint usage by level · reset count · retry index · failed command ratio    │
│ diagnostic-path adherence · blast-radius violations · static-analysis     │
│ findings · test pass rate · perf numbers · AI rubric sub-scores           │
└───────────────────────────────┬───────────────────────────────────────────┘
                                 ▼
┌─ CRITERIA (normalised 0..1, each computed by a named, versioned function) ┐
│ technical_correctness · task_completion · efficiency · troubleshooting    │
│ reliability · security · performance · code_quality · architecture        │
│ production_readiness · scalability · documentation · reasoning           │
└───────────────────────────────┬───────────────────────────────────────────┘
                                 ▼
┌─ SCORING PROFILE (versioned, admin-configurable, inheritable) ────────────┐
│ weights per criterion · penalties · bonuses · pass threshold · rounding   │
└───────────────────────────────┬───────────────────────────────────────────┘
                                 ▼
                    final_score, per-criterion breakdown, feedback
```

Profile shape:

```yaml
id: sp.production-sim.default
version: 4
extends: sp.base
pass_threshold: 0.65
weights:
  troubleshooting: 0.30
  technical_implementation: 0.30
  reliability: 0.15
  security: 0.15
  documentation: 0.10
penalties:
  hints: {level_1: 0.01, level_2: 0.03, level_3: 0.08, cap: 0.20}
  resets: {per_reset: 0.02, cap: 0.06}
  retries: {formula: "0.05 * (retry_index)", cap: 0.15}
  overtime: {per_10min_over: 0.02, cap: 0.10}
  blast_radius: {per_violation: 0.15, cap: 0.30}   # heavy: destructive ops matter
bonuses:
  optional_tasks: {per_task: 0.03, cap: 0.09}
  first_try_pass: 0.02
guards:
  min_after_penalties: 0.30      # penalties can't take a working solution to zero
  ai_criteria_max_share: 0.40    # AI-graded criteria capped as share of total
```

Criterion computation is code, not config — each criterion is a named versioned function (`fn.technical_correctness.v2`) that reads signals and returns `[0,1]` plus an explanation object. Admins configure weights, penalties and thresholds; engineers own how a criterion is derived. This is the right split: it gives curriculum staff genuine control without letting a mis-typed weight silently redefine what "security" means.

The profiles the brief specified map directly:

| Mode | Profile | Weights |
|---|---|---|
| Guided Lab | `sp.guided-lab.default` | technical_correctness .70, task_completion .20, efficiency .10 |
| Production Sim | `sp.production-sim.default` | troubleshooting .30, technical_implementation .30, reliability .15, security .15, documentation .10 |
| Project | `sp.project.default` | architecture .20, implementation .25, production_readiness .20, security .15, scalability .10, documentation .10 |

Activities may override weights within guardrails (each weight ±0.15 of profile default, must sum to 1.0, validated in content CI).

**Re-scoring:** because signals are stored and criterion functions are versioned, `POST /admin/rescore` over a cohort is a supported operation. Every score row records `profile_version`, `criterion_fn_versions`, `computed_at`. This is what makes rubric evolution safe (§3.6).

**Feedback generation:** the score is useless without explanation. For every criterion below threshold, emit:

27. What was expected (from the authored spec).
28. What was observed (from the validator/signal).
29. A pointer to the relevant learning resource (topic/subtopic link).
30. For AI-graded criteria, the rubric level awarded plus a 1–2 sentence justification quoting the learner's own artifact.

## 6.5 AI-based evaluation: doing it responsibly

Where used (project design docs, incident notes, code quality, architecture, viva), the grader is engineered rather than prompted casually:

31. **Rubric-anchored, level-based.** Each criterion has 4–5 levels with concrete descriptors and, critically, **2–3 exemplar submissions per level** stored with the rubric. Exemplars go in the prompt. This is the difference between usable and useless AI grading.
32. **Structured output only.** JSON: `{criterion, level, confidence, evidence_quotes[], justification, flags[]}`. Schema-validated; malformed output is retried then escalated.
33. **Multi-sample with agreement check.** Run 3 samples at temperature ~0.3. If levels agree → accept. If they span >1 level → escalate to a stronger model; if still divergent → **route to human review** and mark the score provisional.
34. **Deterministic pre-processing.** Feed the grader *facts*, not raw environments: the diff, the validator results, static-analysis findings, the resource inventory, the learner's written artifact. The grader judges quality given established facts; it does not establish facts.
35. **Prompt-injection defence.** Learner content is enclosed in delimited, clearly-labelled untrusted blocks; the system prompt states that instructions inside are data. A pre-classifier flags injection attempts (which are themselves logged as an integrity signal). And the structural defence: even a fully successful injection can only move AI-graded criteria, capped at 40% of the total (`ai_criteria_max_share`), and cannot touch deterministic criteria at all.
36. **Calibration harness.** A held-out set of human-graded submissions per rubric; measure quadratic-weighted kappa against human graders on every prompt/model change. Ship no rubric below an agreed agreement threshold. Re-run on every model upgrade — a silent model change can shift your grading distribution overnight.
37. **Human-in-the-loop by policy:** 100% human review for certification-bearing final project scores; ~10% random audit otherwise; 100% review for appeals and for any submission where AI confidence is low or flags fired.

**Trade-off:** this is substantially more work than "ask the model to grade it." It is the difference between a credential that means something and one that does not. If you cannot afford the calibration work for a given rubric, make that criterion advisory-only (feedback shown, weight zero) until you can.

---

# Part VII — AI Mentor Architecture

## 7.1 Design goal

The mentor exists to keep learners *productively stuck* — long enough to think, not long enough to quit. Two failure modes to engineer against: the mentor that gives the answer (learning destroyed, cost high), and the mentor that is uselessly vague (frustration, abandonment).

## 7.2 Layered architecture

```
                 ┌──────────────────────────────────────┐
Learner message ───►│ Mentor Service                         │
                 │                                          │
                 │ 1. POLICY RESOLUTION                    │
                 │    mode × difficulty × hints_used        │
                 │    × time_stuck × activity override      │
                 │    → assistance_level, persona,          │
                 │      disclosure_ceiling                  │
                 │                                          │
                 │ 2. INTENT CLASSIFY                       │
                 │    concept_q | error_help | "just        │
                 │    tell me" | off_topic | injection      │
                 │                                          │
                 │ 3. CONTEXT ASSEMBLY (see 7.4)            │
                 │    ── SOLUTION IS NOT AVAILABLE ──       │
                 │                                          │
                 │ 4. LLM GATEWAY call                      │
                 │    model routing, cache, budget          │
                 │                                          │
                 │ 5. OUTPUT GUARDRAIL                      │
                 │    disclosure check, safety,              │
                 │    command-leak detector                 │
                 │                                          │
                 │ 6. ACCOUNTING                            │
                 │    tokens, cost, hint-equivalence         │
                 │    → scoring penalty if applicable       │
                 └──────────────────────────────────────┘
```

## 7.3 Persona and disclosure policy per mode

| | Guided Lab | Production Sim | Project |
|---|---|---|---|
| Persona | Patient tutor | Senior on-call engineer | Staff-level reviewer |
| Opening move | Explains concept, then asks what they've tried | Asks what they've observed so far | Asks what they've decided and why |
| May explain concepts | Yes, fully | Yes, briefly | Yes |
| May interpret errors/logs | Yes, in detail | Yes, but asks the learner to interpret first | Yes |
| May name the specific broken resource | After hint level 2 | Never — may narrow the search space | N/A |
| May give exact commands | Only at hint level 3, and only for syntax already taught | Never | Never |
| May write code/manifests | Snippets ≤5 lines, only illustrating syntax | No | Reviews learner's code; may show a *pattern*, not their solution |
| May review architecture | N/A | Challenges assumptions | Yes — full review, risks, trade-offs, alternatives |
| Proactive | Yes, after 5 min of no progress on a task | Yes, one nudge at 50% of SLA | Only at milestone gates |
| Solution access | None | None | Only after final submission |

The **disclosure ceiling** is a numeric parameter (0–4: `concept_only`, `narrow_search`, `identify_area`, `identify_cause`, `give_command`) resolved per message from policy. It is passed to the guardrail layer, which checks the *output* against it — because prompt instructions alone will not reliably hold the line.

## 7.4 Context assembly (D11)

What the mentor sees:

```
✓ Activity spec: objectives, task instructions, learning objectives, hint ladder text
✓ Authored "concept notes" for this activity (author-written explainers)
✓ Current environment state SUMMARY (structured, refreshed on demand):
    - validator results (which tasks pass/fail, observed vs expected)
    - recent commands + exit codes (last 30)
    - recent stderr excerpts
    - k8s/cloud resource summary (read-only)
✓ Learner's mastery on this activity's skills (adjusts explanation depth)
✓ Conversation history for this attempt
✓ For Sims: the fault's `canonical_diagnostic_path` — used to steer questions,
  never to state the answer; passed only when disclosure_ceiling >= 2
✗ reference_solution — NEVER in Lab/Sim modes, structurally unavailable
✗ solution_apply scripts — NEVER
✗ Other learners' data — NEVER
```

**Structural enforcement:** solutions live in a separate storage bucket and a separate retrieval index with a distinct service identity. The Mentor Service's credentials cannot read it. The Grader Service's can. This is an IAM boundary, not a prompt instruction — the only kind of guardrail that actually holds.

## 7.5 Hint ladder and the "just tell me" problem

Learners will ask for the answer directly. Do not simply refuse — that reads as obstinate and drives them to a third-party chatbot, where you lose all signal.

Escalation contract:

38. First ask: mentor offers the next hint level explicitly, stating the score cost. *"I can give you a stronger hint — that'll cost about 3% on this task's score. Want it?"* Transparency converts an adversarial interaction into an informed choice.
39. Learner accepts → `HINT_REQUESTED` event → hint delivered → penalty recorded.
40. Ladder exhausted → offer the **guided fallback**: "Would you like me to walk you through this task step by step? This task will be marked assisted and won't count toward mastery, but you'll finish the lab and can retry later for a clean score."
41. That last option is important. A learner who abandons learns nothing and churns. A learner who completes with an assisted task learns most of it and comes back. **The assisted flag propagates to the evidence layer**: assisted tasks contribute zero positive evidence to BKT (and a small negative signal), so mastery stays honest while the experience stays humane.

## 7.6 LLM Gateway

A single chokepoint for every model call in the platform (mentor, grader, authoring assistant, feedback generation, summarisation).

Responsibilities:

| Function | Detail |
|---|---|
| **Routing** | Task-class → model tier. Cheap fast model for intent classification, hint retrieval-augmented generation, summarisation. Strong model for grading, architecture review, viva, disagreement escalation. Route by policy, not hardcoded call sites. |
| **Prompt management** | Prompts are versioned artifacts with IDs, stored in the content repo, testable in CI against a regression suite. No inline prompt strings in application code. |
| **Caching** | Exact-match and semantic cache on `(prompt_version, context_hash)`. Concept explanations for the same activity repeat constantly across learners — cache hit rates of 30–50% are realistic and are pure margin. |
| **Budgeting** | Per-attempt token budget, per-learner daily budget, per-tenant monthly budget, global circuit breaker. On budget exhaustion, degrade gracefully to authored static hints — never fail hard. |
| **Redaction** | Strip credentials, tokens, learner PII from context before egress. Scan environment state summaries for secret patterns. |
| **Observability** | Every call logged with prompt version, model, tokens, latency, cost, policy decisions, guardrail verdicts, attempt ID. Sampled full transcripts to the review queue. |
| **Fallback** | Multi-provider, health-checked, automatic failover. The mentor being down must not block an attempt. |
| **Evaluation hooks** | Shadow-run new prompt versions against live traffic offline; compare on the calibration set before promotion. |

## 7.7 Guardrails summary

| Risk | Control |
|---|---|
| Solution leakage | Solution excluded by IAM boundary; output-side command/manifest-leak detector; disclosure ceiling enforced on output; adversarial suite in content CI (§3.5 step 8) |
| Prompt injection from learner files | Untrusted-content delimiters, injection classifier, and the structural cap that AI can only influence ≤40% of score |
| Cost blowout | Per-attempt/learner/tenant budgets, caching, cheap-model routing, circuit breaker |
| Doing the learner's work | Mentor has **no write access** to the environment. It cannot execute commands or edit files. This is absolute in Lab/Sim modes. (A future "copilot mode" for L1 onboarding could be explicitly opt-in and marked assisted.) |
| Hallucinated technical guidance | Ground on retrieved authored notes + live environment state; instruct and verify "if unsure, say so and suggest how to find out" — which is also good pedagogy |
| Harmful/off-topic use | Standard safety filtering plus scope policy: redirect off-topic to the course; log |
| Unfair advantage / equity | Same policy for all learners in an activity; hint costs are transparent and uniform |

---

# Part VIII — System Architecture, Components, APIs, Data Model

## 8.1 High-level architecture

```
                              ┌──────────────┐
                              │   LEARNER    │
                              └──────┬───────┘
                                     │ HTTPS / WSS
                        ┌────────────▼─────────────┐
                        │   CDN + WAF + Rate Limit  │
                        └────────────┬─────────────┘
              ┌────────────────────────────┼────────────────────────────┐
              ▼                             ▼                             ▼
     ┌─────────────────┐          ┌──────────────────┐          ┌──────────────────┐
     │  PRACTICE UI     │          │   API GATEWAY     │          │   WS GATEWAY      │
     │  (Next.js SPA)   │          │  authn/z, quota,   │          │  attempt streams   │
     │  xterm · Monaco  │          │  idempotency       │          │  terminal · editor │
     └─────────────────┘          └────────┬─────────┘          └────────┬─────────┘
                                             │                              │
                        ┌─────────────────────┼───────────────────────────┘
                        ▼                     ▼
     ╔═══════════════════════════════════════════════════════════════╗
     ║ PRACTICE CORE (modular monolith, HA, stateless)                  ║
     ║                                                                    ║
     ║  ┌──────────────┐  ┌─────────────┐  ┌───────────────┐          ║
     ║  │ Catalog &     │  │ Attempt     │  │ Practice        │          ║
     ║  │ Content Svc   │  │ Service     │  │ Orchestrator    │◄─┐       ║
     ║  │ (activities,  │  │ (lifecycle, │  │ (saga coord.)   │  │       ║
     ║  │ versions)     │  │ events)     │  └───────┬───────┘  │       ║
     ║  └──────────────┘  └─────────────┘          │           │       ║
     ║  ┌──────────────┐  ┌─────────────┐  ┌───────▼───────┐  │       ║
     ║  │ Skill &       │  │ Recommend-  │  │ Eligibility &   │  │       ║
     ║  │ Mastery Svc   │  │ ation Svc   │  │ Quota Svc       │  │       ║
     ║  └──────────────┘  └─────────────┘  └───────────────┘  │       ║
     ║  ┌──────────────┐  ┌─────────────┐  ┌───────────────┐  │       ║
     ║  │ Progress &    │  │ Notification│  │ Admin/CMS API   │  │       ║
     ║  │ Dashboard     │  │ Svc         │  │                 │  │       ║
     ║  └──────────────┘  └─────────────┘  └───────────────┘  │       ║
     ╚═══════════════════════╤═══════════════════════════╤══╪═══════╝
                              │ events (NATS/Kafka)          │  │ gRPC
       ┌───────────────────────┴──────┬────────────────────┴──┼──────────────┐
       ▼                                ▼                       ▼               ▼
┌───────────────────┐  ┌────────────────────────┐  ┌──────────────────┐  ┌───────────┐
│ ENVIRONMENT         │  │ EVALUATION SERVICE       │  │ AI GATEWAY         │  │ ANALYTICS │
│ ORCHESTRATOR (Go)   │  │ (Python workers)         │  │ (Python)           │  │ PIPELINE  │
│                     │  │                          │  │                    │  │           │
│ • tier drivers       │  │ • Validator Runner        │  │ • Mentor Svc        │  │ • ingest  │
│   T0 T1 T2 T3        │  │ • Scoring Engine          │  │ • Grader Svc        │  │ • rollups │
│ • warm pools          │  │ • Rubric/AI grading       │  │ • prompt mgmt       │  │ • ClickH. │
│ • account pool mgr    │  │ • Feedback generator      │  │ • budget/cache      │  │           │
│ • session broker      │  │ • Re-score jobs           │  │ • guardrails        │  │           │
│ • reaper / TTL         │  │                          │  │                    │  │           │
│ • cost meter           │  │                          │  │                    │  │           │
└─────────┬─────────┘  └───────────┬────────────┘  └────────┬─────────┘  └─────┬─────┘
          │                          │                          │                  │
          │ K8s API / cloud APIs      │ read-only creds           │ LLM providers    │
          ▼                          ▼                          ▼                  │
┌───────────────────────────────────────────────────────────────────┐            │
│ EXECUTION FABRIC                                                     │            │
│ T0 browser WASM │ T1 gVisor pods │ T2 microVMs │ T3 cloud accts       │            │
└───────────────────────────────────────────────────────────────────┘            │
                                                                                     │
╔═════════════════════════════ SHARED PLATFORM ═══════════════════════════════╪══╗
║ PostgreSQL (primary + replicas, partitioned events)      Redis (cache, locks,    │  ║
║ rate limit, warm-pool CAS, pub/sub)     NATS JetStream / Kafka (events)          │  ║
║ S3/GCS (snapshots, recordings, artifacts, logs)     ClickHouse (analytics)  ◄──┘  ║
║ Vault/KMS (secrets, credential brokering)   OTel → Prometheus/Loki/Tempo/Grafana ║
╚════════════════════════════════════════════════════════════════════════════════╝
```

Communication rules:

- UI ↔ API Gateway: REST/JSON for commands and queries.
- UI ↔ WS Gateway: WebSocket for terminal, editor sync, live events, mentor streaming. One multiplexed socket plus a dedicated terminal socket per session.
- Practice Core ↔ Orchestrator: **gRPC for synchronous requests** (provision, connect, destroy) + **events for asynchronous state** (`env.ready`, `env.failed`, `env.destroyed`, `env.idle`). Provisioning is modelled as a saga with compensating actions, because half-provisioned cloud resources cost money.
- Practice Core ↔ Evaluation: **queue-based**. Validation and scoring are jobs, not RPCs. This gives you retry, backpressure, and independent scaling for the spiky "everyone submits at 9pm" pattern.
- Everything → event bus → Analytics: one canonical event stream, consumed by ClickHouse ingestion, the mastery updater, the recommendation feature store, and the cost aggregator. Fan-out, no point-to-point analytics coupling.
- Orchestrator → Cost Meter → Core: environments report metered usage every 60s; budget breaches flow back as commands.

## 8.2 Why a modular monolith plus three services (D6)

**What:** One deployable "Practice Core" containing catalog, attempt, orchestration-coordination, skill, recommendation, progress, notification and admin modules with enforced internal boundaries (separate schemas, no cross-module table access, module-to-module calls through interfaces). Three genuinely separate services: Environment Orchestrator, Evaluation, AI Gateway.

**Why these three are separate:**

- **Environment Orchestrator** — different language (Go, for concurrency and K8s client maturity), different failure blast radius (it holds cloud credentials and can spend money), different scaling shape (long-lived state machines, not request/response), and different deployment cadence (you do not want a UI fix redeploying the thing managing 3,000 live environments).
- **Evaluation** — Python for the ML/analysis ecosystem, bursty scaling, runs untrusted-adjacent workloads (static analysis on learner code), needs its own isolation and its own node pool.
- **AI Gateway** — independent budget/circuit-breaker semantics, provider SDK churn, and it must be able to fail without taking the platform with it.

**Why not full microservices for the rest:** at this stage, Attempt, Skill, Progress and Recommendation share transactional boundaries constantly (starting an attempt touches eligibility, quota, activity state and events in one transaction). Splitting them buys distributed-transaction pain and buys nothing operationally. Extract later, when a specific module has a distinct scaling or ownership need — the module boundaries make that a refactor, not a rewrite.

**Trade-offs:** the monolith requires discipline (a linter/ArchUnit-style check that enforces module boundaries in CI, or it degenerates into a big ball of mud within a year). Budget for that enforcement explicitly.

## 8.3 API architecture

Principles:

42. **Resource-oriented, versioned, `/v1/…`.** Nouns for state, sub-resources for relationships.
43. **Long-running work returns an Operation, not a blocked request.** `POST /attempts` returns `202` with the attempt in `PROVISIONING` plus a stream URL. Never make the client wait 40 seconds on a synchronous provision.
44. **Commands that are not CRUD get explicit action sub-resources** — `POST /attempts/{id}:submit` rather than pretending it's a PATCH. Be honest about verbs; state machines are not REST-shaped and pretending otherwise produces unclear APIs.
45. **Idempotency-Key required on every mutating call.** Terminals reconnect, users double-click, networks retry.
46. **Reads and writes separated by shape** — dashboards and catalogs hit denormalised read models (materialised views / cache), not the transactional tables.
47. **Real-time over WS, not polling.** Validation results, provisioning progress, TTL warnings, mentor tokens all stream.
48. **Consistent error envelope** with a machine code, human message, retryability, and a `trace_id`.

Learner surface:

```
# Catalog & discovery
GET    /v1/practice/activities?course&skill&mode&difficulty&duration_max&status
GET    /v1/practice/activities/{id}                    # latest published version for this learner
GET    /v1/practice/activities/{id}/versions/{v}
GET    /v1/practice/recommendations?limit&context=home|skill|course
POST   /v1/practice/recommendations/{id}:dismiss

# Attempts (the core resource)
POST   /v1/practice/attempts                            # {activity_id} → 202, PROVISIONING
GET    /v1/practice/attempts?status&activity_id&cursor
GET    /v1/practice/attempts/{id}                        # state, tasks, env endpoints, TTL
POST   /v1/practice/attempts/{id}:validate               # {scope: "task:t3" | "all"} → 202 job
POST   /v1/practice/attempts/{id}:submit
POST   /v1/practice/attempts/{id}:suspend
POST   /v1/practice/attempts/{id}:resume
POST   /v1/practice/attempts/{id}:reset                  # {mode: task|all|keep_files}
POST   /v1/practice/attempts/{id}:abandon
GET    /v1/practice/attempts/{id}/evaluation              # scores, criteria, feedback
GET    /v1/practice/attempts/{id}/events?after_seq        # replay/audit
GET    /v1/practice/attempts/{id}/artifacts               # snapshot & submission downloads

# Tasks & assistance
GET    /v1/practice/attempts/{id}/tasks
POST   /v1/practice/attempts/{id}/tasks/{key}:skip
POST   /v1/practice/attempts/{id}/tasks/{key}/hints       # → next hint level + penalty preview
POST   /v1/practice/attempts/{id}/mentor/messages         # → SSE/WS stream
GET    /v1/practice/attempts/{id}/mentor/messages

# Environment (thin façade over orchestrator; learners never call the orchestrator)
GET    /v1/practice/attempts/{id}/environment             # status, endpoints, expires_at
POST   /v1/practice/attempts/{id}/environment:extend       # quota-checked TTL extension
WSS    /v1/practice/attempts/{id}/terminal?session_id
WSS    /v1/practice/attempts/{id}/stream
GET    /v1/practice/attempts/{id}/files?path               # editor file API (T0/T1 Monaco mode)
PUT    /v1/practice/attempts/{id}/files?path

# Projects (milestones)
POST   /v1/practice/attempts/{id}/milestones/{key}:submit
GET    /v1/practice/attempts/{id}/milestones
POST   /v1/practice/attempts/{id}/defence/messages         # viva

# Learner state
GET    /v1/practice/progress?course_id
GET    /v1/practice/skills                                  # mastery per skill + band + evidence
GET    /v1/practice/skills/{id}                              # detail, graph neighbourhood, activities
GET    /v1/practice/dashboard                                # denormalised read model
```

**Admin surface:** `/v1/admin/activities`, `/versions:publish`, `:canary`, `:rollback`, `/blueprints`, `/fixtures`, `/faults`, `/rubrics`, `/scoring-profiles`, `/skills`, `/skill-edges`, `/analytics/*`, `/costs/*`, `/rescore`, `/review-queue`, `/environments` (live view, force-destroy), `/quotas`.

**Internal (not public):** orchestrator gRPC (Provision, Connect, Snapshot, Restore, Destroy, Meter), evaluation queue contracts, AI gateway gRPC.

**Why not exactly the endpoint list in the brief:** the brief's list is close but flattens three important things — (a) activity **versions** must be addressable, or you cannot audit or re-run; (b) attempts, not activities, are the primary resource (an attempt can outlive an activity version, and all learner state hangs off it); (c) starting an activity is a *creation of an attempt*, not a `:start` on the activity, because the activity is shared, immutable content and should never appear to be mutated by a learner.

## 8.4 Database architecture

**Primary store: PostgreSQL.** One logical database, schema-per-bounded-context (content, learner, attempt, skill, env, billing, admin). Rationale: the workload is transactional, relational, and moderate-volume; the skill graph is small enough for recursive CTEs; JSONB covers the flexible spec/rubric/payload needs. Adding a graph DB, a document DB and a search engine on day one is complexity without benefit. **Exceptions:** ClickHouse for analytics/telemetry at scale (Phase 3), Redis for cache/locks/ephemeral coordination, S3 for blobs.

**Core entities**

```
── identity & curriculum ─────────────────────────────────────────────
tenant(id, name, plan, settings_jsonb)
user(id, tenant_id, email, role, status, created_at)
enrollment(id, user_id, course_id, cohort_id, status, started_at, expires_at)

course(id, tenant_id, slug, title, status)
module(id, course_id, title, position)
topic(id, module_id, title, position)
subtopic(id, topic_id, title, position)
topic_skill(topic_id, skill_id, coverage_weight, bloom_level)    PK(topic_id, skill_id)

── skills ────────────────────────────────────────────────────────────
skill(id, slug, name, domain, description, decay_half_life_days,
      bkt_p_init, bkt_p_transit, bkt_p_slip, bkt_p_guess, status)
skill_edge(from_skill_id, to_skill_id, type, strength)           PK(from,to,type)
skill_closure(ancestor_id, descendant_id, depth, edge_types[])   -- materialised

── content ─────────────────────────────────────────────────────────
activity(id, tenant_id, slug, mode, owner_id, status, retired_at)
activity_version(id, activity_id, version, semver_kind, status, spec_jsonb,
                  blueprint_version_id, scoring_profile_version_id,
                  difficulty_level, difficulty_elo, estimated_minutes,
                  cost_budget_usd, published_at, published_by, canary_pct)
activity_skill(activity_version_id, skill_id, weight, is_primary, bloom_level)
activity_topic(activity_version_id, topic_id, relevance)
activity_task(id, activity_version_id, key, position, required, spec_jsonb)
validator(id, activity_task_id, key, type, config_jsonb, weight, severity, timeout_ms)
hint(id, activity_task_id, level, text_md, penalty)

blueprint(id, slug); blueprint_version(id, blueprint_id, version, tier,
          capabilities[], spec_jsonb, image_refs[], provisionable)
fixture(id, slug); fixture_version(id, fixture_id, version, spec_jsonb)
fault(id, slug, symptom_class, skills_probed[], canonical_path_jsonb)
fault_version(id, fault_id, version, spec_jsonb)

rubric(id, slug); rubric_version(id, rubric_id, version, criteria_jsonb, exemplars_jsonb)
scoring_profile(id, slug); scoring_profile_version(id, profile_id, version, config_jsonb)

── attempts & evidence ───────────────────────────────────────────────
attempt(id, tenant_id, user_id, activity_id, activity_version_id, mode,
        status, retry_of_attempt_id, retry_index, assistance_flags[],
        created_at, started_at, submitted_at, completed_at, expires_at,
        active_seconds, reset_count, hint_penalty_total, version)
attempt_task_state(attempt_id, task_key, status, first_pass_at, attempts_count,
                    hints_used_max_level, skipped, assisted)  PK(attempt_id, task_key)
attempt_events(attempt_id, seq, occurred_at, actor, type, payload_jsonb)  -- partitioned
validation_run(id, attempt_id, scope, trigger, started_at, finished_at, status)
validator_result(id, validation_run_id, validator_id, status, observed_jsonb,
                  expected_jsonb, duration_ms, evidence_ref)
attempt_signal(attempt_id, signal_key, value_num, value_jsonb, computed_at)
attempt_score(id, attempt_id, profile_version_id, criterion_fn_versions_jsonb,
              final_score, passed, breakdown_jsonb, penalties_jsonb,
              computed_at, rescored_from_id, rescore_reason)
ai_conversation(id, attempt_id, mode, policy_version, token_total, cost_usd)
ai_message(id, conversation_id, seq, role, content_redacted, tokens,
           disclosure_level, guardrail_verdict, model, prompt_version)
artifact(id, attempt_id, kind, storage_uri, bytes, checksum, created_at, retain_until)

── projects ──────────────────────────────────────────────────────────
project_milestone_state(attempt_id, milestone_key, status, submitted_at, score)
project_submission(id, attempt_id, milestone_key, repo_ref, commit_sha,
                    artifacts_jsonb, submitted_at)
defence_session(id, attempt_id, questions_jsonb, transcript_ref, score, reviewed_by)

── learner state ─────────────────────────────────────────────────────
skill_mastery(user_id, skill_id, p_mastery, last_evidence_at, evidence_count,
              elo_rating, review_due_at, band)                PK(user_id, skill_id)
mastery_evidence(id, user_id, skill_id, attempt_id, delta, p_before, p_after,
                  weight, created_at)
learner_activity_state(user_id, activity_id, best_score, latest_score, status,
                        attempts_count, last_attempt_at, cooldown_until)
                        PK(user_id, activity_id)
learner_elo(user_id, skill_domain, rating, matches)            PK(user_id, skill_domain)
recommendation(id, user_id, activity_id, score, features_jsonb, reason_code,
                reason_params_jsonb, generated_at, shown_at, clicked_at,
                started_at, dismissed_at, ranker_version)

── environments & cost ───────────────────────────────────────────────
environment(id, attempt_id, tier, blueprint_version_id, status, region,
            cluster_id, namespace, cloud_account_id, endpoints_jsonb,
            ready_at, expires_at, idle_since, destroyed_at, destroy_reason)
environment_pool(key, tier, blueprint_version_id, target_size, available, region)
cloud_account(id, provider, account_ref, ou, status, claimed_by_env_id,
              last_nuked_at, nuke_verified_at, quarantine_reason)
usage_meter(id, environment_id, attempt_id, window_start, window_end,
            cpu_seconds, mem_gb_seconds, storage_gb_hours, egress_gb,
            cloud_cost_usd, ai_cost_usd, total_cost_usd)
budget(scope_type, scope_id, period, limit_usd, spent_usd, alert_thresholds[])
```

**Relationships that matter most**

- `attempt.activity_version_id` is **never null and never updated** — the reproducibility anchor.
- `mastery_evidence` links every mastery change to the attempt that caused it — full explainability of "why is my mastery this?"
- `attempt_signal` is the interface between validation and scoring; scoring reads signals, never raw events. This keeps the scoring engine fast and re-runnable.
- `activity_skill` is on `activity_version_id`, not `activity_id` — skill mapping can legitimately change between versions and historical evidence must keep the old mapping.

**Indexing strategy**

| Table | Index | Serves |
|---|---|---|
| `attempt` | `(user_id, status, created_at DESC)` | "Continue practice", history |
| `attempt` | `(activity_version_id, status)` partial `WHERE status IN ('IN_PROGRESS','SUSPENDED')` | canary monitoring, version retirement checks |
| `attempt` | `(tenant_id, created_at DESC)` | admin views |
| `attempt_events` | PK `(attempt_id, seq)`; partition by month on `occurred_at`; BRIN on `occurred_at` | replay, audit; cheap time pruning |
| `attempt_events` | GIN on `payload_jsonb` **only** for the small set of queried keys (use `jsonb_path_ops` on an expression index, not the whole doc) | targeted analytics; full GIN on a high-volume table is a write-throughput trap |
| `validator_result` | `(validation_run_id)`, `(validator_id, status, created_at)` | flakiness analysis |
| `skill_mastery` | PK `(user_id, skill_id)`; `(user_id, p_mastery)`; partial `(user_id) WHERE review_due_at < now()` | dashboard, recommendation |
| `skill_closure` | `(descendant_id, ancestor_id)` and `(ancestor_id, descendant_id)` | prerequisite checks both directions |
| `learner_activity_state` | PK `(user_id, activity_id)`; `(user_id, status)` | eligibility filter (hot path) |
| `activity_version` | `(activity_id, version DESC)`; partial `WHERE status='PUBLISHED'`; GIN on `spec_jsonb` for catalog facets | catalog |
| `recommendation` | `(user_id, generated_at DESC)`; `(ranker_version, shown_at)` | serving + experiment analysis |
| `environment` | partial `(status, expires_at) WHERE status NOT IN ('DESTROYED')` | **the reaper's hot query — must be fast and must never seq-scan** |
| `usage_meter` | `(attempt_id)`, `(window_start)` BRIN, `(environment_id)` | cost rollups |
| `cloud_account` | partial `(status) WHERE status='AVAILABLE'` | pool claim |

Additional strategy:

- Partition `attempt_events`, `validator_result`, `usage_meter` and `ai_message` by month; detach and archive to S3/Parquet after the hot window.
- Read replicas serve dashboards, catalog and analytics; the primary serves attempt mutations only.
- Materialised views for dashboard aggregates (`mv_learner_practice_summary`, `mv_activity_health`), refreshed incrementally every 5 minutes — dashboards must never aggregate over `attempt_events` live.
- Row-Level Security on `tenant_id` for every learner-facing table, enforced at the connection role level, as defence-in-depth behind application authorisation.
- Avoid `SELECT ... FOR UPDATE` on `attempt` in hot paths; use the `version` column with optimistic concurrency and retry.

## 8.5 Frontend architecture

| Concern | Choice | Rationale |
|---|---|---|
| Framework | Next.js (App Router), React, TypeScript | SSR for catalog SEO/perf, client-heavy workspace, one codebase |
| Server state | TanStack Query | caching, invalidation on WS events, retry semantics |
| Client state | Zustand (workspace UI state only) | terminal layout, panel sizes, local drafts — small and local |
| Realtime | One multiplexed WS for events + a dedicated WS per terminal session | isolate high-throughput PTY traffic from control messages |
| Terminal | xterm.js + WebGL renderer + serialize addon | performance, reconnect scrollback |
| Editor | Monaco (Phase 1) → embedded OpenVSCode Server (Phase 3) | cost/complexity ladder |
| Design system | Tokenised component library, dark-first (developers live in dark mode) | consistency, and the workspace is a dense tool UI, not a marketing page |
| Offline/degraded | Read-only attempt view when WS is down; queued file saves; explicit reconnect banner | flaky networks are the norm for the target audience |

Workspace layout (the screen learners spend hours in):

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Lab title · L2 · ⏱ 34:12 remaining · [Check] [Submit] [Reset ▾] [Help]   │
├──────────────────────┬──────────────────────────────────────────────────┤
│ TASKS                 │ EDITOR / TERMINAL / PREVIEW (tabbed or split)     │
│ ✓ 1 Build image        │                                                  │
│ ✓ 2 Push to registry    │ $ kubectl get pods                              │
│ ▶ 3 Create Deployment  │ NAME            READY   STATUS                   │
│   4 Expose service      │ node-app-7d…    0/1     CrashLoopBackOff        │
│   5 Verify               │                                                  │
│                         │                                                  │
│ INSTRUCTIONS            │                                                  │
│ (current task only,     │                                                  │
│ collapsible)            │                                                  │
│ [Hint (−3%)]            │                                                  │
├──────────────────────┴──────────────────────────────────────────────────┤
│ MENTOR (collapsible drawer) · validation results stream here too         │
└─────────────────────────────────────────────────────────────────────────┘
```

Three UX rules with architectural consequences: show only the current task's instructions (reduces scanning-for-answers), surface validation failure inline against the task (not as a toast), and always show remaining time and cost-relevant state (TTL is a real constraint; hiding it produces angry learners).

---

# Part IX — Security Architecture

The platform executes arbitrary attacker-controlled code, with cloud credentials, at scale. Treat every learner as a hostile actor who has paid for access.

## 9.1 Threat model

| # | Threat | Impact | Primary controls |
|---|---|---|---|
| T1 | Container/VM escape → host → other tenants | Catastrophic | gVisor (T1), Kata/Firecracker (T2), no privileged containers outside T2, dedicated node pools, seccomp/AppArmor, current kernels, no host mounts, PSS `restricted` |
| T2 | Lateral movement to platform control plane | Catastrophic | Learner namespaces have no SA token; NetworkPolicy denies all in-cluster egress except the egress proxy; control-plane services on a separate cluster/VPC; K8s API not reachable from learner pods |
| T3 | Cross-learner data access | Severe | Namespace + RLS + per-tenant KMS keys + object-storage prefix policies; no shared volumes; signed, short-TTL, path-scoped URLs for artifacts |
| T4 | Cloud account abuse (crypto mining, spam, data exfil, botnet) | Severe + reputational | SCP/Org Policy denies expensive & abusive services; egress allowlist; anomaly detection on spend and network; GuardDuty/Defender enabled and undeletable; hard budget kill |
| T5 | Credential theft / persistence | Severe | Short-lived brokered STS only; no long-lived keys anywhere in the environment; credentials revoked on attempt state change; nuke removes IAM artifacts; secrets never in images or env vars visible to the learner |
| T6 | Resource exhaustion / DoS on shared infra | High | ResourceQuota, LimitRange, PID limits, fork-bomb protection (`pids.max`), disk quotas, network bandwidth shaping, per-tenant node pools for the largest customers |
| T7 | Using the platform as an attack proxy | High (legal) | **Default-deny egress with allowlist** through an inspecting proxy: package registries, the platform's own registry, and blueprint-declared domains only. No arbitrary internet. Log every allowed connection. |
| T8 | Prompt injection / grade manipulation | Moderate | §6.5 controls; AI criteria capped at 40%; deterministic scoring immune |
| T9 | Cheating / credential sharing | Moderate (product) | Integrity signals (§4.4), viva for certification, proctoring option for high-stakes, device/session anomaly detection |
| T10 | Supply-chain (malicious base image, poisoned fixture) | High | Signed images (cosign), pinned digests not tags, SBOM + `trivy` gate in content CI, private registry mirror, no `latest` |
| T11 | Malicious activity content (insider or compromised author) | High | Content CI runs in an isolated account; publish requires a second reviewer; blueprint changes require platform-team approval; content specs cannot grant capabilities beyond the tier's declared set |
| T12 | Data exfiltration by an authorised admin | Moderate | Just-in-time admin elevation, audit logging of every admin read of learner data, no bulk export without approval |

## 9.2 The egress problem (most underestimated)

Learners need `pip install`, `npm install`, `docker pull`, `terraform init`, and `apt-get`. That looks like "they need internet." Giving them internet turns your platform into free compute for scanning, spamming and mining.

**Solution: an explicit-allow egress proxy per environment.**

```
Learner pod ──(NetworkPolicy: egress ONLY to proxy:3128)──► Egress Proxy (Squid/Envoy)
                                                                    │
                          ┌─────────────────────┼─────────────────────┐
                          ▼                       ▼                       ▼
                  registry mirror         package mirrors        blueprint allowlist
                  (pull-through)          (PyPI/npm/apt/Go proxy)  (e.g. api.github.com)
```

- Default allowlist: platform registry, package mirrors, and the cloud API endpoints relevant to the tier.
- Blueprints may declare additional domains; these are reviewed at publish time.
- All other egress is denied with a **helpful error** injected into the learner's terminal ("outbound access to `x.com` is blocked; the packages you need are available from the internal mirror").
- Cache aggressively at the mirror — this both cuts egress cost meaningfully and speeds up every environment.
- DNS is also constrained (the pod's resolver only resolves allowlisted names) — otherwise DNS becomes the exfiltration channel.

**Trade-off:** allowlists break labs when a tool reaches an unexpected domain. Mitigate with a "blocked egress" report in content CI (run the golden path, collect every denied domain, prompt the author to declare or remove it). Never solve it by opening egress.

## 9.3 Credential and secret architecture

```
Vault / KMS ──► Credential Broker (in Environment Orchestrator, never in learner env)
                    │
                    ├─ mints STS AssumeRoleWithWebIdentity (attempt_id as subject, 1h)
                    ├─ refreshes while attempt IN_PROGRESS; stops immediately otherwise
                    ├─ writes to the environment via a sidecar that owns the credential file
                    │  (learner-readable path, but rotated and revocable out-of-band)
                    └─ revokes on: submit, suspend, idle, TTL, budget breach, abuse signal
```

- Platform secrets (DB passwords, LLM API keys, cloud admin roles) are never on the same network segment as learner workloads.
- Validator credentials are minted per validation run, read-only, 5-minute TTL, scoped by resource tags to that one environment.
- Any secret that appears in a learner's environment must be assumed compromised — design so that this is always acceptable (scoped, short-lived, sandbox-only).

## 9.4 Abuse prevention and rate limiting

| Limit | Scope | Default |
|---|---|---|
| Concurrent environments | learner | 1 (2 with an active project) |
| Environment starts | learner / hour | 10 |
| Validation runs | attempt / minute | 6 |
| Mentor messages | attempt / hour | 40 (token-budget bounded too) |
| API requests | user / minute | 300 |
| WS messages | session / second | 100, with backpressure |
| Daily cloud spend | learner | tier-dependent, e.g. $5 |
| Egress volume | environment | 2 GB, then throttle + alert |
| CPU sustained | environment | quota-enforced; >90% for 20 min triggers a mining heuristic review |

Anomaly detection worth building early: sustained max CPU with no terminal input (mining), high egress to a single unusual destination, mass account creation from one IP, environments requested in tight loops, and identical workspace checksums across many learners.

## 9.5 Audit logging

Immutable, append-only, separate store (write-only credentials from the app, WORM retention):

- Every environment lifecycle event with actor, attempt, blueprint, and credentials issued.
- Every command executed (from the session broker tap).
- Every credential mint and revoke.
- Every admin action, including reads of learner data.
- Every scoring computation and re-score.
- Every guardrail trigger and integrity flag.

Retain 24 months minimum; longer where enterprise contracts require it.

---

# Part X — Cloud Cost Architecture

## 10.1 Unit economics first

Before designing controls, define the unit: **cost per completed attempt.**

```
cost_per_attempt = env_compute + env_storage + egress + control_plane_share
                    + validation_compute + ai_tokens + snapshot_storage
```

Illustrative targets (must be measured, not assumed):

- T0 browser lab ~ $0.002 (AI only)
- T1 guided lab (35 min) ~ $0.04 (0.6 env-hr @ shared, spot ARM + $0.01 AI)
- T2 k8s sim (60 min) ~ $0.22
- T3 cloud sim (90 min) ~ $0.90
- T3 project (12 hrs over 2 weeks, suspended between) ~ $4.50

Then: **gross margin per learner per month = subscription − Σ(attempts × cost).** Instrument this from week one; make it a dashboard the whole team sees. Every architecture decision below is justified by moving these numbers.

## 10.2 The cost control stack

| Layer | Control | Expected saving |
|---|---|---|
| **Tier down** | Route to the cheapest tier that satisfies capabilities; author guidance and CI warnings when a blueprint over-specifies | **Largest single lever** — 10–100× on affected activities |
| **Ephemerality** | Nothing runs longer than its TTL; no persistent learner infrastructure, ever | Foundational |
| **Idle kill** | Two-signal idle detection, aggressive defaults, snapshot-and-destroy rather than pause | 30–50% of env-hours |
| **Warm-pool sizing** | Demand-predicted pools, zero overnight, per-region schedules tied to cohort calendars | Balances UX vs. idle burn |
| **Spot / ARM** | Learner workloads on spot with graceful drain (snapshot on preemption notice) and ARM64 where images allow | 40–70% on compute |
| **Bin packing** | High pod density per node, right-sized requests, descheduler consolidation | 15–30% |
| **Registry & package mirrors** | Pull-through caches; cross-AZ traffic avoidance; single-AZ environments | Meaningful egress + faster starts |
| **Storage lifecycle** | Snapshots to S3 IA/Glacier per §4.3; delete intermediates at 7 days; compress with zstd | Large at scale |
| **AI cost** | Cheap-model routing, caching, per-attempt token budgets, static-hint fallback | 50–70% of naive AI spend |
| **Cloud sandbox discipline** | Account pooling, mandatory nuke + verification, orphan sweeper, SCP-denied expensive SKUs | Prevents the tail-risk blowout |
| **Quota & budget** | Per-learner daily, per-cohort, per-tenant, global; hard stops not soft warnings | Bounds worst case |

## 10.3 Metering and attribution

Every environment emits a usage record every 60s tagged with `attempt_id`, `activity_version_id`, `course_id`, `tenant_id`, `tier`, `region`. Cloud costs are attributed by mandatory resource tagging (`attempt_id`, `tenant_id`) plus per-account cost for T3 (one account = one attempt makes attribution exact — a strong secondary argument for account-per-attempt).

Rollups: attempt → activity → course → tenant → global, hourly, into ClickHouse. This directly feeds:

- The admin cost dashboard (cost per learner, per course, per activity).
- Content decisions ("this lab costs $1.80 and teaches the same skills as one that costs $0.06").
- Pricing and packaging decisions.
- Budget enforcement.

## 10.4 Budget enforcement chain

```
usage_meter ──► budget evaluator (every 60s)
                    │
                    ├─ 50%  → informational event
                    ├─ 80%  → warn learner in UI; notify admin for tenant scope
                    ├─ 100% → block NEW environment starts in scope; existing continue
                    └─ 120% → revoke credentials, snapshot, force-destroy, page on-call
```

Independent of this, cloud-native budget alarms (AWS Budgets / Azure Cost Alerts / GCP Budgets) act as a second, out-of-band tripwire per sandbox account. Never rely on a single mechanism you also operate — if your metering pipeline breaks, the cloud provider's alarm is what saves you.

## 10.5 Supporting thousands of learners

Rough capacity model for 10,000 monthly active learners:

- ~6 attempts/learner/month = 60,000 attempts.
- Mix: 70% T1 (avg 0.7 env-hr), 20% T2 (1.0), 8% T3-sim (1.5), 2% T3-project (12 suspended-adjusted → ~4 active env-hr).
- Env-hours: 42,000 (T1) + 12,000 (T2) + 7,200 (T3 sim) + 4,800 (T3 project).
- Peak concurrency (evenings/weekends, ~8× average): plan for ~700–900 concurrent T1 environments, ~120 T2, ~60 T3.
- T1 at 2 vCPU/4 GB with 70% packing on 64-vCPU spot nodes → ~30 nodes at peak, ~8 at trough. Autoscale aggressively; the trough is 3am and should cost almost nothing.
- Cloud account pool: peak T3 concurrency ~60 → pool of ~120 accounts (2× for nuke-in-flight and quarantine headroom).

The architecture scales by **sharding practice clusters regionally** (one cluster per region per ~2,000 concurrent environments) with the control plane global and stateless. The bottleneck you will hit first is not compute — it is K8s API/etcd throughput from namespace churn, and cloud account quota. Both are known and planned for above.

---

# Part XI — Observability and Analytics

## 11.1 What to monitor in real time

**Learner-experience SLIs (page on these):**

| SLI | SLO | Why it matters |
|---|---|---|
| Time-to-ready p50 / p95 | ≤3s / ≤20s (T1–T2) | Directly predicts lab start-abandonment |
| Provision success rate | ≥99.5% | A failed provision is a lost learner |
| Terminal input→echo latency p95 | ≤120ms | Below this it feels local; above it feels broken |
| Terminal session drop rate | <0.5%/hour | Dropped sessions destroy attempts |
| Validation latency p95 | ≤8s | Feedback loop tightness |
| Validator ERROR rate | <0.2% | Platform-caused unfairness |
| Evaluation completion p95 | ≤60s (deterministic ≤5s) | Learners wait on results |
| Mentor first-token latency p95 | ≤2s | Perceived responsiveness |
| WS gateway availability | 99.9% | |

**Safety/cost SLIs (page on these too):**

- Orphaned environments (any environment past deadline still alive) — **target zero**, alert on ≥1.
- Nuke verification failures — target zero, page immediately.
- Accounts in QUARANTINED — alert on any.
- Hourly spend vs. forecast — alert at 1.5×.
- Egress denials spiking on an activity — content bug or abuse.
- Guardrail triggers per 1,000 mentor messages — trend alert.

## 11.2 Telemetry architecture

```
Every service ──OTel SDK──► OTel Collector ──┬──► Prometheus (metrics)
                                                ├──► Loki (logs)
                                                ├──► Tempo (traces)
                                                └──► Kafka/NATS ──► ClickHouse (product analytics)

Trace context propagates: HTTP → gRPC → queue message → validator job → LLM call.
attempt_id is a first-class span attribute EVERYWHERE.
```

The single most valuable observability decision: **`attempt_id` as a universal correlation key.** Given an attempt ID, an engineer must be able to retrieve the full trace across API, orchestrator, environment, validator, evaluation and AI within one query. Build this on day one; support and debugging depend on it entirely.

**Log separation:** environment logs (learner's containers) go to a separate, tenant-scoped, short-retention pipeline — never into the platform's main log store. They are high-volume, untrusted, and potentially contain injected content designed to confuse log tooling.

## 11.3 Admin analytics

Dashboards and the decisions they drive:

| Metric | Decision it drives |
|---|---|
| Attempts started / completed / abandoned per activity | Which content works |
| **Drop-off by task** (which task loses learners) | The single best content-improvement signal — rewrite that task's instructions |
| Median & p90 time per task vs. authored estimate | Re-estimate; split over-long tasks |
| Pass rate on first attempt; retry-to-pass rate | Difficulty calibration; mis-labelled levels |
| Measured Elo vs. authored difficulty label | Re-label or redesign |
| Hint usage distribution by level and task | Tasks needing better instructions (heavy level-3 use = unclear task) |
| Validator failure frequency + `ERROR` rate | Flaky or badly-authored validators |
| Reset rate per activity | Environment or instruction problems |
| Mentor conversation topics (clustered) | Missing curriculum content — learners asking about X means the course under-teaches X |
| Weakest skills across cohort | Curriculum gaps; where to author next |
| Skill progression velocity per cohort | Programme effectiveness; instructor intervention |
| Cost per attempt / per activity / per learner | Tier down, retire uneconomic content |
| AI cost per attempt & cache hit rate | Prompt/routing optimisation |
| Learners stuck >X days on a project milestone | Human intervention list for instructors |

**How these improve curriculum, concretely:** if 60% of learners request a level-3 hint on task 4 of a lab, the task is under-specified — that is a content ticket with a clear owner and a measurable outcome. If the cohort's weakest skill is `k8s.observability.events` while the course allocates six minutes of video to it, that is a curriculum ticket. The Practice Engine's most valuable long-term output may be this feedback loop into the courses themselves, not the labs.

## 11.4 Experimentation

Version and A/B everything that is a judgement call: recommendation ranker weights, hint ladder wording, difficulty defaults, idle timeouts, mentor personas. Assignment at the learner level, sticky, logged on every recommendation and attempt.

**North-star metric for the Practice Engine: skill-mastery gain per learner-hour**, i.e. the sum of positive BKT deltas divided by active practice time. It resists the obvious perverse incentives — making labs trivially easy raises completion but not mastery gain; making them impossibly hard raises time but not gain. Guardrail metrics: completion rate, abandonment, cost per attempt, learner satisfaction.

---

# Part XII — Detailed Mode Workflows

## 12.1 Guided Lab — end-to-end sequence

```
LEARNER          PRACTICE CORE        ORCHESTRATOR         ENVIRONMENT       VALIDATOR    AI
   │                    │                    │                    │              │          │
   │ POST /attempts     │                    │                    │              │          │
   ├───────────────────►│                    │                    │              │          │
   │                    │ eligibility+quota  │                    │              │          │
   │                    │ attempt=CREATED    │                    │              │          │
   │                    │ Provision(bp@v4)   │                    │              │          │
   │                    ├───────────────────►│ pool claim (hit)   │              │          │
   │  202 + ws url      │                    ├───────────────────►│              │          │
   │◄───────────────────┤                    │ apply fixtures     │              │          │
   │                    │                    ├───────────────────►│              │          │
   │                    │                    │ health gate ok     │              │          │
   │                    │  env.ready (2.8s)  │◄───────────────────┤              │          │
   │  WS: READY         │◄───────────────────┤                    │              │          │
   │◄───────────────────┤                    │                    │              │          │
   │                    │                    │                    │              │          │
   │ open terminal WS ──┼────────────────────┼──► session broker ─┤ (telemetry tap)│         │
   │ $ docker build …   │                    │                    │              │          │
   │ COMMAND_EXECUTED ─►│ append event       │                    │              │          │
   │                    │                    │                    │              │          │
   │ [Check] task t1    │                    │                    │              │          │
   ├───────────────────►│ enqueue ValidationRun                   │              │          │
   │                    ├────────────────────┼───────────────────┼──────────────►│          │
   │                    │                    │ mint RO creds      │◄──────────────┤          │
   │                    │                    │                    │ run asserts   │          │
   │  WS: t1 PASS (1.2s)│◄───────────────────┼───────────────────┼───────────────┤          │
   │◄───────────────────┤ task state update  │                    │              │          │
   │                    │                    │                    │              │          │
   │ stuck on t3, 6 min │                    │                    │              │          │
   │ "why won't it start"│                   │                    │              │          │
   ├───────────────────►│ mentor msg ────────┼───────────────────┼───────────────┼─────────►│
   │                    │ context: spec+state+validator results   │              │          │
   │  Socratic reply    │◄───────────────────┼───────────────────┼───────────────┼──────────┤
   │◄───────────────────┤ (disclosure ≤ 1)   │                    │              │          │
   │                    │                    │                    │              │          │
   │ [Hint L2] (−3%)    │ HINT_REQUESTED, penalty recorded         │              │          │
   │ … solves t3–t5 …   │                    │                    │              │          │
   │                    │                    │                    │              │          │
   │ [Submit]           │                    │                    │              │          │
   ├───────────────────►│ full validation ───┼───────────────────┼──────────────►│          │
   │                    │ signals → criteria → profile sp.guided-lab.default     │          │
   │                    │ score 0.86, passed │                    │              │          │
   │                    │ BKT update: k8s.deployments .41 → .63   │              │          │
   │  results page      │◄───────────────────┤                    │              │          │
   │◄───────────────────┤ Destroy ──────────►│ snapshot → destroy │              │          │
   │ reference solution │                    │                    │              │          │
   │ + next recommended │                    │                    │              │          │
```

Total elapsed: ~35 min. Platform cost: ~$0.04.

## 12.2 Production Simulation — workflow

```
1. PRE-FLIGHT
   Eligibility requires P(L) ≥ 0.55 on all REQUIRES ancestors of the primary skill.
   If not met → offer the remediation ladder instead of the sim. (Letting an
   unprepared learner into a sim produces frustration, not learning.)

2. PROVISION HEALTHY
   Blueprint bp.microservices-eks.v3 provisioned. Health gate: all services 200 OK,
   3/3 replicas, synthetic traffic passing. THIS MUST PASS before proceeding —
   otherwise a provisioning flake becomes an unsolvable "fault."

3. BASELINE SNAPSHOT
   Capture full resource inventory + health matrix. This is the NO_REGRESSION
   reference and the "what did they break" diff source.

4. INJECT
   Apply fault set at T0. Re-run a *reduced* health check to confirm the fault
   manifests as the authored symptom (readiness failing, 502s observable). If the
   symptom does not manifest, discard the environment and retry — do not hand the
   learner a sim where the bug isn't reproducible.

5. TICKET
   Learner receives INC ticket, access credentials (scoped, read-write only in the
   affected namespace), incomplete runbook, and an SLA clock.

6. INVESTIGATE ── every action captured ──
   Telemetry signals accumulate:
     diagnostic_efficiency — did they read events/logs/describe before acting?
     blast_radius          — did they run destructive commands?
     hypothesis_ordering   — order of areas investigated vs canonical path
   Mentor persona: senior on-call. Asks "what have you observed?" Will interpret a
   log the learner pastes. Will NOT name the misconfigured field.

7. ESCALATION at T+15min
   Traffic spike fires. Pressure is pedagogical: it tests whether the learner
   stabilises first or keeps root-causing. Both are defensible; the incident note
   must justify the choice.

8. REMEDIATE & VERIFY
   Learner fixes. HTTP_SLO validator runs a 180s window under load.
   K8S_ASSERT confirms replica health. NO_REGRESSION diffs against baseline.

9. INCIDENT NOTE
   Required artifact: root cause, detection, timeline, remediation, prevention.
   Graded by AI against rub.incident-note.v2 — with the deterministic facts
   (which fault was actually present, what the learner actually did) supplied to
   the grader, so it can judge whether the stated root cause is CORRECT, not just
   well-written. This is the key move: never ask an LLM "is this root cause right?"
   without telling it what the actual fault was.

10. SCORE
    troubleshooting  .30 ← diagnostic_efficiency + time-to-first-correct-hypothesis
    technical_impl   .30 ← resolution validators
    reliability      .15 ← SLO window + no_regression + blast_radius penalty
    security         .15 ← did the fix introduce a security regression (tfsec/checkov)
    documentation    .10 ← AI rubric on incident note

11. DEBRIEF
    Reveal the fault, the canonical diagnostic path, and a timeline comparing the
    learner's actions to it. This debrief is where most of the learning happens —
    invest in it.
```

## 12.3 Project — workflow

```
WEEK 0  ENROL & KICKOFF
        Requirements pack issued. Learner's Git repo provisioned (platform-hosted).
        Cloud sandbox account claimed but IDLE (no resources, no cost).
        Milestone schedule generated with soft due dates.

MILESTONE 1 — DESIGN (no environment; near-zero cost)
        Deliverable: architecture doc + diagram + tech-choice rationale +
                     cost estimate + risk register.
        Validation: FILE_PARSE (required sections present), AI_RUBRIC
                     (rub.architecture.v3 — appropriateness given the stated
                     constraints, not "is this the textbook answer").
        Gate: must reach level 3/5 on architecture rubric to proceed.
        Mentor: reviewer persona. Challenges: "you chose EKS — what does that
                cost at the stated 50 rps, and what breaks first?"
        WHY GATE HERE: three weeks of implementation on a bad design teaches
        the wrong lesson and burns real cloud budget.

MILESTONE 2 — INFRASTRUCTURE
        Deliverable: Terraform in repo, applied to the sandbox account.
        Validation: IAC_STATE (no drift, remote state, no secrets in state),
                    CLOUD_ASSERT (VPC topology, no public data stores,
                                  encryption at rest, least-privilege IAM),
                    STATIC_ANALYSIS (tfsec/checkov thresholds).
        Environment: T3, provisioned on demand, destroyed on idle. Terraform
        state persists in platform-managed backend, so destroy/
        recreate is cheap and the learner loses nothing.

MILESTONE 3 — IMPLEMENTATION
        Deliverable: services deployed, CI/CD pipeline functioning.
        Validation: TEST_SUITE on the repo, HTTP_PROBE on deployed endpoints,
                    pipeline-triggers-on-commit check (make a commit via API,
                    assert a deployment follows).

MILESTONE 4 — PRODUCTION HARDENING
        Deliverable: observability, autoscaling, security posture, runbook.
        Validation: CHAOS_PROBE (kill a pod / drain a node — service survives),
                    PERF_BENCH (p95 under target load), CLOUD_ASSERT on
                    logging/alerting existence, trivy on images.

MILESTONE 5 — SUBMISSION & DEFENCE
        Deliverable: final repo + README + demo recording.
        Full acceptance suite runs against the live system.
        Resource inventory snapshotted (survives the nuke).
        DEFENCE: 6–8 questions generated from the learner's OWN architecture doc
        and OWN commit history. Examples the generator should produce:
          "Your design doc says you chose X for Y; your implementation
           uses Z instead — walk me through that change."
          "What happens to in-flight requests during your deploy?"
        Scored on reasoning rubric. Human-reviewed for certification.

POST    Environment nuked. Repo retained permanently (portfolio value —
        learners will link this from their CV; that is a retention feature).
        Mastery updated across all mapped skills, weighted by milestone scores.
```

Suspension is the norm, not the exception, for projects. Environments come up on demand, live for hours not weeks, and are destroyed aggressively. All durable state is in Git and Terraform remote state. This is what makes a two-week project cost ~$4.50 instead of ~$400.

---

# Part XIII — Roadmap, Technology, Scalability, Risks

## 13.1 Phased delivery

The governing principle (D12): **each phase ships a complete vertical slice that real learners use.** No phase builds infrastructure that waits for a later phase to become useful.

### Phase 1 — MVP: Guided Labs, one track (≈4 months)

**Scope**

- **One tier: T1** (gVisor pods on one regional cluster). No T0/T2/T3.
- **One course track: DevOps** — Linux, Docker, Kubernetes (via k3s in-pod where the tier allows; otherwise defer multi-node K8s to Phase 2).
- 25–35 guided labs, L1–L3.
- Full evidence pipeline: events, deterministic validators, signals, scoring engine, one scoring profile.
- Skill graph (~80 skills for the track), BKT mastery, mastery display.
- Curriculum mapping to existing courses/modules/topics.
- Attempt lifecycle including suspend/resume and workspace snapshots.
- Terminal (xterm + session broker with telemetry tap) + Monaco editor.
- Authored hint ladders. **No AI mentor** — static hints only.
- Simple recommendation: rules only (next topic, remediate lowest-mastery prerequisite).
- Learner dashboard (progress, mastery, recent attempts) + basic admin analytics.
- Content-as-code repo + content CI (lint, golden path, null path, flake) + minimal CMS (read/preview; authoring in Git).
- Security baseline: gVisor, NetworkPolicy default-deny, egress proxy, quotas, reaper, audit log.
- Cost metering and per-learner daily budget.

**Explicitly out:** cloud accounts, simulations, projects, AI anything, adaptive recommendation, multi-region, OpenVSCode, T0/T2.

**Architecture:** Practice Core monolith + Environment Orchestrator + Evaluation workers. Postgres + Redis + S3 + NATS. One cluster. No ClickHouse (use Postgres read replica for analytics at this volume).

**Complexity:** high (the orchestrator, session broker and validator runner are all novel). **Risks:** environment reliability, content authoring throughput, cold-start latency. **Learner outcome:** can complete a full DevOps hands-on track with real feedback and a defensible skill profile.

**Definition of done:** 200 learners complete ≥3 labs each; provision success ≥99%; time-to-ready p95 ≤20s; validator ERROR rate <0.5%; cost per attempt <$0.08; measured Elo available for every lab.

### Phase 2 — Production Simulations + T2 (≈3 months)

**Scope:** T2 microVM tier (real DinD, k3s, multi-node); fault library (first 30 faults); simulation authoring; process telemetry signals (diagnostic efficiency, blast radius); NO_REGRESSION and SLO validators; incident-note artifact (rubric-graded — first AI grading use, human-reviewed at 100% initially); Elo calibration live; retry/cooldown policy; second course track.

**Dependencies:** Phase 1 evidence pipeline; fault library requires blueprint stability.

**Risks:** simulation flakiness (mitigated by health-gate-before-inject and null-path CI); microVM operational maturity; the first AI grading rubric requires a calibration set that must be built by hand (budget 3–4 weeks of SME time — do not underestimate this).

**Outcome:** learners move from "follow" to "troubleshoot."

### Phase 3 — Projects + T3 cloud sandboxes (≈4 months)

**Scope:** AWS account vending, SCP framework, credential brokering, nuke + verification, account pool manager, budget enforcement chain; project mode with milestones, platform Git hosting, long-lived suspension; acceptance test suites, chaos and perf probes; architecture rubrics + defence viva; ClickHouse analytics; full admin cost dashboard; OpenVSCode editor.

**Dependencies:** rock-solid Phase 1–2 teardown discipline. **Do not build T3 until orphan-environment count has been zero for a sustained period** — the same bug that leaks a pod leaks a NAT gateway.

**Risks:** highest cost risk in the programme; account quota lead times; nuke completeness. Mitigate with a hard per-account budget, an independent cloud-native alarm, a nightly sweeper, and a launch cap on concurrent T3 attempts.

**Outcome:** learners produce portfolio-grade, defensible work in real cloud.

### Phase 4 — AI Mentor + Adaptive Engine (≈3 months)

**Scope:** LLM Gateway (routing, caching, budgets, prompt versioning); Mentor Service with policy engine, personas, disclosure ceilings, guardrails and adversarial CI; feature store and the full four-stage recommender with explanations; spaced repetition; auto-generated remediation ladders; A/B framework; AI-assisted content authoring (internal).

**Why this late:** the mentor is only as good as the environment-state context it can retrieve, and the recommender is only as good as the attempt data it learns from. Both need Phases 1–3 to exist. Shipping a mentor first would produce a generic chatbot with no situational awareness — the thing learners already have elsewhere.

**Risks:** solution leakage, cost, over-reliance harming learning outcomes. Mitigate with the IAM boundary on solutions, budgets, and an explicit experiment measuring mastery gain with vs. without mentor access.

**Outcome:** personalised progression; measurable reduction in abandonment.

### Phase 5 — Scale, multi-cloud, enterprise (≈4 months)

**Scope:** Azure and GCP sandbox tiers; T0 browser tier for data/AI tracks; multi-region practice clusters; enterprise multi-tenancy (dedicated node pools, per-tenant KMS, SSO/SCIM, custom content); certification pipeline with proctoring; cohort management for instructors; public API and LMS/LTI integration; composable simulation generation (blueprint × fault selection driven by weak skills).

## 13.2 Technology recommendations

| Layer | Recommendation | Why | Alternatives rejected |
|---|---|---|---|
| Frontend | Next.js + TypeScript + TanStack Query + xterm.js + Monaco | Mature, one language across FE/BE, best-in-class terminal | SvelteKit (smaller ecosystem for this), Angular (heavier) |
| Practice Core | TypeScript (NestJS) **or** Go — pick one and commit | TS shares types and people with the frontend; Go is better for the orchestrator anyway, so a Go core reduces language count. Team skill should decide. | Java/Spring (heavier ops), Rails/Django (weaker for streaming + concurrency) |
| Environment Orchestrator | **Go** | Best K8s client ecosystem, controller-runtime patterns, cloud SDKs, concurrency for thousands of state machines | Python (GIL + weaker K8s tooling), Rust (slower to build, thinner cloud SDK coverage) |
| Evaluation & AI | **Python** | Analysis libraries, LLM tooling, static-analysis integrations | Node (adequate; loses the ML ecosystem) |
| Primary DB | **PostgreSQL 16+** (managed: RDS/Cloud SQL) | Relational + JSONB + partitioning + RLS + recursive CTEs covers everything needed | MongoDB (no relational integrity where it matters), Neo4j (graph too small to justify) |
| Cache / coordination | **Redis** (managed) | Locks, warm-pool CAS, rate limits, pub/sub | Memcached (no data structures) |
| Event bus | **NATS JetStream** (Phase 1–2) → **Kafka** if volume demands | NATS is dramatically simpler to operate at this scale; migrate only on evidence | Kafka from day one (operational tax too early), SQS/SNS (cloud lock-in, weaker semantics) |
| Analytics | **ClickHouse** (Phase 3+) | Event-analytics workload, cheap at volume | Snowflake/BigQuery (fine, higher cost, more lock-in), Postgres alone (breaks by ~10M events/day) |
| Object storage | S3/GCS with lifecycle policies + zstd | Snapshots, recordings, artifacts | — |
| Container sandbox | **gVisor** (T1), **Kata/Firecracker** (T2) | Escape mitigation at acceptable cost; hardware isolation where privileges are needed | runc alone (unacceptable risk), full VMs everywhere (cost) |
| Orchestration | **Kubernetes** (EKS/GKE), one cluster per region-shard | Namespace/quota/netpol primitives are exactly the isolation model needed | Nomad (smaller ecosystem), raw VMs (loses density and primitives) |
| IaC | **Terraform** for platform + account baselines; Helm/Kustomize for workloads; ArgoCD for GitOps | Standard, and matches what learners are taught | Pulumi (fine; smaller talent pool), CDK (cloud-specific) |
| Secrets | **Vault** or cloud KMS + workload identity | Dynamic short-lived credentials are the core requirement | Static secrets in K8s (unacceptable) |
| Observability | OpenTelemetry → Prometheus + Loki + Tempo + Grafana | Vendor-neutral, correlation by attempt_id | Datadog (excellent, expensive at this telemetry volume — revisit) |
| CI/CD | GitHub Actions + ArgoCD; dedicated content-CI runners with cluster access | Content CI needs real environments; keep it isolated from app CI | — |

## 13.3 Scalability considerations

| Dimension | Bottleneck | Mitigation |
|---|---|---|
| Concurrent environments | K8s API server + etcd under namespace churn | Shard clusters at ~2,000 concurrent envs; keep namespace objects minimal; batch deletes; monitor etcd object count and API p99 as SLIs |
| Environment start latency | Image pull, fixture apply | Regional pull-through cache, node-level pre-pull DaemonSet, warm pools keyed by blueprint version, fixtures baked into images where stable |
| Terminal throughput | WS gateway fan-out, PTY buffering | Stateless gateway with sticky routing to the broker; backpressure; drop-and-mark on overflow rather than unbounded buffering |
| Validation burst | "Everyone submits before the deadline" | Queue-based with autoscaled workers; per-attempt serialisation; priority lanes (interactive checks > submission grading > re-scores) |
| Event volume | `attempt_events` write rate and table size | Monthly partitions, BRIN on time, async batched writes, archive to Parquet, avoid whole-document GIN indexes |
| Dashboard queries | Aggregations over attempts | Materialised views + ClickHouse; never aggregate the OLTP tables live |
| Cloud accounts | Provider quota, creation latency | Pre-warmed pool sized to peak × 2, quota increases requested ahead of cohort intakes, multi-region pools |
| AI cost & rate limits | Provider throughput | Gateway with caching, cheap-model routing, budgets, multi-provider failover, graceful degradation to static hints |
| Content authoring | Human throughput (the real limit) | Composable blueprints/fixtures/faults, AI-assisted drafting, CI that makes publishing safe enough to be fast |
| Team cognitive load | Too many moving parts | Modular monolith, one orchestrator interface for four tiers, phase discipline |

## 13.4 Major technical risks and mitigations

| # | Risk | Likelihood | Impact | Mitigation | Early warning signal |
|---|---|---|---|---|---|
| R1 | **Uncontrolled cloud cost** — orphaned resources, runaway environments | High | Severe | Reaper with hard deadlines; nuke + verification; independent cloud budget alarms; per-scope hard stops; nightly sweeper; T3 gated behind proven zero-orphan operation | Orphan count > 0; hourly spend > 1.5× forecast |
| R2 | **Sandbox escape / cross-tenant breach** | Low | Existential | gVisor/Kata; no SA tokens; default-deny egress; separate control-plane network; PSS restricted; regular pen-testing and a bug bounty; kernel patch SLA | Unexpected syscall denials; anomalous egress; IDS alerts |
| R3 | **Flaky labs destroy trust** | High | Severe | Golden-path + null-path + 5× flake CI; health gates; validator retry with backoff; ERROR results never penalise learners; per-activity flakiness dashboard with auto-unpublish above threshold | Validator ERROR rate; reset rate; support tickets |
| R4 | **Content supply cannot fill the engine** | High | Severe | Composable primitives; AI-assisted authoring; CMS for SMEs; measure and target activities-published-per-week as a tracked metric from Phase 1 | Publish rate below plan for two consecutive sprints |
| R5 | **AI grading is unfair or gameable** | Medium | Severe (trust) | Deterministic-first; AI capped at 40% of score; rubric calibration with kappa thresholds; multi-sample agreement; human review for certification; injection classifier | Kappa drift; appeal rate; grade distribution shift after model change |
| R6 | **Cold-start latency kills engagement** | Medium | High | Warm pools with demand prediction; image pre-pull; T0 tier for instant-start content; honest progress UI with what's happening | Time-to-ready p95; start→first-command abandonment |
| R7 | **Platform used for abuse (mining, spam, attacks)** | Medium | Severe (legal/reputation) | Egress allowlist; SCP denies; CPU anomaly detection; egress volume caps; KYC on higher tiers; rapid suspend runbook | CPU-without-input; egress spikes; provider abuse notices |
| R8 | **State loss — learner loses hours of work** | Medium | High | Auto-commit git in workspace; 5-min snapshots; snapshot on preemption notice; snapshot before any destroy; restore tested in CI | Snapshot failure rate; restore failure rate |
| R9 | **etcd/K8s API saturation from namespace churn** | Medium | High | Cluster sharding; minimal namespace objects; delete batching; capacity headroom; load test namespace churn at 3× projected peak before Phase 3 | API p99; etcd DB size; delete latency |
| R10 | **Over-engineering delays launch** | Medium | High | Phase discipline; MVP is one tier and one track; explicit "not now" list per phase; modular monolith over microservices | Phase 1 slipping past 5 months |
| R11 | **Mastery model produces nonsense** | Medium | Medium | Start with conservative BKT parameters; validate against instructor judgement on a sample cohort; display bands with evidence, never raw numbers; keep the evidence log so the model can be replaced without data loss | Instructor disagreement rate; mastery vs. project-outcome correlation |
| R12 | **Multi-cloud doubles operational surface** | Medium | Medium | AWS-only through Phase 4; abstract at the orchestrator driver interface, not deeper; add Azure/GCP only against demonstrated customer demand | Support burden per cloud |
| R13 | **Cheating undermines credential value** | Medium | Medium | Integrity signals + review queue; viva for certification; proctoring option; personalised project variants (parameterised requirements per learner) | Duplicate workspace hashes; impossible timings |

## 13.5 What to get right in the first month

If only five things are built correctly at the start, make them these:

49. **The event model and `attempt_id` correlation.** Everything downstream — scoring, re-scoring, analytics, debugging, appeals — depends on it and none of it can be retrofitted cheaply.
50. **The Environment Orchestrator interface and the reaper.** The abstraction over tiers and the guarantee that nothing outlives its deadline. The reaper is what stops a cost incident from becoming a company incident.
51. **Content CI with golden-path and null-path tests.** This is what keeps quality from decaying as content volume grows, and it is nearly impossible to add retroactively to 300 existing labs.
52. **The telemetry tap in the session broker.** Server-side, authoritative command capture. Every process-based signal, every integrity check and every simulation score depends on it existing from day one.
53. **The skill graph and its ownership.** Small, curated, reviewed. A sprawling untended taxonomy is the quiet way this platform becomes unusable in year two.

---

# Appendix A — Decision summary (What → Why → How → Trade-off)

| Decision | What | Why | How | Trade-off |
|---|---|---|---|---|
| Tiered execution | 4 tiers T0–T3 | 100× cost spread across capability needs | Blueprint declares capabilities; orchestrator selects cheapest qualifying tier | 4 driver implementations to build and operate |
| Unified activity runtime | One runtime, 5 configuration axes | 3 engines would triple validator/telemetry/tooling work | Guidance, complexity, faults, validation granularity, AI persona as parameters | More abstraction before the first lab ships |
| Deterministic-first validation | Typed validators; AI only for open-ended | Fairness, reproducibility, cost, injection resistance | Validator catalogue + out-of-band runner with read-only creds | Authoring validators is real work; limits what can be assessed |
| Event-sourced attempts | Append-only events; scores derived | Re-scoring, audit, appeals, analytics | Partitioned `attempt_events` + materialised task state + replay tool | Storage volume; replay discipline required |
| Content-as-code + CI | Git-authored specs, CI-gated publish | Content quality is the long-run bottleneck | Lint → provision → golden path → null path → flake → cost → AI safety | Content CI consumes real infrastructure budget |
| Two graphs | Curriculum tree ≠ skill DAG | 9 overlapping courses; prerequisites are epistemic, not commercial | `topic_skill` + `activity_skill` joins; materialised closure | Mapping and governance burden |
| BKT mastery + Elo difficulty | Probabilistic mastery, self-calibrating difficulty | Completion % cannot express uncertainty or decay | Evidence-weighted Bayesian update; per-domain Elo | Parameter tuning; opacity (mitigated by bands + evidence) |
| Modular monolith + 3 services | Core monolith; orchestrator/eval/AI separate | Shared transactions in core; genuinely different profiles for the three | gRPC + queues + events; enforced module boundaries in CI | Requires discipline or it rots |
| Cattle environments, pet workspaces | Destroy compute aggressively; persist work | Compute is the cost; work is the value | Git auto-commit + snapshots + IaC remote state | Restore path must be flawless and tested |
| Zero standing cloud credentials | Vended accounts + brokered STS + SCP + nuke | IAM inside an account is breakable; SCPs are not | Account pool manager, OIDC federation, nuke + verify | Account quotas, pool warming, operational commitment |
| Solution isolated from mentor | IAM boundary, not a prompt rule | Prompt instructions do not hold under adversarial pressure | Separate bucket/index/service identity | Mentor is less capable in Lab/Sim modes — intentionally |
| Cost as architecture | Budgets, meters, kill switches, unit economics | Thousands of learners × real cloud is unbounded otherwise | Per-attempt metering, 4-level budget chain, reaper, tiering | Constant tuning; some learner friction from TTLs and quotas |
