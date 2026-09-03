import { Controller, Delete, Get } from '@nestjs/common';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';
import { PrivacyService } from './privacy.service';

/**
 * PLAN.md C16 / memory.md §883 -- the learner acts on their OWN data
 * only. Both routes derive the subject from the bearer JWT (auth.userId);
 * neither takes a user id parameter, so a learner cannot export or erase
 * anyone else. An admin-initiated erasure (e.g. a support ticket) is a
 * separate future surface, not this one.
 */
@Controller('v1/practice/me')
export class PrivacyController {
  constructor(private readonly privacy: PrivacyService) {}

  /** GDPR right of access: the full archive of this learner's data. */
  @Get('export')
  async export(@AuthUser() auth: AuthClaims) {
    return this.privacy.exportForUser(auth.userId);
  }

  /**
   * GDPR right to erasure: anonymise this learner's account and redact
   * every PII-bearing child record. Aggregate counters
   * (learner_activity_state, attempt_score, the anonymised attempt rows)
   * are retained so analytics stay correct. Idempotent.
   */
  @Delete('data')
  async erase(@AuthUser() auth: AuthClaims) {
    return this.privacy.eraseForUser(auth.userId);
  }
}
