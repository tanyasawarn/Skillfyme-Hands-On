import { Module } from '@nestjs/common';
import { EventStoreModule } from '../event-store/event-store.module';
import { LlmGatewayModule } from '../llm-gateway/llm-gateway.module';
import { EnvStateSummaryService } from './env-state-summary.service';
import { MentorController } from './mentor.controller';
import { MentorService } from './mentor.service';

/**
 * PLAN.md Phase 4 -- AI Mentor.
 *
 *   G2  EnvStateSummaryService  -- structured env-state view (doc §7.4)
 *   G5  disclosure-policy / output-guardrail  -- pure, no DI
 *   G7  prompt-registry / adversarial-suite   -- pure, no DI
 *   G4  MentorService  -- the layered pipeline that composes G2/G5/G7 and
 *       calls G1 (LlmGatewayModule)
 *
 * G3 (IAM boundary): this module does NOT import SolutionStore and cannot
 * (it is not on EvaluationModule's seam; eslint.boundaries.mjs enforces).
 * DatabaseModule is @Global so KYSELY is available.
 */
@Module({
  imports: [EventStoreModule, LlmGatewayModule],
  providers: [EnvStateSummaryService, MentorService],
  controllers: [MentorController],
  exports: [EnvStateSummaryService, MentorService],
})
export class MentorModule {}
