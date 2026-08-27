import { ConfigService } from '@nestjs/config';
import Anthropic from '@anthropic-ai/sdk';
import { ClaudeAiGrader } from './claude-ai-grader.service';
import { RubricRepository } from './rubric.repository';
import type { GradingFacts } from './ai-grader.interface';

const rubric = new RubricRepository().getRubric('rub.incident-note.v2')!;

const facts: GradingFacts = {
  artifactText: '# Incident note\n\nRoot cause: memory limit too low.',
  appliedFaultIds: ['f.k8s.memory-limit-too-low'],
  resolutionValidatorResults: [{ validatorId: 'v1', status: 'PASS' }],
  commandSequence: ['kubectl get pods', 'kubectl describe pod checkout-abc'],
};

function config(apiKey: string | undefined): ConfigService {
  return {
    get: (key: string) => (key === 'ANTHROPIC_API_KEY' ? apiKey : undefined),
  } as unknown as ConfigService;
}

function toolResponse(grades: Array<{ criterion: string; level: number }>) {
  return {
    content: [
      {
        type: 'tool_use',
        id: 'toolu_1',
        name: 'submit_grades',
        input: {
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
        },
      },
    ],
  };
}

describe('ClaudeAiGrader', () => {
  describe('constructor / DI safety', () => {
    it('does not throw when ANTHROPIC_API_KEY is unset (NestJS eagerly instantiates every provider regardless of which the AI_GRADER factory selects)', () => {
      expect(() => new ClaudeAiGrader(config(undefined))).not.toThrow();
    });

    it('grade() throws a clear DI-wiring error if called without a key configured', async () => {
      const grader = new ClaudeAiGrader(config(undefined));
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /DI wiring bug/,
      );
    });

    it('constructs the Anthropic client with an explicit timeout and retry budget rather than SDK defaults', () => {
      const grader = new ClaudeAiGrader(config('fake-test-key'));
      // @ts-expect-error -- reaching into the private client field to assert on its construction options.
      const client = grader.client as Anthropic;
      expect(client.timeout).toBe(45_000);
      expect(client.maxRetries).toBe(2);
    });
  });

  describe('grade() with a mocked Anthropic client', () => {
    function graderWithMockedClient(
      createImpl: (...args: unknown[]) => unknown,
    ) {
      const grader = new ClaudeAiGrader(config('fake-test-key'));
      // @ts-expect-error -- reaching into the private client field to mock the SDK boundary is the standard pattern for testing a thin API wrapper without hitting the real network.
      grader.client.messages = { create: createImpl };
      return grader;
    }

    it('grades every rubric criterion and marks the result provisional', async () => {
      const grader = graderWithMockedClient(async () => toolResponse([]));
      const result = await grader.grade(rubric, facts);

      expect(result.rubricId).toBe('rub.incident-note.v2');
      expect(result.criterionGrades).toHaveLength(rubric.criteria.length);
      expect(result.provisional).toBe(true);
    });

    it('makes exactly SAMPLE_COUNT (3) calls per grade() invocation (doc rule 33: multi-sample)', async () => {
      let callCount = 0;
      const grader = graderWithMockedClient(async () => {
        callCount++;
        return toolResponse([]);
      });
      await grader.grade(rubric, facts);
      expect(callCount).toBe(3);
    });

    it('forces tool_choice to submit_grades (doc rule 32: structured output only, not free text)', async () => {
      let capturedArgs: any;
      const grader = graderWithMockedClient(async (args: any) => {
        capturedArgs = args;
        return toolResponse([]);
      });
      await grader.grade(rubric, facts);
      expect(capturedArgs.tool_choice).toEqual({
        type: 'tool',
        name: 'submit_grades',
      });
      expect(capturedArgs.tools[0].name).toBe('submit_grades');
    });

    it('includes the learner artifact inside a delimited block, never as a raw system-prompt substitution (doc rule 35: prompt-injection defence)', async () => {
      let capturedArgs: any;
      const grader = graderWithMockedClient(async (args: any) => {
        capturedArgs = args;
        return toolResponse([]);
      });
      await grader.grade(rubric, facts);
      const userMessage = capturedArgs.messages[0].content as string;
      expect(userMessage).toContain('<learner_artifact>');
      expect(userMessage).toContain('</learner_artifact>');
      expect(userMessage).toContain(facts.artifactText);
    });

    it('never passes a live environment handle or raw telemetry -- only the precomputed GradingFacts fields (doc rule 34)', async () => {
      let capturedArgs: any;
      const grader = graderWithMockedClient(async (args: any) => {
        capturedArgs = args;
        return toolResponse([]);
      });
      await grader.grade(rubric, facts);
      const userMessage = capturedArgs.messages[0].content as string;
      expect(userMessage).toContain('f.k8s.memory-limit-too-low');
      expect(userMessage).toContain('v1=PASS');
      expect(userMessage).toContain('kubectl get pods');
    });

    it('when all samples agree, provisionalReason cites "no calibration harness" not disagreement', async () => {
      const grader = graderWithMockedClient(async () =>
        toolResponse(
          rubric.criteria.map((c) => ({
            criterion: c.key,
            level: c.levels[0].level,
          })),
        ),
      );
      const result = await grader.grade(rubric, facts);
      expect(result.provisionalReason).toMatch(/no calibration harness/);
    });

    it('when samples disagree on a criterion level, flags SAMPLE_DISAGREEMENT and updates provisionalReason (doc rule 33: divergent -> provisional)', async () => {
      let call = 0;
      const grader = graderWithMockedClient(async () => {
        call++;
        // First sample grades criterion[0] at its lowest level; later samples at its highest -- guaranteed disagreement.
        const level =
          call === 1
            ? rubric.criteria[0].levels[0].level
            : rubric.criteria[0].levels[rubric.criteria[0].levels.length - 1]
                .level;
        return toolResponse([{ criterion: rubric.criteria[0].key, level }]);
      });
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
      const grader = graderWithMockedClient(async () => ({
        content: [
          {
            type: 'tool_use',
            id: 'toolu_1',
            name: 'submit_grades',
            input: {
              grades: [
                {
                  criterion: rubric.criteria[0].key,
                  level: rubric.criteria[0].levels[0].level,
                  confidence: 0.5,
                  justification: 'x',
                },
              ],
            },
          },
        ],
      }));
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /omitted required criteria/,
      );
    });

    it('rejects a response with an out-of-range level for a criterion', async () => {
      const grader = graderWithMockedClient(async () =>
        toolResponse(
          rubric.criteria.map((c) => ({ criterion: c.key, level: 9999 })),
        ),
      );
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /invalid level/,
      );
    });

    it('rejects a response naming a criterion that does not exist on the rubric', async () => {
      const grader = graderWithMockedClient(async () => ({
        content: [
          {
            type: 'tool_use',
            id: 'toolu_1',
            name: 'submit_grades',
            input: {
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
            },
          },
        ],
      }));
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /unknown criterion/,
      );
    });

    it('rejects a response with confidence outside [0,1]', async () => {
      const grader = graderWithMockedClient(async () => ({
        content: [
          {
            type: 'tool_use',
            id: 'toolu_1',
            name: 'submit_grades',
            input: {
              grades: rubric.criteria.map((c) => ({
                criterion: c.key,
                level: c.levels[0].level,
                confidence: 1.5,
                justification: 'x',
              })),
            },
          },
        ],
      }));
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /out-of-range confidence/,
      );
    });

    it('throws a clear error if the model responds without any tool_use block', async () => {
      const grader = graderWithMockedClient(async () => ({
        content: [{ type: 'text', text: 'I refuse to grade this.' }],
      }));
      await expect(grader.grade(rubric, facts)).rejects.toThrow(
        /expected a tool_use block/,
      );
    });

    it('propagates a timeout-shaped API error out of grade() rather than hanging (caller is responsible for catching it)', async () => {
      const grader = graderWithMockedClient(async () => {
        throw new Anthropic.APIConnectionTimeoutError();
      });
      await expect(grader.grade(rubric, facts)).rejects.toThrow(/timed out/i);
    });

    it('propagates a transient rate-limit error out of grade() (no silent success on a failed sample)', async () => {
      const grader = graderWithMockedClient(async () => {
        throw new Anthropic.RateLimitError(
          429,
          {},
          'rate limited',
          new Headers(),
        );
      });
      await expect(grader.grade(rubric, facts)).rejects.toThrow();
    });

    it('does not multiply retries across the 3-sample loop into unbounded latency (each sample fails fast once its own mock rejects)', async () => {
      let callCount = 0;
      const grader = graderWithMockedClient(async () => {
        callCount++;
        throw new Anthropic.APIConnectionTimeoutError();
      });
      await expect(grader.grade(rubric, facts)).rejects.toThrow();
      // grade() aborts on the first sample's failure rather than
      // attempting all SAMPLE_COUNT samples when one has already failed.
      expect(callCount).toBe(1);
    });
  });
});
