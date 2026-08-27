import { Controller, Get, Param, Query } from '@nestjs/common';
import { CatalogRepository } from './catalog.repository';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';
import { findOrThrow } from '../../common/find-or-throw';

/**
 * Doc §8.3: "GET /v1/practice/activities?course&skill&mode&difficulty..."
 * Phase 1 scope: tenant-scoped listing of PUBLISHED activity versions only.
 * Filters (course/skill/mode/difficulty) are not yet implemented -- this is
 * the read path needed to prove the publish pipeline end-to-end, not the
 * full catalog query surface (M1.13 frontend work will need more).
 */
@Controller('v1/practice/activities')
export class CatalogController {
  constructor(private readonly catalog: CatalogRepository) {}

  @Get()
  async list(@AuthUser() auth: AuthClaims) {
    return this.catalog.listPublishedCatalog(auth.tenantId);
  }

  /**
   * Skill-driven catalog: every curriculum skill for ONE course, each
   * carrying the PUBLISHED activity that covers it as a primary skill
   * (if any). Entry point requested to replace the activity-first
   * catalog listing -- clicking a skill provisions its mapped lab
   * through the unchanged attempt lifecycle (create -> provision ->
   * start -> submit).
   *
   * `course` query param comes from the external LMS's launch URL (this
   * app is opened per-course, not a switcher inside the app). Defaults
   * to the original devops-with-ai course so existing callers that don't
   * pass it yet keep working unchanged.
   */
  @Get('by-skill')
  async listBySkill(
    @AuthUser() auth: AuthClaims,
    @Query('course') courseSlug: string = 'devops-with-ai',
  ) {
    return this.catalog.listSkillDrivenCatalog(auth.tenantId, courseSlug);
  }

  @Get(':activityVersionId')
  async getOne(@Param('activityVersionId') activityVersionId: string) {
    return findOrThrow(
      await this.catalog.getVersionById(activityVersionId),
      `activity version ${activityVersionId} not found`,
    );
  }
}
