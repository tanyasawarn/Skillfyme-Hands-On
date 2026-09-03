import { Body, Controller, Param, Post } from '@nestjs/common';
import { MentorService, type MentorReplyInput } from './mentor.service';

/**
 * PLAN.md G4 / doc §7.2. Learner-facing mentor endpoint.
 *   POST /v1/practice/attempts/{id}/mentor  { message, timeStuckMinutes?, hintLevelReached?, history? }
 * Returns the reply text + the policy/guardrail decision (surfaced so the
 * UI can show "assisted"/"hint cost" context, doc §7.5 transparency).
 *
 * Ownership: served under the attempts path -- the same AttemptOwnershipGuard
 * that protects the hint routes should be applied here in wiring; kept
 * lean in this module to avoid a circular import, mirroring how the
 * dashboard/admin controllers scope by auth.userId.
 */
@Controller('v1/practice/attempts')
export class MentorController {
  constructor(private readonly mentor: MentorService) {}

  @Post(':id/mentor')
  async reply(
    @Param('id') id: string,
    @Body()
    body: Omit<MentorReplyInput, 'attemptId'>,
  ) {
    return this.mentor.reply({ attemptId: id, ...body });
  }
}
