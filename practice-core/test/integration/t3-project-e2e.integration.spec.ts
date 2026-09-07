import { Test } from '@nestjs/testing';
import { ConfigModule } from '@nestjs/config';
import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { DatabaseModule, KYSELY } from '../../src/db/database.module';
import { GrpcProjectOrchestrator } from '../../src/modules/project/grpc-project-orchestrator';
import { GrpcOrchestratorClient } from '../../src/modules/attempt/grpc-orchestrator.client';
import { truncateAll } from './test-db';

/**
 * PLAN.md Phase 3 -- the FULL T3 chain driven from practice-core's side,
 * end to end against the REAL orchestrator:
 *
 *   PROJECT-mode attempt (real Postgres row)
 *     -> GrpcProjectOrchestrator.provisionForMilestone  (real Provision RPC, tier=T3)
 *     -> GrpcOrchestratorClient.execShell               (real ExecShell in the T3 pod)
 *     -> GrpcProjectOrchestrator.snapshotAndSuspend     (real Snapshot RPC -> MinIO manifest)
 *     -> GrpcProjectOrchestrator.restore                (real Restore RPC -> account reclaimed)
 *     -> GrpcProjectOrchestrator.destroy               (real Destroy RPC)
 *
 * This is the "attempt -> ... -> destroy" chain the orchestrator-side
 * TestT3Lifecycle_LocalReal doesn't cover (that one starts at the
 * Provision RPC). Together they bracket the whole path.
 *
 * Requires the compose dev stack up with the orchestrator in T3
 * local-real mode (docker-compose.yml sets CLOUD_ACCOUNTS_MODE=fake +
 * T3_EDITOR_IMAGE + seeds sandbox accounts) and the t3-tools image
 * pushed to the compose registry. OPT-IN via RUN_T3_E2E=1 so a normal
 * `jest` run (which may not have that stack) still passes; skips
 * gracefully when the flag is off or the orchestrator is unreachable.
 */
const OPTED_IN = process.env.RUN_T3_E2E === '1';
const d = OPTED_IN ? describe : describe.skip;

d('T3 PROJECT flow end-to-end (real orchestrator)', () => {
  let moduleRef: Awaited<ReturnType<typeof build>>;
  let db: Kysely<Database>;
  let projectOrch: GrpcProjectOrchestrator;
  let orchClient: GrpcOrchestratorClient;

  async function build() {
    return Test.createTestingModule({
      imports: [ConfigModule.forRoot({ isGlobal: true }), DatabaseModule],
      providers: [GrpcProjectOrchestrator, GrpcOrchestratorClient],
    }).compile();
  }

  beforeAll(async () => {
    // Point the gRPC clients at the compose orchestrator's host port.
    process.env.ORCHESTRATOR_GRPC_ADDRESS =
      process.env.ORCHESTRATOR_GRPC_ADDRESS ?? 'localhost:50051';
    process.env.ORCHESTRATOR_SHARED_SECRET =
      process.env.ORCHESTRATOR_SHARED_SECRET ?? 'compose-dev-shared-secret';
    process.env.PROJECT_ORCHESTRATOR_GRPC = 'on';

    moduleRef = await build();
    await moduleRef.init();
    db = moduleRef.get(KYSELY);
    projectOrch = moduleRef.get(GrpcProjectOrchestrator);
    orchClient = moduleRef.get(GrpcOrchestratorClient);
  });

  afterAll(async () => {
    await moduleRef.close();
  });

  it('attempt -> provision T3 -> ExecShell -> Snapshot -> Restore -> Destroy', async () => {
    await truncateAll(db);

    // --- a real PROJECT-mode attempt row ---------------------------
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 't3-e2e-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 't3-e2e@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenant.id, slug: 'proj.t3.e2e', mode: 'PROJECT' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activity.id,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {
          environment: {
            tier: 'CLOUD_ACCOUNT',
            blueprint: 'bp.project.default',
            cost_budget_usd: 5,
            cloud: { regions: ['us-east-1'] },
          },
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
        mode: 'PROJECT',
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    // --- 1. provision a T3 environment for a milestone -------------
    const env = await projectOrch.provisionForMilestone({
      attemptId: attempt.id,
      milestoneKey: 'infra',
      region: 'us-east-1',
      budgetUsd: 5,
    });
    expect(env.status).toBe('READY');
    expect(env.environmentId).toMatch(/^[0-9a-f-]{36}$/);

    // record the env on the attempt, the way project.service does
    await db
      .updateTable('attempt.attempt')
      .set({ environment_id: env.environmentId })
      .where('id', '=', attempt.id)
      .execute();

    // --- 2. ExecShell inside the real T3 workspace pod ------------
    const sh = await orchClient.execShell({
      environmentId: env.environmentId,
      attemptId: attempt.id,
      command:
        "mkdir -p /workspace && echo 'built on T3' > /workspace/NOTES.md && " +
        'printenv AWS_SHARED_CREDENTIALS_FILE && ' +
        'test -f "$AWS_SHARED_CREDENTIALS_FILE" && echo OK-T3-EXEC',
      timeoutMs: 120_000,
    });
    expect(sh.exitCode).toBe(0);
    expect(sh.stdout).toContain('OK-T3-EXEC');

    // --- 3. Snapshot + suspend (real manifest to MinIO) ----------
    const snap = await projectOrch.snapshotAndSuspend({
      attemptId: attempt.id,
      environmentId: env.environmentId,
    });
    // a REAL snapshot id -- "<envId>-<unixSeconds>", never a stub- id
    expect(snap.snapshotId).toMatch(
      new RegExp(`^${env.environmentId}-\\d{9,}$`),
    );
    expect(snap.snapshotId).not.toMatch(/^stub-/);
    expect(new Date(snap.capturedAt).toString()).not.toBe('Invalid Date');

    // --- 4. Restore (account reclaimed, fresh pod) --------------
    const restored = await projectOrch.restore({
      attemptId: attempt.id,
      snapshotId: snap.snapshotId,
    });
    expect(restored.status).toBe('READY');
    expect(restored.environmentId).toBe(env.environmentId);

    // the restored workspace is real: exec still works
    const sh2 = await orchClient.execShell({
      environmentId: restored.environmentId,
      attemptId: attempt.id,
      command: 'echo RESTORED-OK',
      timeoutMs: 60_000,
    });
    expect(sh2.exitCode).toBe(0);
    expect(sh2.stdout).toContain('RESTORED-OK');

    // --- 5. Destroy --------------------------------------------
    const destroyed = await projectOrch.destroy({
      attemptId: attempt.id,
      environmentId: restored.environmentId,
    });
    expect(destroyed.alreadyDestroyed).toBe(false);

    // a second destroy is an idempotent no-op
    const again = await projectOrch.destroy({
      attemptId: attempt.id,
      environmentId: restored.environmentId,
    });
    expect(again.alreadyDestroyed).toBe(true);
  }, 300_000);
});
