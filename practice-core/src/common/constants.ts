/**
 * PLAN.md Phase 4's K11: assorted magic-number consolidation. Each
 * constant here was independently duplicated as a bare literal at 2+
 * real call sites, confirmed via grep before extraction (not every
 * matching literal value in the codebase is the same concept -- e.g.
 * `bkt.service.ts`'s mastery-band boundary and `scoring-profile.ts`'s
 * scoring weight both happen to also be `0.55`, but are unrelated
 * concepts left as their own local literals, not folded in here).
 */
export const MasteryConstants = {
  /**
   * Doc §2.5 stage 2: "REQUIRES-prereq mastery < 0.55" blocks the
   * activity. Duplicated as a bare `0.55` in 3 real places before this:
   * the actual eligibility gate check (`mastery.service.ts`'s
   * `meetsRequiresGate`), and 2 uses of the identical threshold in
   * `recommendation.service.ts` (filtering to "struggling" skills, and
   * the urgency-score formula that treats 0.55 as the boundary of
   * "not yet met").
   */
  REQUIRES_GATE_THRESHOLD: 0.55,
} as const;

export const GrpcClientConstants = {
  /**
   * `base-grpc-client.ts`'s `call()` default deadline, and
   * `grpc-orchestrator.client.ts`'s `execShell` request (which adds a
   * 5s buffer on top of whatever the caller's own `timeoutMs` is, or
   * this default when none is given) both independently hardcoded the
   * same 30s value.
   */
  DEFAULT_DEADLINE_MS: 30_000,
} as const;

export const TimeoutConstants = {
  /**
   * `workspace-file.service.ts`'s 3 shell-exec calls (list directory,
   * read file, write file) all independently hardcoded the same 10s
   * timeout for what's meant to be a single filesystem operation, not a
   * long-running command.
   */
  WORKSPACE_FILE_OP_MS: 10_000,
} as const;

export const EligibilityConstants = {
  /**
   * PLAN.md K12: doc §4.4 "one active environment per learner by
   * default." Previously two independent literals in
   * `eligibility.service.ts` -- the comparison (`>= 1`) and the
   * user-facing message ("1 active environment per learner") -- with no
   * shared source, internally consistent only by coincidence (this is
   * the exact class of bug PLAN.md's original audit flagged: a message
   * and its enforced value can silently drift the moment either one is
   * edited alone without the other).
   */
  MAX_CONCURRENT_ENVIRONMENTS_PER_LEARNER: 1,
} as const;

export const CacheSweepConstants = {
  /**
   * Revised lifecycle requirement §3/§9's SECOND stage only: how long an
   * attempt can sit SUSPENDED (already zero-cost, environment already
   * torn down) before moving to CACHED for history/cleanup purposes. Not
   * the 15-minute active->suspended threshold -- that one lives in the
   * Go orchestrator (orchestrator/internal/orchestrator/server.go's
   * idleTimeout / orchestrator/internal/idledetect/detector.go) and
   * already runs today, independent of this constant.
   */
  INACTIVITY_THRESHOLD_MS: 72 * 60 * 60 * 1000,
  /** How often CacheSweepService scans SUSPENDED attempts past the threshold above -- mirrors the Go reaper's 60s poll (orchestrator/internal/reaper/reaper.go), scaled up since this window is measured in days, not minutes. Unrelated to the 15-minute idle timeout despite the coincidentally equal value here. */
  SWEEP_INTERVAL_MS: 15 * 60 * 1000,
} as const;
