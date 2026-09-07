import { ConfigService } from '@nestjs/config';
import { ClaudeAiGrader } from './claude-ai-grader.service';
import { RubricRepository } from './rubric.repository';
import type { GradingFacts } from './ai-grader.interface';
import {
  AnthropicToolCaller,
  GroqToolCaller,
  createLlmToolCaller,
  type LlmToolCaller,
  type LlmToolCallRequest,
} from './llm-tool-call.port';

const rubric = new RubricRepository().getRubric('rub.incident-note.v2')!;

const facts: GradingFacts = {
  artifactText: '# Incident note\n\nRoot cause: memory limit too low.',
  appliedFaultIds: ['f.k8s.memory-limit-too-low'],
  resolutionValidatorResults: [{ validatorId: 'v1', status: 'PASS' }],
  commandSequence: ['kubectl get pods', 'kubectl describe pod checkout-abc'],
};

/** Builds the schema-shaped tool arguments a caller would return. */
function toolArgs(grades: Array<{ criterion: string; level: number }>) {
  return {
    grades: rubric.criteria.map((c, i) => {
      const g = grades.find((x) => x.criterion === c.key) ?? grades[i];
      return {
        criterion: c.key,
        level: g?.level ?? c.levels[0].level,
        confidence: 0.9,
        evidenceQuotes: ['some quoted evidence'],
        justification: 'because the rubric level descriptor matches',
        flags: [],
      };
    }),
  };
}

/** A fake LlmToolCaller that records requests and returns a canned result. */
function fakeCaller(
  impl: (req: LlmToolCallRequest) => unknown,
): LlmToolCaller & { calls: LlmToolCallRequest[] } {
  const calls: LlmToolCallRequest[] = [];
  return {
    calls,
    async callTool(req: LlmToolCallRequest) {
      calls.push(req);
      return impl(req);
    },
  };
}

function config(vars: Record<string, string | undefined>): ConfigService {
  return {
    get: (key: string) => vars[key],
  } as unknown as ConfigService;
}

