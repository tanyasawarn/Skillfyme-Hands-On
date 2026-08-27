import { describe, it, expect } from 'vitest';
import { COURSES, courseLabel } from './courses';

describe('courses', () => {
  it('COURSES matches the previously-triplicated course list (PLAN.md K4)', () => {
    expect(COURSES).toEqual([
      { slug: 'devops-with-ai', label: 'DevOps With AI' },
      { slug: 'genai-with-ml', label: 'Generative AI With ML' },
    ]);
  });

  it('courseLabel returns the known label for a real course slug', () => {
    expect(courseLabel('devops-with-ai')).toBe('DevOps With AI');
    expect(courseLabel('genai-with-ml')).toBe('Generative AI With ML');
  });

  it('courseLabel falls back to the raw slug for an unknown course', () => {
    expect(courseLabel('not-a-real-course')).toBe('not-a-real-course');
  });
});
