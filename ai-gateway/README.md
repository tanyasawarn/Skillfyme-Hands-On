# ai-gateway/

PLAN.md's Phase 0 repo layout lists this as a top-level directory for the AI Gateway service
(D6's third extracted service: multi-provider LLM routing, budget circuit breaker, caching).

This is genuinely not built yet — unlike `evaluation/` (see that directory's README), there is
no working AI Gateway logic living elsewhere in this repo to point to. The current state:

- `practice-core/src/modules/evaluation/fake-ai-grader.service.ts` stands in for the AI Gateway
  for the one AI-graded artifact type that exists so far (`rub.incident-note.v2`) — a
  deterministic, no-real-LLM-call fake, explicitly scoped as a placeholder in its own doc
  comment, matching PLAN.md's Phase 2 note that this is "human-reviewed at 100% initially."
- Real AI Gateway work (multi-provider failover, budget circuit breaker, caching layer,
  environment-state summary API for the Mentor Service, IAM boundary so the Mentor can't read
  `reference_solution`) is Phase 4 scope per PLAN.md's own phasing, not Phase 0-2.

This directory stays empty until that Phase 4 work begins — populating it now with a partial
LLM client wired only to the incident-note grader would misrepresent how much of the AI Gateway
actually exists relative to PLAN.md's own scope for it.