describe('ClaudeAiGrader', () => {
  describe('constructor / DI safety', () => {
    it('does not throw when no provider is configured (NestJS eagerly instantiates every provider regardless of which the AI_GRADER factory selects)', () => {
      expect(() => new ClaudeAiGrader(null)).not.toThrow();
    });

    it('grade() throws a clear DI-wiring error if called without a provider configured', async () => {
      const grader = new ClaudeAiGrader(null);
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /DI wiring bug/,
      );
    });

    it('clamps the sample count into [1,5]', async () => {
      const caller = fakeCaller(() => toolArgs([]));
      const grader = new ClaudeAiGrader(caller).withSampleCount(99);
      await grader.grade(rubric, facts);
      expect(caller.calls).toHaveLength(5);
    });
  });

  describe('grade() with a fake LlmToolCaller', () => {
    it('grades every rubric criterion and marks the result provisional', async () => {
      const grader = new ClaudeAiGrader(fakeCaller(() => toolArgs([])));
      const result = await grader.grade(rubric, facts);

      expect(result.rubricId).toBe('rub.incident-note.v2');
      expect(result.criterionGrades).toHaveLength(rubric.criteria.length);
      expect(result.provisional).toBe(true);
    });

    it('makes exactly SAMPLE_COUNT (3) calls per grade() invocation (doc rule 33: multi-sample)', async () => {
      const caller = fakeCaller(() => toolArgs([]));
      const grader = new ClaudeAiGrader(caller);
      await grader.grade(rubric, facts);
      expect(caller.calls).toHaveLength(3);
    });

    it('forces a single submit_grades tool (doc rule 32: structured output only, not free text)', async () => {
      const caller = fakeCaller(() => toolArgs([]));
      const grader = new ClaudeAiGrader(caller);
      await grader.grade(rubric, facts);
      expect(caller.calls[0].tool.name).toBe('submit_grades');
      expect(caller.calls[0].cacheable).toBe(true);
    });

    it('includes the learner artifact inside a delimited block, never as a raw system-prompt substitution (doc rule 35: prompt-injection defence)', async () => {
      const caller = fakeCaller(() => toolArgs([]));
      const grader = new ClaudeAiGrader(caller);
      await grader.grade(rubric, facts);
      const { system, user } = caller.calls[0];
      expect(user).toContain('<learner_artifact>');
      expect(user).toContain('</learner_artifact>');
      expect(user).toContain(facts.artifactText);
      expect(system).not.toContain(facts.artifactText);
    });

    it('never passes a live environment handle or raw telemetry -- only the precomputed GradingFacts fields (doc rule 34)', async () => {
      const caller = fakeCaller(() => toolArgs([]));
      const grader = new ClaudeAiGrader(caller);
      await grader.grade(rubric, facts);
      const { user } = caller.calls[0];
      expect(user).toContain('f.k8s.memory-limit-too-low');
      expect(user).toContain('v1=PASS');
      expect(user).toContain('kubectl get pods');
    });

    it('when all samples agree, provisionalReason cites "no calibration harness" not disagreement', async () => {
      const grader = new ClaudeAiGrader(
        fakeCaller(() =>
          toolArgs(
            rubric.criteria.map((c) => ({
              criterion: c.key,
              level: c.levels[0].level,
            })),
          ),
        ),
      );
      const result = await grader.grade(rubric, facts);
      expect(result.provisionalReason).toMatch(/no calibration harness/);
    });

    it('when samples disagree on a criterion level, flags SAMPLE_DISAGREEMENT and updates provisionalReason (doc rule 33: divergent -> provisional)', async () => {
      let call = 0;
      const grader = new ClaudeAiGrader(
        fakeCaller(() => {
          call++;
          const level =
            call === 1
              ? rubric.criteria[0].levels[0].level
              : rubric.criteria[0].levels[
                  rubric.criteria[0].levels.length - 1
                ].level;
          return toolArgs([{ criterion: rubric.criteria[0].key, level }]);
        }),
      );
      const result = await grader.grade(rubric, facts);

      const disagreedGrade = result.criterionGrades.find(
        (g) => g.criterion === rubric.criteria[0].key,
      )!;
      expect(
        disagreedGrade.flags.some((f) => f.startsWith('SAMPLE_DISAGREEMENT')),
      ).toBe(true);
      expect(result.provisionalReason).toMatch(/disagreed/);
    });

    it('rejects a response that omits a required criterion', async () => {
      const grader = new ClaudeAiGrader(
        fakeCaller(() => ({
          grades: [
            {
              criterion: rubric.criteria[0].key,
              level: rubric.criteria[0].levels[0].level,
              confidence: 0.5,
              justification: 'x',
            },
          ],
        })),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /omitted required criteria/,
      );
    });

    it('rejects a response with an out-of-range level for a criterion', async () => {
      const grader = new ClaudeAiGrader(
        fakeCaller(() =>
          toolArgs(
            rubric.criteria.map((c) => ({ criterion: c.key, level: 9999 })),
          ),
        ),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /invalid level/,
      );
    });

    it('rejects a response naming a criterion that does not exist on the rubric', async () => {
      const grader = new ClaudeAiGrader(
        fakeCaller(() => ({
          grades: [
            {
              criterion: 'not_a_real_criterion',
              level: 1,
              confidence: 0.5,
              justification: 'x',
            },
            ...rubric.criteria.slice(1).map((c) => ({
              criterion: c.key,
              level: c.levels[0].level,
              confidence: 0.5,
              justification: 'x',
            })),
          ],
        })),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /unknown criterion/,
      );
    });

    it('rejects a response with confidence outside [0,1]', async () => {
      const grader = new ClaudeAiGrader(
        fakeCaller(() => ({
          grades: rubric.criteria.map((c) => ({
            criterion: c.key,
            level: c.levels[0].level,
            confidence: 1.5,
            justification: 'x',
          })),
        })),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /out-of-range confidence/,
      );
    });

    it('propagates a provider error out of grade() rather than hanging, and aborts the sample loop on the first failure (no silent success)', async () => {
      let callCount = 0;
      const grader = new ClaudeAiGrader(
        fakeCaller(() => {
          callCount++;
          throw new Error('provider request timed out');
        }),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(/timed out/i);
      expect(callCount).toBe(1);
    });
  });
});

describe('createLlmToolCaller', () => {
  it('returns null when no provider key is set', () => {
    expect(createLlmToolCaller(config({}))).toBeNull();
  });

  it('selects Anthropic when only ANTHROPIC_API_KEY is set', () => {
    const caller = createLlmToolCaller(config({ ANTHROPIC_API_KEY: 'k' }));
    expect(caller).toBeInstanceOf(AnthropicToolCaller);
  });

  it('selects Groq when only GROQ_API_KEY is set', () => {
    const caller = createLlmToolCaller(config({ GROQ_API_KEY: 'k' }));
    expect(caller).toBeInstanceOf(GroqToolCaller);
  });

  it('ANTHROPIC_API_KEY wins when both keys are set and LLM_PROVIDER is unset (preserves the prior default)', () => {
    const caller = createLlmToolCaller(
      config({ ANTHROPIC_API_KEY: 'a', GROQ_API_KEY: 'g' }),
    );
    expect(caller).toBeInstanceOf(AnthropicToolCaller);
  });

  it('LLM_PROVIDER=groq forces Groq even when ANTHROPIC_API_KEY is also set', () => {
    const caller = createLlmToolCaller(
      config({
        LLM_PROVIDER: 'groq',
        ANTHROPIC_API_KEY: 'a',
        GROQ_API_KEY: 'g',
      }),
    );
    expect(caller).toBeInstanceOf(GroqToolCaller);
  });

  it('LLM_PROVIDER=groq with no GROQ_API_KEY returns null (falls back to the Fake grader upstream)', () => {
    const caller = createLlmToolCaller(
      config({ LLM_PROVIDER: 'groq', ANTHROPIC_API_KEY: 'a' }),
    );
    expect(caller).toBeNull();
  });
});
