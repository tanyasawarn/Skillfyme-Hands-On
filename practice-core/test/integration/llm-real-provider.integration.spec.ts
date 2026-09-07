import * as path from 'node:path';
import * as dotenv from 'dotenv';
// Load practice-core/.env so GROQ_API_KEY / LLM_PROVIDER are visible both
// to the HAS_KEY gate below and to ConfigModule (jest does not do this,
// and jest's rootDir is the repo root, not practice-core, so an explicit
// path is required for reliability across full-suite runs).
dotenv.config({ path: path.resolve(__dirname, '../../.env') });

import { Test } from '@nestjs/testing';
import { ConfigModule, ConfigService } from '@nestjs/config';
import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { DatabaseModule, KYSELY } from '../../src/db/database.module';
import { EventStoreModule } from '../../src/modules/event-store/event-store.module';
import { LlmGatewayModule } from '../../src/modules/llm-gateway/llm-gateway.module';
import { LlmGatewayService } from '../../src/modules/llm-gateway/llm-gateway.service';
import {
  buildLlmProviders,
  hasRealLlmProvider,
} from '../../src/modules/llm-gateway/providers';
import { MentorModule } from '../../src/modules/mentor/mentor.module';
import { MentorService } from '../../src/modules/mentor/mentor.service';
import { AuthoringModule } from '../../src/modules/authoring/authoring.module';
import { AuthoringAssistantService } from '../../src/modules/authoring/authoring-assistant.service';
import { truncateAll } from './test-db';

/**
 * Task 1 acceptance (PLAN.md P0 / doc §7.6): the LLM Gateway makes REAL
 * model calls.
 *
 *   - Mentor returns a real LLM response using Groq in local dev.
 *   - AuthoringAssistant.draft() returns real generated output.
 *   - Switching LLM_PROVIDER / NODE_ENV changes the provider order.
 *   - No fake responses when an API key exists.
 *
 * Requires GROQ_API_KEY (or ANTHROPIC_API_KEY) in the environment
 * (practice-core/.env sets LLM_PROVIDER=groq + GROQ_API_KEY for local
 * dev) AND an explicit opt-in: RUN_LLM_REAL_TESTS=1.
 *
 * It is OPT-IN (not part of the default integration run) because it
 * makes real calls to Groq's free tier, which is rate-limited
 * (requests/min, tokens/min, tokens/day) -- running it alongside 20+
 * other suites reliably trips those limits and makes it flaky. Run it
 * on its own to verify Task 1 acceptance:
 *
 *   RUN_LLM_REAL_TESTS=1 npx jest --config test/jest-integration.json \
 *     test/integration/llm-real-provider.integration.spec.ts
 *
 * Skips (never fails) when the flag or a provider key is missing.
 */
const HAS_KEY = !!process.env.GROQ_API_KEY || !!process.env.ANTHROPIC_API_KEY;
const OPTED_IN = process.env.RUN_LLM_REAL_TESTS === '1';
const d = HAS_KEY && OPTED_IN ? describe : describe.skip;

