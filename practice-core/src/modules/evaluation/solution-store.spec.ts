import {
  MentorBoundaryViolationError,
  SolutionStore,
  issueGraderIdentity,
} from './solution-store';

/**
 * PLAN.md G3 / doc §7.4 -- the Mentor Service's credentials cannot read
 * reference solutions; the Grader's can. Here the boundary is
 * structural: SolutionStore lives inside evaluation/ (not on the module
 * seam, so mentor/ + llm-gateway/ cannot import it -- enforced by
 * eslint.boundaries.mjs) AND reads require the grader identity token
 * that only EvaluationModule provides.
 */
describe('SolutionStore IAM boundary (G3)', () => {
  it('the grader identity can fetch a reference solution', async () => {
    const id = issueGraderIdentity();
    const store = new SolutionStore(id);
    const text = await store.fetch(
      { activityVersionId: 'av-1', kind: 'reference_solution' },
      id,
    );
    expect(text).toContain('reference-solution');
  });

  it('a store constructed WITHOUT the grader identity refuses every fetch', async () => {
    const store = new SolutionStore(null); // the shape a non-grader caller would get
    await expect(
      store.fetch(
        { activityVersionId: 'av-1', kind: 'reference_solution' },
        issueGraderIdentity(),
      ),
    ).rejects.toBeInstanceOf(MentorBoundaryViolationError);
  });

  it('a grader-scoped store still refuses a caller passing a non-grader identity', async () => {
    const store = new SolutionStore(issueGraderIdentity());
    await expect(
      store.fetch({ activityVersionId: 'av-1', kind: 'solution_apply' }, {
        kind: 'not-grader',
      } as unknown as ReturnType<typeof issueGraderIdentity>),
    ).rejects.toBeInstanceOf(MentorBoundaryViolationError);
  });

  it('assertMentorCannotReach throws for a non-grader store', () => {
    expect(() => new SolutionStore(null).assertMentorCannotReach()).toThrow(
      MentorBoundaryViolationError,
    );
    expect(() =>
      new SolutionStore(issueGraderIdentity()).assertMentorCannotReach(),
    ).not.toThrow();
  });
});
