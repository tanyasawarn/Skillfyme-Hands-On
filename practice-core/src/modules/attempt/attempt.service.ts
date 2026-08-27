import { Inject, Injectable } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { AttemptStatus, Database } from '../../db/schema';
import { AttemptRepository } from './attempt.repository';
import { EligibilityService } from './eligibility.service';
import { EventStoreRepository } from '../event-store/event-store.repository';
import {
  appendTypedEvent,
  type AttemptEventType,
} from '../event-store/attempt-event-type';
import {
  ORCHESTRATOR_CLIENT,
  type OrchestratorClient,
} from './orchestrator-client.interface';
import { EvaluationService } from '../evaluation/evaluation.service';
import {
  ValidatorRunnerService,
  type TaskSpec,
} from '../evaluation/validator-runner.service';
import { ReplayService } from '../event-store/replay.service';
import type { ActivitySpec } from '../catalog/activity-spec';
import { findOrThrow } from '../../common/find-or-throw';
import { AttemptStatusGroups } from '../../common/attempt-status-groups';
import { attemptError, singleAttemptError } from './attempt-error';

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
    private readonly validatorRunner: ValidatorRunnerService,
    private readonly replay: ReplayService,
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
    const version = findOrThrow(
      await this.db
        .selectFrom('content.activity_version as v')
        .innerJoin('content.activity as a', 'a.id', 'v.activity_id')
        .select(['v.id', 'v.activity_id', 'v.spec_jsonb', 'a.mode'])
        .where('v.id', '=', input.activityVersionId)
        .executeTakeFirst(),
      `activity version ${input.activityVersionId} not found`,
    );

    const result = await this.eligibility.check(
      input.userId,
      input.activityVersionId,
    );
    if (!result.eligible) {
      attemptError(result.reasons);
    }

    // Doc §2.7 / §4.5: "A retry is a new Attempt row with
    // retry_of_attempt_id and incremented retry_index." The most recent
    // completed attempt on this activity (any version) is what's being
    // retried, if one exists; retryIndex is 0 for a genuine first
    // attempt, otherwise the prior attempt's retryIndex + 1.
    const priorAttempt = await this.attempts.findMostRecentCompletedAttempt(
      input.userId,
      version.activity_id,
    );

    const attempt = await this.attempts.create({
      tenantId: input.tenantId,
      userId: input.userId,
      activityId: version.activity_id,
      activityVersionId: input.activityVersionId,
      mode: version.mode,
      idempotencyKey: input.idempotencyKey,
      ...(priorAttempt
        ? {
            retryOfAttemptId: priorAttempt.id,
            retryIndex: priorAttempt.retry_index + 1,
          }
        : {}),
    });

    // Idempotent replay: if attempts.create returned a pre-existing row
    // (duplicate idempotency key), don't double-append ATTEMPT_CREATED.
    const alreadyHasEvent = await this.eventHasBeenRecorded(
      attempt.id,
      'ATTEMPT_CREATED',
    );
    if (!alreadyHasEvent) {
      await appendTypedEvent(this.events, {
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
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (attempt.status !== 'CREATED') {
      singleAttemptError(
        'INVALID_STATE_TRANSITION',
        `attempt ${attemptId} is ${attempt.status}, expected CREATED`,
        { attemptId, currentStatus: attempt.status, expectedStatus: 'CREATED' },
      );
    }

    const version = await this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('id', '=', attempt.activity_version_id)
      .executeTakeFirstOrThrow();

    await appendTypedEvent(this.events, {
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
    const spec = version.spec_jsonb as Pick<
      ActivitySpec,
      'environment' | 'health_gate'
    >;

    // PLAN.md M1.3: environment.seed is the activity's ordered fixture
    // list (contracts/activity_spec.schema.json) -- previously read
    // nowhere at all, so ProvisionRequest.fixtures (contracts/
    // orchestrator.proto field 6, defined on the wire since Phase 0 but
    // never populated by either side) always arrived empty regardless of
    // what an activity's seed: block declared. fixture_id is content's
    // own id string (e.g. "fx.k3s-ready.v1"); version is parsed from its
    // trailing ".vN" suffix per doc §3.6's content-versioning
    // convention, defaulting to "1" for an id with no explicit version
    // suffix rather than failing the whole provision over a formatting
    // gap.
    const fixtures = (spec.environment?.seed ?? []).map((s) => {
      const match = /^(.*)\.v(\d+)$/.exec(s.fixture);
      return match
        ? { fixtureId: match[1] + '.v' + match[2], version: match[2] }
        : { fixtureId: s.fixture, version: '1' };
    });

    // PLAN.md M1.4/M1.14: health_gate is a top-level spec field (doc
    // §3.2/§7.3, contracts/activity_spec.schema.json), previously
    // declared in content (e.g. sim.sre.checkout-latency-incident.yaml)
    // but never read here at all -- JSON-stringified for the wire (see
    // ProvisionRequest.healthGateJson's own comment for why a string,
    // not a typed field). undefined/absent stays undefined, not "[]",
    // so the orchestrator's own req.HealthGateJson != "" check correctly
    // treats "no health_gate declared" as "skip the richer gate" rather
    // than "declared an empty list of checks" (a meaningless distinction
    // in practice, but keeping the wire value absent when the activity
    // genuinely didn't author one is the more honest representation).
    const healthGateJson = spec.health_gate?.length
      ? JSON.stringify(spec.health_gate)
      : undefined;

    const result = await this.orchestrator.provision({
      attemptId,
      blueprintId: version.blueprint_id ?? 'unknown',
      blueprintVersion: version.blueprint_version ?? '1',
      tier:
        tierMap[spec.environment?.tier ?? 'SHARED_CONTAINER'] ??
        'T1_SHARED_CONTAINER',
      ttlMinutes: spec.environment?.ttl_minutes ?? 90,
      idleTimeoutMinutes: spec.environment?.idle_timeout_minutes ?? 15,
      fixtures,
      healthGateJson,
    });

    const refreshed = await this.attempts.findById(attemptId);
    if (!refreshed)
      singleAttemptError(
        'ATTEMPT_VANISHED',
        `attempt ${attemptId} disappeared mid-provision`,
        { attemptId },
      );

    if (result.status === 'READY') {
      await appendTypedEvent(this.events, {
        attemptId,
        actor: 'SYSTEM',
        type: 'ENV_READY',
        payload: { environment_id: result.environmentId },
      });

      // Doc §3.2/§7.3 worked example, PLAN.md Phase 2 integration point:
      // "faults are applied only after the health gate passes." The
      // health gate itself is the orchestrator's WaitForPodReady
      // (server.go, blocks inside the Provision RPC this method already
      // awaited above) -- reaching this branch at all means it passed,
      // so T0 faults apply immediately here. apply_at values other than
      // "T0" (e.g. "T+900" escalation) are intentionally not scheduled
      // yet -- that needs a timer/job mechanism this synchronous
      // request/response flow doesn't have; T0 is what a PRODUCTION_SIM
      // attempt actually needs to become a real fault scenario today.
      await this.applyT0Faults(
        attemptId,
        result.environmentId,
        version.spec_jsonb,
      );

      return this.attempts.transition(attemptId, refreshed.version, {
        status: 'READY',
        environmentId: result.environmentId,
      });
    }

    await appendTypedEvent(this.events, {
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
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (attempt.status !== 'READY') return attempt; // idempotent: already started or in a later state

    await appendTypedEvent(this.events, {
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
  /**
   * Doc §6.3's learner-triggered validation ("Check my work" / auto-
   * debounce): runs the full validator set against the live environment
   * and returns per-task pass/fail, WITHOUT ending the attempt. This is
   * the missing feedback loop -- before this, the only way to learn a
   * task wasn't done was to submit() and read the score screen, with no
   * recovery path (see the "quiz flow break after submission" bug).
   *
   * Unlike submit(): no state transition (stays IN_PROGRESS), no
   * environment teardown, no scoring/mastery update. It does write an
   * attempt.validation_run row with trigger 'manual' -- which is
   * deliberate: fn.efficiency.v2 reads preSubmitValidationRunCount as
   * evidence the learner actually engaged ("minor typo shouldn't zero
   * out efficiency"), so a checked-then-submitted attempt is scored more
   * fairly than a blind submit.
   */
  async checkWork(attemptId: string) {
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (attempt.status !== 'IN_PROGRESS') {
      singleAttemptError(
        'INVALID_STATE_TRANSITION',
        `attempt ${attemptId} is ${attempt.status}, expected IN_PROGRESS`,
        {
          attemptId,
          currentStatus: attempt.status,
          expectedStatus: 'IN_PROGRESS',
        },
      );
    }
    if (!attempt.environment_id) {
      singleAttemptError(
        'NO_ENVIRONMENT',
        `attempt ${attemptId} has no environment to validate against`,
        { attemptId, currentStatus: attempt.status },
      );
    }

    const version = findOrThrow(
      await this.db
        .selectFrom('content.activity_version')
        .select('spec_jsonb')
        .where('id', '=', attempt.activity_version_id)
        .executeTakeFirst(),
      `activity version ${attempt.activity_version_id} not found`,
    );
    const spec = version.spec_jsonb as Pick<ActivitySpec, never> & {
      tasks?: TaskSpec[];
    };

    const summaries = await this.validatorRunner.run({
      attemptId,
      environmentId: attempt.environment_id,
      scope: 'all',
      trigger: 'manual',
      tasks: spec.tasks ?? [],
    });

    // Same as EvaluationService.evaluate(): ReplayService is the single
    // writer of attempt_task_state, rebuilt from the TASK_PASSED/FAILED
    // events the run above just appended -- so GET /tasks reflects this
    // check immediately, not just after a submit.
    await this.replay.rebuildForAttempt(attemptId);
    await this.attempts.touch(attemptId);

    return {
      tasks: summaries.map((s) => ({
        task_key: s.taskKey,
        required: s.required,
        passed: s.passed,
        validators: s.results.map((r) => ({
          validator_id: r.validatorId,
          status: r.status,
          severity: r.severity,
        })),
      })),
      all_required_passed: summaries
        .filter((s) => s.required)
        .every((s) => s.passed),
    };
  }

  async submit(attemptId: string) {
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (attempt.status !== 'IN_PROGRESS') {
      singleAttemptError(
        'INVALID_STATE_TRANSITION',
        `attempt ${attemptId} is ${attempt.status}, expected IN_PROGRESS`,
        {
          attemptId,
          currentStatus: attempt.status,
          expectedStatus: 'IN_PROGRESS',
        },
      );
    }

    await appendTypedEvent(this.events, {
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
      singleAttemptError(
        'ATTEMPT_VANISHED',
        `attempt ${attemptId} disappeared mid-evaluation`,
        { attemptId },
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
          attemptId,
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
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (!attempt.environment_id) {
      singleAttemptError(
        'NO_ENVIRONMENT',
        `attempt ${attemptId} has no environment (status ${attempt.status})`,
        { attemptId, currentStatus: attempt.status },
      );
    }
    return this.orchestrator.connect({
      environmentId: attempt.environment_id,
      attemptId,
    });
  }

  /**
   * Doc §4.2 / PLAN.md integration point #4: "ENV_DESTROYED is the only
   * way an attempt learns its environment is gone... Attempt Service must
   * handle this idempotently." Called from the NATS event consumer that
   * subscribes to the Orchestrator's ENV_DESTROYED publish (see
   * EnvDestroyedConsumer) -- not called directly by user-facing endpoints.
   *
   * This is also where the revised lifecycle requirement's 15-minute
   * inactivity auto-suspend actually lands: the Go orchestrator's idle
   * detector already fires at 15min (real compute teardown,
   * orchestrator/internal/idledetect/detector.go) and publishes
   * ENV_DESTROYED(reason="idle") -- this handler is what turns that into
   * the attempt's SUSPENDED status. No separate 15-minute timer needed on
   * this side; the mechanism already existed, only the reason-agnostic
   * SUSPENDED transition did too.
   *
   * Every status that means "this attempt is fully done, no further
   * transition is valid" must be excluded here -- attempt.repository's
   * transition() is a plain version-gated update with no state-machine
   * check of its own, so without this guard a destroy event arriving
   * after evaluation already completed (a real race: submit() requests
   * its own destroy, but the orchestrator's idle detector or reaper can
   * independently fire on the same environment before that destroy
   * lands) would silently regress a PASSED/FAILED attempt back to
   * EVALUATING/SUSPENDED. CACHED is excluded the same way as SUSPENDED --
   * a confirmed real bug otherwise: cache()'s own destroy() call (below)
   * causes the orchestrator to publish its own ENV_DESTROYED(idle),
   * which would loop back through this handler and regress a
   * just-cached attempt straight back to SUSPENDED.
   */
  async handleEnvironmentDestroyed(attemptId: string, reason: string) {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt) return; // attempt already gone / unknown -- nothing to reconcile
    if (AttemptStatusGroups.TERMINAL.includes(attempt.status)) return; // idempotent no-op
    if (
      (attempt.status === 'SUSPENDED' || attempt.status === 'CACHED') &&
      reason !== 'submit'
    ) {
      return; // already past the live states, nothing new to record
    }

    const nextStatus = reason === 'submit' ? 'EVALUATING' : 'SUSPENDED';
    await this.attempts.transition(attemptId, attempt.version, {
      status: nextStatus,
      environmentId: null,
    });
  }

  /**
   * Revised lifecycle requirement §3/§9: SUSPENDED -> CACHED, the second
   * stage. Unlike the old direct-from-live sweep, an attempt reaching
   * here has ALREADY had its environment torn down (SUSPENDED means the
   * 15-min idle path, or a submit/ttl/reaper/budget/admin destroy,
   * already ran) -- so there is no orchestrator.destroy() call here
   * anymore. This stage is purely a DB/metadata transition for
   * history/cleanup purposes (requirement §6: cached labs stay visible
   * in history), not a cost-control one -- SUSPENDED already achieved
   * zero backend cost (§4).
   *
   * Snapshot stub (§5, "persisted user progress snapshot"): no real
   * workspace-state capture exists anywhere in this codebase yet (the Go
   * orchestrator's Snapshot/Restore RPCs are Unimplemented stubs --
   * confirmed before writing this). This fires SNAPSHOT_TAKEN and sets
   * snapshot_id/snapshot_taken_at so the event log and data model are
   * shaped correctly for when real capture is built, but snapshot_id is
   * always a placeholder value today, not a real artifact reference --
   * reactivate() below still provisions a fresh environment, not a
   * restored one.
   */
  async cache(attemptId: string): Promise<void> {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt) return;
    if (attempt.status !== 'SUSPENDED') {
      return; // only the second stage of suspended -> cached; raced or already elsewhere
    }

    const snapshotId = `stub-${attemptId}-${Date.now()}`;
    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'SNAPSHOT_TAKEN',
      payload: {
        snapshot_id: snapshotId,
        // Explicit, not just absent-field: makes it unambiguous to
        // anything reading the event log that this is a placeholder,
        // not a dropped/failed real capture.
        stub: true,
      },
    });
    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'SUSPENDED',
      payload: { reason: 'cache_sweep_inactive', from_status: attempt.status },
    });
    await this.attempts.transition(attemptId, attempt.version, {
      status: 'CACHED',
      snapshotId,
      snapshotTakenAt: new Date(),
    });
    console.log(
      `[CacheSweep] attempt ${attemptId}: SUSPENDED -> CACHED (stale)`,
    );
  }

  /**
   * Statuses reactivate() will resume in place rather than treat as a
   * dead end. SUSPENDED/CACHED are the normal inactivity path. FAILED
   * and EVAL_FAILED are deliberately excluded -- those mean the learner's
   * attempt was actually scored/evaluated (or evaluation itself broke),
   * a finished attempt in either case; recovering from those already has
   * its own, different mechanism (a brand-new attempt via
   * findMostRecentCompletedAttempt's retry-chain, retry_of_attempt_id +
   * retry_index), and resuming the SAME row in place would let a learner
   * keep editing a workspace after it was already scored -- a real
   * conflict with how scoring/mastery works, not just an omission.
   * PROVISION_FAILED is the one failure mode that's "recoverable" in the
   * resume-in-place sense requirement §5 asks for: the environment never
   * came up at all, so the learner did nothing wrong and never touched
   * anything to score -- retrying provisioning on the same row is
   * strictly correct here in a way it isn't for the other two.
   */
  private static readonly REACTIVATABLE_STATUSES: readonly AttemptStatus[] = [
    'SUSPENDED',
    'CACHED',
    'PROVISION_FAILED',
  ];

  /**
   * Reactivation: "click Start Lab" on a suspended/cached/recoverably-
   * failed attempt resumes it, not creates a new instance (requirement
   * §5). Idempotent by construction (requirement §8): a second click
   * while the first reactivation is still provisioning finds the attempt
   * already past REACTIVATABLE_STATUSES (CREATED/PROVISIONING/READY) and
   * returns it as-is rather than re-transitioning, so repeated clicks
   * never spawn a second environment -- the "current state" check §8
   * asks for is exactly this status guard, and "existing session
   * reference" is attempt.environment_id, already null for any attempt
   * that reaches this method.
   *
   * Snapshot restore (§5) is not real yet -- see cache()'s doc comment.
   * This lands back on CREATED so provision() re-runs its normal path
   * (new environment, T0 faults reapplied), which is a fresh environment
   * today, not a restored one. From the learner's perspective progress
   * inside attempt_task_state/attempt_events is untouched either way
   * (this method never deletes anything), so task completion and hint
   * history do survive -- only the workspace filesystem/container itself
   * does not yet.
   */
  async reactivate(attemptId: string) {
    const attempt = findOrThrow(
      await this.attempts.findById(attemptId),
      `attempt ${attemptId} not found`,
    );
    if (!AttemptService.REACTIVATABLE_STATUSES.includes(attempt.status)) {
      return attempt; // already reactivated (or never in a resumable state) -- idempotent no-op
    }

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'LEARNER',
      type: 'RESUMED',
      payload: {
        from_status: attempt.status,
        snapshot_id: attempt.snapshot_id,
      },
    });
    console.log(
      `[CacheSweep] attempt ${attemptId}: ${attempt.status} -> CREATED (reactivated)`,
    );
    return this.attempts.transition(attemptId, attempt.version, {
      status: 'CREATED',
      environmentId: null,
    });
  }

  private async eventHasBeenRecorded(
    attemptId: string,
    type: AttemptEventType,
  ): Promise<boolean> {
    const events = await this.events.replay(attemptId);
    return events.some((e) => e.type === type);
  }

  /**
   * Doc §3.2/§7.3 worked example: a PRODUCTION_SIM activity's `faults`
   * array, applied in author order. A fault the orchestrator has no real
   * handler for yet (see content/faults/*.yaml's handler_implemented
   * field -- 5 of 35 today) comes back applied=false rather than
   * throwing (GrpcOrchestratorClient.injectFault's contract); recorded
   * as FAULT_INJECTED regardless so the event log is an honest record of
   * what was *attempted*, and provisioning still succeeds -- an
   * unimplemented fault handler is a content/platform gap, not a reason
   * to fail the learner's environment setup.
   */
  private async applyT0Faults(
    attemptId: string,
    environmentId: string,
    specJsonb: unknown,
  ): Promise<void> {
    const spec = specJsonb as Pick<ActivitySpec, 'faults'>;
    const t0Faults = (spec.faults ?? []).filter((f) => f.apply_at === 'T0');

    for (const fault of t0Faults) {
      const result = await this.orchestrator.injectFault({
        environmentId,
        faultId: fault.id,
        // ActivitySpec.faults[].params mirrors the schema's untyped
        // `{"type": "object"}` -- InjectFaultRequest.params needs
        // Record<string, string> specifically (orchestrator-client.
        // interface.ts), a narrower contract this call site owns, not
        // something the canonical spec type should assume for every
        // consumer.
        params: (fault.params ?? {}) as Record<string, string>,
        attemptId,
      });
      await appendTypedEvent(this.events, {
        attemptId,
        actor: 'SYSTEM',
        type: 'FAULT_INJECTED',
        payload: {
          fault_id: fault.id,
          applied: result.applied,
          symptom_verified: result.symptomVerified,
        },
      });
    }
  }
}
