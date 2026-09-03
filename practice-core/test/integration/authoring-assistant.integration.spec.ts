import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AuthoringAssistantService } from '../../src/modules/authoring/authoring-assistant.service';
import { SpecLintService } from '../../src/modules/content-ci/spec-lint.service';
import { LlmGatewayService } from '../../src/modules/llm-gateway/llm-gateway.service';
import { FakeLlmProvider } from '../../src/modules/llm-gateway/fake-provider';
import { BudgetLedger } from '../../src/modules/llm-gateway/budget-ledger';
import { PromptCache } from '../../src/modules/llm-gateway/prompt-cache';
import { createTestDb, truncateAll } from './test-db';

// A real, schema-valid activity spec stands in for a good model draft --
// exercises the lint path against exactly what content CI accepts.
const REAL_DRAFT = readFileSync(
  join(__dirname, '../../../content/activities/lab.k8s.services.yaml'),
  'utf8',
);

/**
 * PLAN.md G12 / doc §3.1 point 10 -- the internal AI-assisted authoring
 * tool: model draft -> extract YAML -> lint against
 * activity_spec.schema.json (same check content CI runs) -> return for
 * HUMAN APPROVAL (never publishes).
 */
describe('AuthoringAssistantService (§3.1, G12)', () => {
  let db: Kysely<Database>;
  let svc: AuthoringAssistantService;
  let provider: FakeLlmProvider;

  beforeAll(() => {
    db = createTestDb();
  });
  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    provider = new FakeLlmProvider();
    const gw = new LlmGatewayService(
      [provider],
      new BudgetLedger({ globalDailyUsd: 100 }),
      new PromptCache(),
    );
    svc = new AuthoringAssistantService(db, gw, new SpecLintService());
    await db
      .insertInto('skill.skill')
      .values([
        { slug: 'k8s.services', name: 'K8s Services', domain: 'k8s' },
        { slug: 'k8s.core', name: 'K8s Core', domain: 'k8s' },
        { slug: 'k8s.deployments', name: 'K8s Deployments', domain: 'k8s' },
      ])
      .execute();
  });

  function stubModelYaml(yaml: string) {
    provider.complete = async (i) => ({
      text: '```yaml\n' + yaml + '\n```',
      model: 'fake-strong',
      inputTokens: Math.ceil(i.system.length / 4),
      outputTokens: Math.ceil(yaml.length / 4),
    });
  }


  it('returns a valid, lint-clean draft for human approval (never publishes)', async () => {
    stubModelYaml(REAL_DRAFT);
    const r = await svc.draft({
      topic: 'Exposing a Kubernetes Deployment with a Service',
      mode: 'GUIDED_LAB',
      skillSlugs: ['k8s.services', 'k8s.core', 'k8s.deployments'],
    });
    expect(r.valid).toBe(true);
    expect(r.lintErrors).toEqual([]);
    expect(r.draftYaml).toContain('id: lab.k8s.services');
    expect(r.costUsd).toBeGreaterThan(0);

    // it did NOT publish anything
    const published = await db
      .selectFrom('content.activity')
      .selectAll()
      .execute();
    expect(published).toEqual([]);
  });

  it('surfaces lint errors when the model omits required fields', async () => {
    stubModelYaml('id: lab.broken\nversion: 1\nmode: GUIDED_LAB');
    const r = await svc.draft({
      topic: 'anything',
      mode: 'GUIDED_LAB',
      skillSlugs: ['k8s.core'],
    });
    expect(r.valid).toBe(false);
    expect(r.lintErrors.length).toBeGreaterThan(0);
  });

  it('flags an unknown skill slug', async () => {
    stubModelYaml(
      REAL_DRAFT.replace(/k8s\.services/g, 'k8s.totally-made-up'),
    );
    const r = await svc.draft({
      topic: 'x',
      mode: 'GUIDED_LAB',
      skillSlugs: ['k8s.core'], // only k8s.core is "known" here
    });
    expect(r.valid).toBe(false);
    expect(r.lintErrors.join(' ')).toMatch(/skill|slug/i);
  });

  it('handles a model reply with no yaml block', async () => {
    provider.complete = async () => ({
      text: 'Sorry, I need more detail about the topic.',
      model: 'fake-strong',
      inputTokens: 5,
      outputTokens: 5,
    });
    const r = await svc.draft({ topic: 'x', mode: 'GUIDED_LAB' });
    expect(r.valid).toBe(false);
    expect(r.lintErrors[0]).toMatch(/yaml block/i);
  });

  it('degrades gracefully when the gateway has no provider', async () => {
    provider.setHealthy(false);
    provider.complete = async () => {
      throw new Error('down');
    };
    const r = await svc.draft({ topic: 'gateway-down-topic', mode: 'GUIDED_LAB' });
    expect(r.valid).toBe(false);
    expect(r.lintErrors[0]).toMatch(/degraded/i);
    provider.setHealthy(true);
  });
});