d('LLM Gateway — real provider (Task 1)', () => {
  describe('buildLlmProviders factory (no network)', () => {
    const cfgWith = (env: Record<string, string | undefined>) =>
      ({
        get: (k: string) => env[k],
      }) as unknown as ConfigService;

    it('development (default) puts Groq first when GROQ_API_KEY is set', () => {
      const providers = buildLlmProviders(
        cfgWith({ GROQ_API_KEY: 'gsk_x', NODE_ENV: 'development' }),
      );
      expect(providers[0].name).toBe('groq');
    });

    it('production puts Anthropic first when ANTHROPIC_API_KEY is set', () => {
      const providers = buildLlmProviders(
        cfgWith({
          ANTHROPIC_API_KEY: 'sk-ant-x',
          GROQ_API_KEY: 'gsk_x',
          NODE_ENV: 'production',
        }),
      );
      expect(providers[0].name).toBe('anthropic');
      // the other real provider is kept as a failover target
      expect(providers.map((p) => p.name)).toContain('groq');
    });

    it('explicit LLM_PROVIDER overrides NODE_ENV', () => {
      const providers = buildLlmProviders(
        cfgWith({
          LLM_PROVIDER: 'anthropic',
          ANTHROPIC_API_KEY: 'sk-ant-x',
          NODE_ENV: 'development',
        }),
      );
      expect(providers[0].name).toBe('anthropic');
    });

    it('with a real key present, the fake provider is NEVER in the list', () => {
      const providers = buildLlmProviders(cfgWith({ GROQ_API_KEY: 'gsk_x' }));
      expect(providers.map((p) => p.name)).not.toContain('fake');
    });

    it('only falls back to the fake when NO real key is set anywhere', () => {
      const providers = buildLlmProviders(cfgWith({}));
      expect(providers).toHaveLength(1);
      expect(providers[0].name).toBe('fake');
    });
  });

  describe('real end-to-end calls', () => {
    let moduleRef: Awaited<ReturnType<typeof build>>;
    let db: Kysely<Database>;
    let gateway: LlmGatewayService;
    let mentor: MentorService;
    let authoring: AuthoringAssistantService;

    async function build() {
      return Test.createTestingModule({
        imports: [
          ConfigModule.forRoot({ isGlobal: true }),
          DatabaseModule,
          EventStoreModule,
          LlmGatewayModule,
          MentorModule,
          AuthoringModule,
        ],
      }).compile();
    }

    beforeAll(async () => {
      moduleRef = await build();
      await moduleRef.init();
      db = moduleRef.get(KYSELY);
      gateway = moduleRef.get(LlmGatewayService);
      mentor = moduleRef.get(MentorService);
      authoring = moduleRef.get(AuthoringAssistantService);
    });

    afterAll(async () => {
      await moduleRef.close();
    });

    it('the gateway is wired with a real provider (not the fake)', () => {
      const cfg = moduleRef.get(ConfigService);
      expect(hasRealLlmProvider(cfg)).toBe(true);
    });

    it('gateway.call() returns a real, non-degraded completion from a real provider', async () => {
      const res = await gateway.call({
        taskClass: 'mentor_reply',
        promptVersion: 'test.raw.v1',
        system: 'You are a terse assistant. Answer in one short sentence.',
        user: 'In one sentence: what does a Kubernetes readiness probe do?',
        maxOutputTokens: 128,
        noCache: true,
      });
      expect(res.degraded).toBeUndefined();
      expect(['groq', 'anthropic']).toContain(res.provider);
      expect(res.provider).not.toBe('fake');
      expect(res.provider).not.toBe('cache');
      expect(res.text.trim().length).toBeGreaterThan(10);
      expect(res.outputTokens).toBeGreaterThan(0);
    }, 30_000);

    it('Mentor returns a real LLM reply (Groq in local dev)', async () => {
      await truncateAll(db);
      const tenant = await db
        .insertInto('learner.tenant')
        .values({ name: 'llm-real-tenant' })
        .returningAll()
        .executeTakeFirstOrThrow();
      const user = await db
        .insertInto('learner.user_account')
        .values({ tenant_id: tenant.id, email: 'llm-real@test.dev' })
        .returningAll()
        .executeTakeFirstOrThrow();
      await db
        .insertInto('skill.skill')
        .values({ slug: 'llm.real.skill', name: 'S', domain: 'test' })
        .execute();
      const activity = await db
        .insertInto('content.activity')
        .values({ tenant_id: tenant.id, slug: 'lab.llm.real', mode: 'GUIDED_LAB' })
        .returningAll()
        .executeTakeFirstOrThrow();
      const version = await db
        .insertInto('content.activity_version')
        .values({
          activity_id: activity.id,
          version: 1,
          status: 'PUBLISHED',
          spec_jsonb: {
            objectives: ['understand readiness probes'],
            tasks: [
              {
                key: 't1',
                title: 'Inspect the probe',
                instructions_md: 'Look at the readiness probe on the deployment.',
              },
            ],
          },
        })
        .returningAll()
        .executeTakeFirstOrThrow();
      const attempt = await db
        .insertInto('attempt.attempt')
        .values({
          tenant_id: tenant.id,
          user_id: user.id,
          activity_id: activity.id,
          activity_version_id: version.id,
          mode: 'GUIDED_LAB',
        })
        .returningAll()
        .executeTakeFirstOrThrow();

      const r = await mentor.reply({
        attemptId: attempt.id,
        message: 'How does a readiness probe decide a pod is ready?',
      });

      expect(r.degraded).toBe(false);
      expect(r.intent).toBe('concept_q');
      expect(r.text.trim().length).toBeGreaterThan(20);
      // a real model answer mentions the mechanism, not a canned fake line
      expect(r.text.toLowerCase()).not.toContain(
        'here is a concept-level explanation (no commands, no solution).',
      );

      // the AI_MESSAGE accounting event records a real provider + cost
      const ev = await db
        .selectFrom('attempt.attempt_events')
        .selectAll()
        .where('attempt_id', '=', attempt.id)
        .where('type', '=', 'AI_MESSAGE')
        .orderBy('seq', 'desc')
        .executeTakeFirst();
      expect(ev).toBeTruthy();
      const payload = ev!.payload as { cost_usd?: number };
      expect(typeof payload.cost_usd).toBe('number');
    }, 30_000);

    it('AuthoringAssistant.draft() returns real generated YAML', async () => {
      await truncateAll(db);
      const tenant = await db
        .insertInto('learner.tenant')
        .values({ name: 'authoring-real-tenant' })
        .returningAll()
        .executeTakeFirstOrThrow();
      await db
        .insertInto('skill.skill')
        .values([
          { slug: 'linux.files', name: 'Linux files', domain: 'devops' },
        ])
        .execute();

      const res = await authoring.draft({
        topic: 'Navigate the Linux filesystem: cd, ls, pwd, and absolute vs relative paths',
        mode: 'GUIDED_LAB',
        skillSlugs: ['linux.files'],
      });

      // real generated output: a non-empty YAML draft came back
      expect(res.draftYaml.trim().length).toBeGreaterThan(100);
      expect(res.draftYaml).toMatch(/mode:\s*GUIDED_LAB/);
      expect(res.costUsd).toBeGreaterThanOrEqual(0);
      // it was NOT the degraded/empty path
      expect(res.lintErrors).not.toContain('llm gateway degraded: all_providers_down');
    }, 45_000);
  });
});
