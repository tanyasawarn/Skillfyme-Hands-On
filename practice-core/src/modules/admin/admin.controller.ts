import {
  BadRequestException,
  Body,
  Controller,
  Get,
  Inject,
  Param,
  Post,
  Query,
} from '@nestjs/common';
import { AnalyticsService } from './analytics.service';
import { CostDashboardService, type CostGrain } from './cost-dashboard.service';
import {
  AnalyticsQueryService,
  type RollupGrain,
} from '../analytics/analytics-query.service';
import { SpecLintService } from '../content-ci/spec-lint.service';
import { CatalogRepository } from '../catalog/catalog.repository';
import { KYSELY } from '../../db/database.module';
import type { Kysely } from 'kysely';
import type { ActivityVersionStatus, Database } from '../../db/schema';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';
import { Role } from '../auth/role';
import { Roles } from '../auth/roles.decorator';
import type { ActivitySpec } from '../catalog/activity-spec';

/**
 * Doc §3.7: "Web CMS... Guided forms, task builder, validator picker,
 * hint ladder editor, rubric editor, preview & test-run, publish
 * request." Phase 1 scope per §13.1: "minimal CMS (read/preview;
 * authoring in Git)." This is the read/preview/publish-request half --
 * authors still write YAML in the content/ repo, this surface lints and
 * publishes it (what §3.7 calls the CMS "writing the same YAML and
 * opening a merge request... both paths converge on CI"). A guided
 * form-based editor is a larger UI investment out of scope for this
 * Phase 1 slice.
 *
 * Every route here is content-authoring/analytics surface, not learner
 * surface -- gated to non-learner roles (see auth/roles.guard.ts) on top
 * of the auth every route already requires.
 *
 * Doc §3.7 draft/review/approval flow: DRAFT -[submit-review]-> IN_REVIEW
 * -[review-decision]-> APPROVED or back to DRAFT -[publish]-> PUBLISHED.
 * activities/draft and submit-review are 'author'-or-'admin' (class-level
 * @Roles above); review-decision and (activity-versions) publish are
 * 'admin'-only (method-level @Roles override) -- the person approving or
 * publishing content is deliberately never the same role as the person
 * who can merely draft it (separation of duties).
 *
 * Route note: every action route here uses "/segment" (e.g.
 * "activities/draft"), never a literal ":" ("activities:draft") --
 * confirmed live that Express/path-to-regexp under this NestJS version
 * parses ":xxx" as a *named route parameter*, not literal text, so
 * "activities:lint" and "activities:draft" silently compiled to the
 * *same* pattern and whichever was registered first (:lint) swallowed
 * every request meant for the others. attempt.controller.ts hit the
 * ":id:action" variant of this same bug earlier; this is the
 * "literal-segment:action" variant, which is just as broken.
 */
@Controller('v1/admin')
@Roles(Role.ADMIN, Role.AUTHOR)
export class AdminController {
  constructor(
    private readonly analytics: AnalyticsService,
    private readonly cost: CostDashboardService,
    private readonly analyticsQuery: AnalyticsQueryService,
    private readonly lint: SpecLintService,
    private readonly catalog: CatalogRepository,
    @Inject(KYSELY) private readonly db: Kysely<Database>,
  ) {}

  /** Doc §3.5 step 1 (LINT), exposed for CMS preview before publish. */
  @Post('activities/lint')
  async lintSpec(@Body() body: { yaml: string }) {
    const spec = this.lint.parseYaml(body.yaml);
    const skills = await this.db
      .selectFrom('skill.skill')
      .select('slug')
      .execute();
    const result = this.lint.lint(spec, new Set(skills.map((s) => s.slug)));
    return result;
  }

  /** Doc §3.7 "publish request": lints, then publishes if valid. Bypasses
   * the draft/review/approval flow below -- kept for the seed/CLI
   * publishing path (scripts/publish-all-content.ts), which authors a
   * trusted bulk import, not a single CMS-driven change that needs
   * human review. */
  @Post('activities/publish')
  async publishSpec(
    @AuthUser() auth: AuthClaims,
    @Body() body: { yaml: string },
  ) {
    const spec = await this.lintAndParse(body.yaml);
    return this.catalog.publishNewVersion({
      tenantId: auth.tenantId,
      activitySlug: spec.id,
      mode: spec.mode,
      spec,
      publishedBy: auth.userId,
    });
  }

  /**
   * Doc §3.7 CMS flow, step 1: create a DRAFT version. Lints the same as
   * activities/publish, but the resulting row is invisible to every learner-facing
   * query (status != PUBLISHED) until it's explicitly moved through
   * review and approval below.
   */
  @Post('activities/draft')
  async draftSpec(
    @AuthUser() auth: AuthClaims,
    @Body() body: { yaml: string },
  ) {
    const spec = await this.lintAndParse(body.yaml);
    return this.catalog.createDraft({
      tenantId: auth.tenantId,
      activitySlug: spec.id,
      mode: spec.mode,
      spec,
    });
  }

  /** Doc §3.7 step 2: author submits a draft for review. Any author/admin may do this. */
  @Post('activity-versions/:id/submit-review')
  async submitForReview(@Param('id') id: string) {
    return this.catalog.transitionVersionStatus(id, 'DRAFT', 'IN_REVIEW');
  }

