import { FakeAiGrader } from './fake-ai-grader.service';
import { RubricRepository } from './rubric.repository';
import type { GradingFacts } from './ai-grader.interface';

describe('FakeAiGrader (stub grader -- see class doc comment for why this must never look confident)', () => {
  const grader = new FakeAiGrader();
  const rubric = new RubricRepository().getRubric('rub.incident-note.v2')!;

  const facts: GradingFacts = {
    artifactText: '# Incident note\n\nRoot cause: ...',
    appliedFaultIds: ['f.k8s.memory-limit-too-low'],
    resolutionValidatorResults: [{ validatorId: 'v1', status: 'PASS' }],
    commandSequence: ['kubectl get pods'],
  };

  it('grades every rubric criterion at confidence 0 with the STUB_GRADER_NO_REAL_INFERENCE flag', async () => {
    const result = await grader.grade(rubric, facts);
    expect(result.criterionGrades).toHaveLength(rubric.criteria.length);
    for (const grade of result.criterionGrades) {
      expect(grade.confidence).toBe(0);
      expect(grade.flags).toContain('STUB_GRADER_NO_REAL_INFERENCE');
      expect(grade.evidenceQuotes).toEqual([]);
    }
  });

  it('always returns provisional: true', async () => {
    const result = await grader.grade(rubric, facts);
    expect(result.provisional).toBe(true);
    expect(result.provisionalReason).toMatch(/FakeAiGrader stub/);
  });

  it('picks the midpoint level of each criterion deterministically', async () => {
    const result = await grader.grade(rubric, facts);
    // Every criterion in rub.incident-note.v2 has levels [1,2,3,4] -> midpoint index floor(3/2)=1 -> level 2.
    for (const grade of result.criterionGrades) {
      expect(grade.level).toBe(2);
    }
  });

  it('echoes the rubric id back on the result', async () => {
    const result = await grader.grade(rubric, facts);
    expect(result.rubricId).toBe('rub.incident-note.v2');
  });

  it('returns the same grades across repeated calls (deterministic, not random)', async () => {
    const first = await grader.grade(rubric, facts);
    const second = await grader.grade(rubric, facts);
    expect(second.criterionGrades).toEqual(first.criterionGrades);
  });
});
