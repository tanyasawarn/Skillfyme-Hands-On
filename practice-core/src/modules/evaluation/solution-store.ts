import { Inject, Injectable, Logger } from '@nestjs/common';

/**
 * PLAN.md G3 / doc §7.4 "Structural enforcement": "solutions live in a
 * separate storage bucket and a separate retrieval index with a distinct
 * service identity. The Mentor Service's credentials cannot read it. The
 * Grader Service's can. This is an IAM boundary, not a prompt
 * instruction -- the only kind of guardrail that actually holds."
 *
 * In this codebase (no real S3/IAM) the boundary is enforced structurally:
 *
 *   1. This file lives INSIDE the evaluation module and is NOT on the
 *      module's public seam (eslint.boundaries.mjs) -- so nothing outside
 *      evaluation/ can even import it. The Mentor module physically
 *      cannot reference a solution.
 *   2. Reads require a GRADER_IDENTITY token. The token is provided ONLY
 *      by EvaluationModule. A caller that somehow got a SolutionStore
 *      instance without the grader identity gets an error, not the text.
 *   3. `assertMentorCannotReach()` is a runtime self-check used by the
 *      boundary test.
 *
 * A production deployment replaces the in-process store with the real
 * grader-scoped bucket/index client; the seam (fetch by ref, identity
 * required) is unchanged.
 */

export const GRADER_IDENTITY = Symbol('GRADER_IDENTITY');

/** Opaque proof that the caller is the grader path. Constructed only by
 *  EvaluationModule's provider factory. */
export interface GraderIdentity {
  readonly kind: 'grader';
  readonly issuedAt: number;
}

export function issueGraderIdentity(): GraderIdentity {
  return { kind: 'grader', issuedAt: Date.now() };
}

export class MentorBoundaryViolationError extends Error {
  constructor() {
    super(
      'SolutionStore accessed without the grader identity -- the Mentor ' +
        'Service must never reach reference-solution content (doc §7.4).',
    );
    this.name = 'MentorBoundaryViolationError';
  }
}

@Injectable()
export class SolutionStore {
  private readonly logger = new Logger(SolutionStore.name);

  constructor(
    @Inject(GRADER_IDENTITY) private readonly identity: GraderIdentity | null,
  ) {}

  /**
   * Fetch the reference solution / solution_apply text for an activity
   * version. Requires the grader identity. The Mentor path has no way to
   * obtain one, and cannot import this file.
   */
  async fetch(
    ref: {
      activityVersionId: string;
      kind: 'reference_solution' | 'solution_apply';
    },
    identity: GraderIdentity,
  ): Promise<string> {
    if (identity?.kind !== 'grader' || this.identity?.kind !== 'grader') {
      this.logger.error('SolutionStore.fetch denied: not the grader identity');
      throw new MentorBoundaryViolationError();
    }
    // Placeholder: a real store reads the grader-scoped bucket/index.
    // Returning a marker keeps the seam exercised without shipping
    // solution content into this repo's runtime.
    return `<<reference-solution:${ref.kind}:${ref.activityVersionId}>>`;
  }

  /** Used by the boundary test to prove a non-grader caller is refused. */
  assertMentorCannotReach(): void {
    if (this.identity?.kind === 'grader') return;
    throw new MentorBoundaryViolationError();
  }
}
