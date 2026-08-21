import { BadRequestException, Inject, Injectable } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { AttemptRepository } from './attempt.repository';
import { EligibilityService } from './eligibility.service';
import { EventStoreRepository } from '../event-store/event-store.repository';
import {
  ORCHESTRATOR_CLIENT,
  type OrchestratorClient,
} from './orchestrator-client.interface';
import { EvaluationService } from '../evaluation/evaluation.service';

export interface CreateAttemptInput {
  tenantId: string;
  userId: string;
  activityVersionId: string;
  idempotencyKey?: string;
}

/**
 * Doc §4.1 steps 16-23 (attempt creation + provisioning) and the B
 * attempt-lifecycle state machine. This is Practice Core's side of
 * PLAN.md integration point #1: "Dev B's Attempt Service calls Dev A's
 * Orchestrator via the Phase-0 gRPC contract... until Dev A's is live"
 * -- see FakeOrchestratorClient, bound in attempt.module.ts.
 *
 * Every state transition is (a) event-sourced first (D3: attempt_events
 * is the source of truth) and (b) reflected onto attempt.status via
 * optimistic-concurrency update. Steps happen in that order deliberately:
 * if the process dies after the event is appended but before the status
 * column updates, the replay tool (ReplayService) can still reconstruct
 * intent, whereas the reverse ordering would leave status ahead of any
 * audit trail explaining why.
 */
