import { Body, Controller, Get, Param, Post, UseGuards } from '@nestjs/common';
import { AttemptOwnershipGuard } from '../attempt/attempt-ownership.guard';
import { ProjectService } from './project.service';
import { DefenceService } from './defence.service';
import type { ProjectMilestoneKey } from '../../db/schema';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.6 / B2). The project-mode learner
 * surface (memory.md §12.3, lines 1531-1534):
 *
 *   GET  /v1/practice/attempts/{id}/milestones
 *   POST /v1/practice/attempts/{id}/milestones/{key}/submit
 *   POST /v1/practice/attempts/{id}/defence/messages
 *
 * The doc's literal syntax is `/milestones/{key}:submit` (AIP colon-
 * action); this router version rejects a literal `:` after a route
 * param, so `/submit` is used — same resource-oriented intent, same
 * convention already applied to `/attempts/{id}/submit` in
 * AttemptController.
 *
 * Ownership: reuses AttemptOwnershipGuard (reads `:id`), so a caller can
 * only touch their own attempt's milestones — doc §9.1 T3.
 */
@Controller('v1/practice/attempts/:id')
@UseGuards(AttemptOwnershipGuard)
export class ProjectController {
  constructor(
    private readonly project: ProjectService,
    private readonly defence: DefenceService,
  ) {}

  @Get('milestones')
  async listMilestones(@Param('id') id: string) {
    return this.project.listMilestones(id);
  }

  @Post('milestones/:key/submit')
  async submitMilestone(
    @Param('id') id: string,
    @Param('key') key: ProjectMilestoneKey,
    @Body()
    body: { design_text?: string; commit_sha?: string } = {},
  ) {
    return this.project.submitMilestone({
      attemptId: id,
      milestoneKey: key,
      designText: body.design_text,
      commitSha: body.commit_sha,
    });
  }

  @Post('defence/messages')
  async postDefenceMessage(
    @Param('id') id: string,
    @Body()
    body: { role?: 'LEARNER' | 'EXAMINER'; text?: string; start?: boolean },
  ) {
    if (body.start) {
      return this.defence.startViva(id);
    }
    return this.defence.postMessage({
      attemptId: id,
      role: body.role ?? 'LEARNER',
      text: body.text ?? '',
    });
  }
}
