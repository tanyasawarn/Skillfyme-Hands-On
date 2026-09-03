import { ADVERSARIAL_CASES, runAdversarialSuite } from './adversarial-suite';
import {
  ACTIVE_PROMPT_ID,
  activePrompt,
  getPrompt,
  listPrompts,
} from './prompt-registry';

/**
 * PLAN.md G7 / doc §3.5 step 8, §7.7. The structural half of the
 * adversarial suite: every "if the model complied" leaky output is run
 * through the output guardrail and must NOT survive. The other half
 * (does a real model resist the injection INPUTS) is the content-CI AI
 * DRY-RUN, which needs the Mentor Service (G4) + a live model.
 */
describe('mentor adversarial suite (G7)', () => {
  it('every leaky output is caught by the guardrail (no solution leakage)', () => {
    const results = runAdversarialSuite();
    expect(results).toHaveLength(ADVERSARIAL_CASES.length);
    const leaks = results.filter((r) => r.leaked);
    expect(leaks).toEqual([]); // no case leaks
  });

  it('every case produces at least one guardrail violation', () => {
    for (const r of runAdversarialSuite()) {
      expect(r.violations.length).toBeGreaterThan(0);
    }
  });

  it('the roleplay-solution case is flagged as an unrecoverable snippet leak', () => {
    const r = runAdversarialSuite().find((x) => x.id === 'roleplay-solution')!;
    expect(r.violations).toContain('SOLUTION_SNIPPET_LEAK');
  });

  it('the name-the-resource case is caught as NAMES_BROKEN_RESOURCE', () => {
    const r = runAdversarialSuite().find((x) => x.id === 'name-the-resource')!;
    expect(r.violations).toContain('NAMES_BROKEN_RESOURCE');
  });
});

describe('prompt registry (G7)', () => {
  it('has an active prompt and it resolves', () => {
    expect(activePrompt().id).toBe(ACTIVE_PROMPT_ID);
    expect(activePrompt().status).toBe('active');
  });

  it('the active system prompt states untrusted-content and no-solution rules', () => {
    const p = activePrompt().systemPrompt;
    expect(p).toMatch(/untrusted-learner-content/);
    expect(p).toMatch(/do NOT have the reference solution/i);
    expect(p).toMatch(/disclosure ceiling/i);
  });

  it('getPrompt throws on an unknown id', () => {
    expect(() => getPrompt('mentor.system.v999')).toThrow(/unknown prompt/);
  });

  it('listPrompts returns the registry', () => {
    expect(listPrompts().map((p) => p.id)).toContain(ACTIVE_PROMPT_ID);
  });
});
