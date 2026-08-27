import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { ArtifactService } from '../../src/modules/evaluation/artifact.service';
import { RubricRepository } from '../../src/modules/evaluation/rubric.repository';
import { FakeAiGrader } from '../../src/modules/evaluation/fake-ai-grader.service';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { createTestDb, truncateAll } from './test-db';

/**
 * Doc §6.5/§1.3.2 step 9's incident-note submission + AI-grading
 * pipeline. Exercises ArtifactService against real Postgres: key
 * validation against artifacts_required, checksum + EDITOR_SAVE event on
 * submit, and the deterministic-facts assembly + AI_MESSAGE event on
 * gradeArtifact -- with FakeAiGrader standing in for the unconfigured
 * real LLM provider (see that class's own doc comment).
 */
describe('ArtifactService (integration, real Postgres) — doc §6.5, §1.3.2 step 9', () => {
  let db: Kysely<Database>;
  let attemptRepo: AttemptRepository;
  let events: EventStoreRepository;
  let catalog: CatalogRepository;
  let artifacts: ArtifactService;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    attemptRepo = new AttemptRepository(db);
    events = new EventStoreRepository(db);
    catalog = new CatalogRepository(db);
    artifacts = new ArtifactService(
      db,
      events,
      new RubricRepository(),
      new FakeAiGrader(),
      new ActivitySpecReader(db),
    );
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 't' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 'l@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;
    userId = user.id;
    const skillRepo = new SkillRepository(db);
    await skillRepo.createSkill({
      slug: 'k8s.troubleshooting',
      name: 'K8s Troubleshooting',
      domain: 'k8s',
    });
  });

  async function makeAttemptWithIncidentNoteRequired() {
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.sim.incident-note-test',
      mode: 'PRODUCTION_SIM',
      spec: {
        id: 'lab.sim.incident-note-test',
        version: 1,
        meta: { difficulty_level: 'L3', estimated_minutes: 30 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.08 },
        skills: [{ skill: 'k8s.troubleshooting', weight: 1.0, primary: true }],
        faults: [{ id: 'f.k8s.memory-limit-too-low', apply_at: 'T0' }],
        artifacts_required: [
          {
            key: 'incident-note',
            type: 'MARKDOWN',
            rubric: 'rub.incident-note.v2',
          },
        ],
      },
    });
    return attemptRepo.create({
      tenantId,
      userId,
      activityId: version.activity_id,
      activityVersionId: version.id,
      mode: 'PRODUCTION_SIM',
    });
  }

  describe('submit', () => {
    it('accepts a submission for a required artifact key, storing content and checksum', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      const { id } = await artifacts.submit(
        attempt.id,
        'incident-note',
        '# Root cause\n\nOOM.',
      );

      const row = await db
        .selectFrom('attempt.artifact')
        .selectAll()
        .where('id', '=', id)
        .executeTakeFirstOrThrow();
      expect(row.attempt_id).toBe(attempt.id);
      expect(row.kind).toBe('incident-note');
      expect(row.content).toBe('# Root cause\n\nOOM.');
      expect(row.checksum).toHaveLength(64); // sha256 hex
    });

    it('appends an EDITOR_SAVE event on submit', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      await artifacts.submit(attempt.id, 'incident-note', 'content');

      const log = await events.replay(attempt.id);
      const saveEvent = log.find((e) => e.type === 'EDITOR_SAVE');
      expect(saveEvent).toBeDefined();
      expect(
        (saveEvent!.payload as { artifact_key: string }).artifact_key,
      ).toBe('incident-note');
    });

    it('rejects a key the activity does not require', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      await expect(
        artifacts.submit(attempt.id, 'not-a-real-key', 'content'),
      ).rejects.toThrow(/does not require an artifact/);
    });

    it('rejects submission for an attempt that does not exist', async () => {
      await expect(
        artifacts.submit(
          '00000000-0000-0000-0000-000000000000',
          'incident-note',
          'x',
        ),
      ).rejects.toThrow(/not found/);
    });
  });

  describe('gradeArtifact', () => {
    it('returns null when no artifact has been submitted for that key', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      expect(
        await artifacts.gradeArtifact(attempt.id, 'incident-note'),
      ).toBeNull();
    });

    it('grades the most recently submitted artifact against its rubric and appends AI_MESSAGE', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      await artifacts.submit(
        attempt.id,
        'incident-note',
        '# Root cause\n\nMemory limit too low.',
      );

      const result = await artifacts.gradeArtifact(attempt.id, 'incident-note');
      expect(result).not.toBeNull();
      expect(result!.rubricId).toBe('rub.incident-note.v2');
      expect(result!.provisional).toBe(true);
      expect(result!.criterionGrades).toHaveLength(3);

      const log = await events.replay(attempt.id);
      const aiMessage = log.find((e) => e.type === 'AI_MESSAGE');
      expect(aiMessage).toBeDefined();
      expect((aiMessage!.payload as { rubric_id: string }).rubric_id).toBe(
        'rub.incident-note.v2',
      );
    });

    it('grades the latest submission when the same key is submitted twice', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      await artifacts.submit(attempt.id, 'incident-note', 'first draft');
      await artifacts.submit(
        attempt.id,
        'incident-note',
        'second draft, more complete',
      );

      const result = await artifacts.gradeArtifact(attempt.id, 'incident-note');
      expect(result).not.toBeNull();

      const rows = await db
        .selectFrom('attempt.artifact')
        .selectAll()
        .where('attempt_id', '=', attempt.id)
        .where('kind', '=', 'incident-note')
        .execute();
      expect(rows).toHaveLength(2);
    });

    it('returns null for a key the activity does not require', async () => {
      const attempt = await makeAttemptWithIncidentNoteRequired();
      expect(
        await artifacts.gradeArtifact(attempt.id, 'not-a-real-key'),
      ).toBeNull();
    });
  });
});
