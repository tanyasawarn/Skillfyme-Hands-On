import { RubricRepository } from './rubric.repository';

describe('RubricRepository (reads content/rubrics/*.yaml directly -- see class doc comment)', () => {
  const repo = new RubricRepository();

  it('reads the real rub.incident-note.v2 rubric with its 3 criteria and human review policy', () => {
    const rubric = repo.getRubric('rub.incident-note.v2');
    expect(rubric).not.toBeNull();
    expect(rubric!.id).toBe('rub.incident-note.v2');
    expect(rubric!.artifactType).toBe('MARKDOWN');
    expect(rubric!.criteria.map((c) => c.key)).toEqual([
      'root_cause_accuracy',
      'completeness',
      'prevention_quality',
    ]);
    expect(rubric!.humanReviewPolicy).toBe(
      'ALWAYS_PROVISIONAL_UNTIL_CALIBRATED',
    );
  });

  it('each criterion carries 4 levels with descriptors', () => {
    const rubric = repo.getRubric('rub.incident-note.v2')!;
    for (const criterion of rubric.criteria) {
      expect(criterion.levels).toHaveLength(4);
      for (const level of criterion.levels) {
        expect(typeof level.descriptor).toBe('string');
        expect(level.descriptor.length).toBeGreaterThan(0);
      }
    }
  });

  it('root_cause_accuracy carries exemplars', () => {
    const rubric = repo.getRubric('rub.incident-note.v2')!;
    const rootCause = rubric.criteria.find(
      (c) => c.key === 'root_cause_accuracy',
    );
    expect(rootCause!.exemplars!.length).toBe(2);
  });

  it('returns null for a rubric id that does not exist', () => {
    expect(repo.getRubric('rub.does-not-exist')).toBeNull();
  });
});
