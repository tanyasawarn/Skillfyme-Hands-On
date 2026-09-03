import { FakeVivaModel } from './viva-model.port';

describe('FakeVivaModel (Phase 3 3.8 — deterministic grounded questions)', () => {
  const model = new FakeVivaModel();

  it('every question cites a specific commit sha or design section', async () => {
    const qs = await model.generateQuestions({
      designDoc:
        '# Design\n\n## Context and constraints\n...\n\n## Component choices\n...\n\n## Failure modes\n...',
      commits: [
        { sha: 'a'.repeat(40), message: 'add RDS instance' },
        { sha: 'b'.repeat(40), message: 'switch to Fargate' },
      ],
      count: 7,
    });
    expect(qs).toHaveLength(7);
    for (const q of qs) {
      expect(q.groundedIn.length).toBeGreaterThan(0);
      const isSha =
        /^[0-9a-f]{40}$/.test(q.groundedIn) || q.groundedIn === 'HEAD';
      const isSection = q.groundedIn.startsWith('section:');
      expect(isSha || isSection).toBe(true);
    }
  });

  it('prioritises real commits, then design sections, then a grounded fallback', async () => {
    const qs = await model.generateQuestions({
      designDoc: '# Design\n\n## Data model\n...',
      commits: [{ sha: 'c'.repeat(40), message: 'initial' }],
      count: 4,
    });
    expect(qs[0].groundedIn).toBe('c'.repeat(40)); // the commit first
    expect(qs.some((q) => q.groundedIn.startsWith('section:'))).toBe(true);
    expect(qs).toHaveLength(4);
  });

  it('handles an empty repo without crashing', async () => {
    const qs = await model.generateQuestions({
      designDoc: '',
      commits: [],
      count: 6,
    });
    expect(qs).toHaveLength(6);
    expect(qs.every((q) => q.groundedIn === 'HEAD')).toBe(true);
  });
});