@Injectable()
export class AttemptService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly attempts: AttemptRepository,
    private readonly eligibility: EligibilityService,
    private readonly events: EventStoreRepository,
    @Inject(ORCHESTRATOR_CLIENT)
    private readonly orchestrator: OrchestratorClient,
    private readonly evaluation: EvaluationService,
  ) {}

  /**
   * Doc §4.1 steps 16-19: create the CREATED attempt row + ATTEMPT_CREATED
   * event, after an eligibility check. Provisioning (step 20 onward) is a
   * separate call (provision()) so callers can inspect the CREATED
   * attempt (or an eligibility rejection) before committing to spinning
   * up an environment -- matches the doc's "long-running work returns an
   * Operation, not a blocked request" API principle (§8.3).
   */
  async createAttempt(input: CreateAttemptInput) {
    const version = await this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('id', '=', input.activityVersionId)
      .executeTakeFirst();
    if (!version) {
      throw new BadRequestException(
        `activity version ${input.activityVersionId} not found`,
      );
    }

    const result = await this.eligibility.check(
      input.userId,
      input.activityVersionId,
    );
    if (!result.eligible) {
      throw new BadRequestException({
        message: 'not eligible to start this activity',
        reasons: result.reasons,
      });
    }

    const attempt = await this.attempts.create({
      tenantId: input.tenantId,
      userId: input.userId,
      activityId: version.activity_id,
      activityVersionId: input.activityVersionId,
      mode: version.spec_jsonb
        ? ((version as { mode?: string }).mode ?? 'GUIDED_LAB')
        : 'GUIDED_LAB',
      idempotencyKey: input.idempotencyKey,
    });

    // Idempotent replay: if attempts.create returned a pre-existing row
    // (duplicate idempotency key), don't double-append ATTEMPT_CREATED.
    const alreadyHasEvent = await this.eventHasBeenRecorded(
      attempt.id,
      'ATTEMPT_CREATED',
    );
    if (!alreadyHasEvent) {
      await this.events.append({
        attemptId: attempt.id,
        actor: 'SYSTEM',
        type: 'ATTEMPT_CREATED',
        payload: { activity_version_id: input.activityVersionId },
      });
    }

    return attempt;
  }

  /**
   * Doc §4.1 steps 20-22: provisioning request to the Environment
   * Orchestrator, warm-pool claim or cold-provision, health gate. In
   * Phase 1 this always calls FakeOrchestratorClient until Dev A's real
   * service exists (PLAN.md integration point #1).
   */
  async provision(attemptId: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt)
      throw new BadRequestException(`attempt ${attemptId} not found`);
    if (attempt.status !== 'CREATED') {
      throw new BadRequestException(
        `attempt ${attemptId} is ${attempt.status}, expected CREATED`,
      );
    }

    const version = await this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('id', '=', attempt.activity_version_id)
      .executeTakeFirstOrThrow();

    await this.events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'ENV_REQUESTED',
      payload: { blueprint_id: version.blueprint_id },
    });
    await this.attempts.transition(attemptId, attempt.version, {
      status: 'PROVISIONING',
    });

    const tierMap: Record<
      string,
      | 'T0_BROWSER'
      | 'T1_SHARED_CONTAINER'
      | 'T2_ISOLATED_MICROVM'
      | 'T3_CLOUD_ACCOUNT'
    > = {
      BROWSER: 'T0_BROWSER',
      SHARED_CONTAINER: 'T1_SHARED_CONTAINER',
      ISOLATED_VM: 'T2_ISOLATED_MICROVM',
      CLOUD_ACCOUNT: 'T3_CLOUD_ACCOUNT',
    };
    const spec = version.spec_jsonb as {
      environment?: {
        tier?: string;
        ttl_minutes?: number;
        idle_timeout_minutes?: number;
      };
    };

    const result = await this.orchestrator.provision({
      attemptId,
      blueprintId: version.blueprint_id ?? 'unknown',
      blueprintVersion: version.blueprint_version ?? '1',
      tier:
        tierMap[spec.environment?.tier ?? 'SHARED_CONTAINER'] ??
        'T1_SHARED_CONTAINER',
      ttlMinutes: spec.environment?.ttl_minutes ?? 90,
      idleTimeoutMinutes: spec.environment?.idle_timeout_minutes ?? 15,
    });

    const refreshed = await this.attempts.findById(attemptId);
    if (!refreshed)
      throw new BadRequestException(
        `attempt ${attemptId} disappeared mid-provision`,
      );

    if (result.status === 'READY') {
      await this.events.append({
        attemptId,
        actor: 'SYSTEM',
        type: 'ENV_READY',
        payload: { environment_id: result.environmentId },
      });
      return this.attempts.transition(attemptId, refreshed.version, {
        status: 'READY',
        environmentId: result.environmentId,
      });
    }

    await this.events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'ENV_FAILED',
      payload: {},
    });
    return this.attempts.transition(attemptId, refreshed.version, {
      status: 'PROVISION_FAILED',
    });
  }

  /**
   * Doc §4.1 step 23: "Attempt -> IN_PROGRESS on first learner interaction
   * (not on READY -- this keeps time-on-task honest)." Callers invoke this
   * from the first COMMAND_EXECUTED / EDITOR_SAVE / HINT_REQUESTED event,
   * not from the provisioning flow.
   */
  async markStarted(attemptId: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt)
      throw new BadRequestException(`attempt ${attemptId} not found`);
    if (attempt.status !== 'READY') return attempt; // idempotent: already started or in a later state

    await this.events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'ATTEMPT_STARTED',
      payload: {},
    });
    return this.attempts.transition(attemptId, attempt.version, {
      status: 'IN_PROGRESS',
      startedAt: new Date(),
    });
  }

  /**
   * Doc §4.1 steps 10-13: submit -> Evaluation Engine collects signals,
   * scores, updates mastery -> result page. The doc's full architecture
   * routes this through environment teardown first (destroy -> ENV_DESTROYED
   * event -> EVALUATING, PLAN.md integration point #4) because a real
   * environment must be torn down before/while grading. Phase 1 has no
   * real environment to tear down (FakeOrchestratorClient), so submit()
   * calls evaluation directly rather than waiting on an ENV_DESTROYED
   * event that will never arrive from a fake orchestrator. Once Dev A's
   * real Orchestrator exists, this should go back to: submit() only
   * marks SUBMITTED and requests destroy; handleEnvironmentDestroyed()
   * (already doc-shaped below) becomes what triggers evaluate().
   */
  async submit(attemptId: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt)
      throw new BadRequestException(`attempt ${attemptId} not found`);
    if (attempt.status !== 'IN_PROGRESS') {
      throw new BadRequestException(
        `attempt ${attemptId} is ${attempt.status}, expected IN_PROGRESS`,
      );
    }

    await this.events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'SUBMITTED',
      payload: {},
    });
    await this.attempts.transition(attemptId, attempt.version, {
      status: 'SUBMITTED',
      submittedAt: new Date(),
    });

    await this.evaluation.evaluate(attemptId);

    const evaluated = await this.attempts.findById(attemptId);
    if (!evaluated)
      throw new BadRequestException(
        `attempt ${attemptId} disappeared mid-evaluation`,
      );

    // Real orchestrator gap, found only once Dev A's service was live:
    // with FakeOrchestratorClient nothing leaked because there was no
    // real resource, so submit() never tore down the environment. Now
    // that Provision() creates a real pod/namespace, skipping this call
    // means a real, running environment survives every attempt
    // indefinitely (the reaper's TTL deadline is the only backstop,
    // which is a cost/security bug, not a design choice). Destroy is
    // best-effort here: a failure must not fail the learner's submit --
    // the reaper still guarantees teardown eventually per doc §5.6.
    if (evaluated.environment_id) {
      try {
        await this.orchestrator.destroy({
          environmentId: evaluated.environment_id,
          reason: 'submit',
        });
      } catch (err) {
        // Intentionally not rethrown -- see comment above.
        console.error(
          `[AttemptService] orchestrator.destroy failed for env ${evaluated.environment_id}:`,
          err,
        );
      }
    }

    return evaluated;
  }

  /**
   * Doc §5.4 / §8.5: mints fresh terminal WS connection endpoints for an
   * attempt's already-provisioned environment. Called by the frontend
   * workspace shell on mount and again on every reconnect attempt --
   * Connect() mints a brand-new session_token each call (server.go's
   * connectionEndpoints), so re-calling this after a dropped socket is
   * the correct way to get back in, not reusing a stale token.
   */
  async connect(attemptId: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt)
      throw new BadRequestException(`attempt ${attemptId} not found`);
    if (!attempt.environment_id) {
      throw new BadRequestException(
        `attempt ${attemptId} has no environment (status ${attempt.status})`,
      );
    }
    return this.orchestrator.connect({ environmentId: attempt.environment_id });
  }

  /**
   * Doc §4.2 / PLAN.md integration point #4: "ENV_DESTROYED is the only
   * way an attempt learns its environment is gone... Attempt Service must
   * handle this idempotently." Called from the event consumer that
   * subscribes to Dev A's ENV_DESTROYED events (see event-store.module
   * wiring notes) -- not called directly by user-facing endpoints.
   */
  async handleEnvironmentDestroyed(attemptId: string, reason: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt) return; // attempt already gone / unknown -- nothing to reconcile
    if (['COMPLETED', 'ABANDONED', 'EXPIRED'].includes(attempt.status)) return; // idempotent no-op

    const nextStatus = reason === 'submit' ? 'EVALUATING' : 'SUSPENDED';
    await this.attempts.transition(attemptId, attempt.version, {
      status: nextStatus,
      environmentId: null,
    });
  }

  private async eventHasBeenRecorded(
    attemptId: string,
    type: string,
  ): Promise<boolean> {
    const events = await this.events.replay(attemptId);
    return events.some((e) => e.type === type);
  }
}
