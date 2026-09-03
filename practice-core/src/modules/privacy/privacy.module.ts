import { Module } from '@nestjs/common';
import { PrivacyController } from './privacy.controller';
import { PrivacyService } from './privacy.service';

/**
 * PLAN.md C16 / memory.md §883 -- learner-facing GDPR export + erasure.
 * DatabaseModule is @Global, so KYSELY is available without importing it.
 */
@Module({
  controllers: [PrivacyController],
  providers: [PrivacyService],
  exports: [PrivacyService],
})
export class PrivacyModule {}
