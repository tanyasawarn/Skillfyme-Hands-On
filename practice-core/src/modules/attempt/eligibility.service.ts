import { Inject, Injectable } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { SkillRepository } from '../skill/skill.repository';
import { MasteryService } from '../skill/mastery.service';

export interface EligibilityResult {
  eligible: boolean;
  reasons: string[];
}

/**
 * Doc §4.1 step 17: "Eligibility service checks: enrolment, prerequisite
 * closure, retry cooldown, concurrent-environment quota, daily cost
 * budget, tenant budget, activity published state." §5.7: "Exceeding a
 * cap queues the request with a clear message rather than failing
 * opaquely" -- reasons[] exists so callers can surface exactly that.
 *
 * Phase 1 scope: prerequisite closure (REQUIRES gate), retry cooldown,
 * activity published state, and one-active-environment-per-learner
 * concurrency (§4.4). Tenant/daily cost budgets are §10.4's budget
 * evaluator, which reads usage_meter -- Dev A's emission target, not yet
 * wired (correctly deferred: usage_meter has no rows until the
 * Orchestrator exists). Enrolment is deferred to Phase 5 multi-tenancy
 * work per PLAN.md.
 */
@Injectable()
export class EligibilityService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly skillRepo: SkillRepository,
    private readonly mastery: MasteryService,
  ) {}

  async check(
    userId: string,
    activityVersionId: string,
  ): Promise<EligibilityResult> {
    const reasons: string[] = [];

    const version = await this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('id', '=', activityVersionId)
      .executeTakeFirst();

    if (!version) {
      return { eligible: false, reasons: ['activity version not found'] };
    }
    if (version.status !== 'PUBLISHED' && version.status !== 'CANARY') {
      reasons.push(`activity version is ${version.status}, not PUBLISHED`);
    }

    // REQUIRES-prereq mastery gate (doc §2.5 stage 2: "REQUIRES-prereq mastery < 0.55").
    const primarySkills = await this.db
      .selectFrom('content.activity_skill')
      .select('skill_id')
      .where('activity_version_id', '=', activityVersionId)
      .where('is_primary', '=', true)
      .execute();

    for (const { skill_id } of primarySkills) {
      const ancestors = await this.skillRepo.getRequiresAncestors(skill_id);
      const gateMet = await this.mastery.meetsRequiresGate(userId, ancestors);
      if (!gateMet) {
        reasons.push(`prerequisite mastery gate not met for skill ${skill_id}`);
      }
    }

    // Retry cooldown (doc §2.7: "after a failure, the same activity is not
    // immediately re-offered").
    const activityId = version.activity_id;
    const learnerState = await this.db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', activityId)
      .executeTakeFirst();
    if (
      learnerState?.cooldown_until &&
      learnerState.cooldown_until > new Date()
    ) {
      reasons.push(
        `retry cooldown active until ${learnerState.cooldown_until}`,
      );
    }

    // Concurrent-environment quota (doc §4.4: "one active environment per
    // learner by default"). An attempt still holds its environment slot
    // through SUBMITTED/EVALUATING -- per §4.1's sequence diagram, the
    // environment is only destroyed *after* evaluation completes (step
    // 13: "Destroy -> snapshot -> destroy" follows the score/mastery
    // update), not at the moment of submit.
    const activeAttempts = await this.db
      .selectFrom('attempt.attempt')
      .select((eb) => eb.fn.countAll<number>().as('count'))
      .where('user_id', '=', userId)
      .where('status', 'in', [
        'PROVISIONING',
        'READY',
        'IN_PROGRESS',
        'SUBMITTED',
        'EVALUATING',
      ])
      .executeTakeFirstOrThrow();
    if (Number(activeAttempts.count) >= 1) {
      reasons.push(
        'concurrent-environment quota exceeded (1 active environment per learner)',
      );
    }

    return { eligible: reasons.length === 0, reasons };
  }
}
