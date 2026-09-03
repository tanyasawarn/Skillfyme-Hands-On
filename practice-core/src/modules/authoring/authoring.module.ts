import { Module } from '@nestjs/common';
import { ContentCiModule } from '../content-ci/content-ci.module';
import { LlmGatewayModule } from '../llm-gateway/llm-gateway.module';
import { AuthoringAssistantService } from './authoring-assistant.service';

/**
 * PLAN.md G12 / doc §3.1 -- internal AI-assisted authoring. Composes the
 * LLM Gateway (G1) for the model call and SpecLintService (from
 * ContentCiModule) for the schema validation. No learner-facing routes;
 * the author-facing endpoint is wired under the admin controller.
 */
@Module({
  imports: [LlmGatewayModule, ContentCiModule],
  providers: [AuthoringAssistantService],
  exports: [AuthoringAssistantService],
})
export class AuthoringModule {}