  /**
   * Doc §3.7 step 3: a reviewer approves (or the same route sends it
   * back to DRAFT -- see `to` body param). Restricted to 'admin' only,
   * not 'author' -- the whole point of an approval gate is that the
   * person who wrote the content isn't the same person who approves it
   * (separation of duties), enforced here at the role level, not just by
   * convention.
   */
  @Post('activity-versions/:id/review-decision')
  @Roles(Role.ADMIN)
  async reviewDecision(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Body() body: { decision: 'approve' | 'reject' },
  ) {
    const to: ActivityVersionStatus =
      body.decision === 'approve' ? 'APPROVED' : 'DRAFT';
    return this.catalog.transitionVersionStatus(
      id,
      'IN_REVIEW',
      to,
      auth.userId,
    );
  }

  /** Doc §3.7 step 4: publish an already-approved version. Also 'admin'-only for the same separation-of-duties reason. */
  @Post('activity-versions/:id/publish')
  @Roles(Role.ADMIN)
  async publishApprovedVersion(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
  ) {
    return this.catalog.transitionVersionStatus(
      id,
      'APPROVED',
      'PUBLISHED',
      auth.userId,
    );
  }

  private async lintAndParse(yaml: string): Promise<ActivitySpec> {
    // Asserted before result.valid is checked below -- real shape isn't
    // guaranteed until lint() confirms it against
    // contracts/activity_spec.schema.json, but lint() itself needs a
    // structurally-typed object to call .skills/.meta etc on, same as
    // before this migration to the canonical ActivitySpec type (K10).
    const spec = this.lint.parseYaml(yaml) as ActivitySpec;

    const skills = await this.db
      .selectFrom('skill.skill')
      .select('slug')
      .execute();
    const result = this.lint.lint(spec, new Set(skills.map((s) => s.slug)));
    if (!result.valid) {
      throw new BadRequestException({
        message: 'lint failed',
        issues: result.issues,
      });
    }
    return spec;
  }

  @Get('activities/:activityId/analytics/dropoff')
  async taskDropoff(
    @AuthUser() auth: AuthClaims,
    @Param('activityId') activityId: string,
  ) {
    await this.assertActivityInCallerTenant(auth, activityId);
    return this.analytics.getTaskDropoff(activityId);
  }

  @Get('activities/:activityId/analytics/validators')
  async validatorHealth(
    @AuthUser() auth: AuthClaims,
    @Param('activityId') activityId: string,
  ) {
    await this.assertActivityInCallerTenant(auth, activityId);
    return this.analytics.getValidatorHealth(activityId);
  }

  @Get('analytics/activity-health')
  async activityHealth(@AuthUser() auth: AuthClaims) {
    return this.analytics.getActivityHealth(auth.tenantId);
  }

  // --- Phase 3 4.2/4.3: analytics rollups + cost dashboard ----------

  /**
   * Event rollups attempt → activity → course → tenant → global, served
   * from ClickHouse when configured (4.1/4.2), Postgres otherwise. The
   * `store` field in the response says which backend answered — the 4.2
   * "identical numbers" check compares both.
   */
  @Get('analytics/rollup')
  async analyticsRollup(
    @AuthUser() auth: AuthClaims,
    @Query('grain') grain: RollupGrain = 'activity',
    @Query('hours') hours = '24',
  ) {
    const rows = await this.analyticsQuery.eventRollup(
      auth.tenantId,
      normalizeRollupGrain(grain),
      Number(hours) || 24,
    );
    return { store: this.analyticsQuery.store(), grain, rows };
  }

  /**
   * Blended cost (compute + cloud + AI) per learner / course / activity /
   * tenant, from env.usage_meter (4.3 / B10). Empty when the orchestrator's
   * env schema isn't present.
   */
  @Get('cost/by-grain')
  async costByGrain(
    @AuthUser() auth: AuthClaims,
    @Query('grain') grain: CostGrain = 'activity',
    @Query('days') days = '30',
  ) {
    return this.cost.costByGrain(
      auth.tenantId,
      normalizeCostGrain(grain),
      Number(days) || 30,
    );
  }

  /** Per-account spend + quarantine flags (§10.3). */
  @Get('cost/accounts')
  async costAccounts(@Query('days') days = '30') {
    return this.cost.accountSpend(Number(days) || 30);
  }

  /** Budget-breach / quarantine history for the caller's tenant. */
  @Get('cost/budget-breaches')
  async budgetBreaches(
    @AuthUser() auth: AuthClaims,
    @Query('limit') limit = '50',
  ) {
    return this.cost.budgetBreachHistory(auth.tenantId, Number(limit) || 50);
  }

  /**
   * dropoff/validators are keyed by activityId with no tenant column in
   * their own query (see analytics.service.ts) -- without this check an
   * admin from tenant A could read tenant B's content-health stats just
   * by guessing/enumerating an activityId.
   */
  private async assertActivityInCallerTenant(
    auth: AuthClaims,
    activityId: string,
  ): Promise<void> {
    const activity = await this.db
      .selectFrom('content.activity')
      .select('tenant_id')
      .where('id', '=', activityId)
      .executeTakeFirst();
    if (!activity || activity.tenant_id !== auth.tenantId) {
      throw new BadRequestException('activity not found');
    }
  }
}

function normalizeRollupGrain(g: string): RollupGrain {
  return (['activity', 'course', 'tenant', 'global'] as const).includes(
    g as RollupGrain,
  )
    ? (g as RollupGrain)
    : 'activity';
}

function normalizeCostGrain(g: string): CostGrain {
  return (['learner', 'course', 'activity', 'tenant'] as const).includes(
    g as CostGrain,
  )
    ? (g as CostGrain)
    : 'activity';
}
