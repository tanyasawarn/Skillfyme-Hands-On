import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { ValidatorRunnerService } from './validator-runner.service';
import { ScoringEngineService } from './scoring-engine.service';
import { EvaluationService } from './evaluation.service';
import { FaultRepository } from './fault.repository';
import { RubricRepository } from './rubric.repository';
import { ArtifactService } from './artifact.service';
import { FakeAiGrader } from './fake-ai-grader.service';
import { ClaudeAiGrader } from './claude-ai-grader.service';
import { AI_GRADER } from './ai-grader.interface';
import { FakeValidatorExecutor } from './fake-validator-executor';
import { GrpcValidatorExecutor } from './grpc-validator-executor';
import { VALIDATOR_EXECUTOR } from './validator-executor.interface';
import { EventStoreModule } from '../event-store/event-store.module';
import { SkillModule } from '../skill/skill.module';
import { ActivitySpecReader } from '../../common/activity-spec-reader';
import { AttemptRepository } from '../attempt/attempt.repository';

@Module({
  imports: [EventStoreModule, SkillModule],
  providers: [
    ValidatorRunnerService,
    ScoringEngineService,
    EvaluationService,
    FaultRepository,
    RubricRepository,
    ArtifactService,
    ActivitySpecReader,
    // Same circular-import constraint S5 (ActivitySpecReader) hit:
    // AttemptModule imports EvaluationModule, so EvaluationModule can't
    // import AttemptModule back for its AttemptRepository export.
    // Registered directly here instead -- AttemptRepository is stateless
    // (just a Kysely wrapper), so a second DI instance in this module is
    // harmless, same reasoning as ActivitySpecReader's own two instances.
    AttemptRepository,
    GrpcValidatorExecutor,
    FakeAiGrader,
    ClaudeAiGrader,
    // Doc §6.5/§7's AI Gateway integration. Same swap-the-mock shape as
    // VALIDATOR_EXECUTOR below: real grading (ClaudeAiGrader) is the
    // default whenever ANTHROPIC_API_KEY is configured; falls back to
    // FakeAiGrader otherwise so the artifact-submission pipeline still
    // works end-to-end (with an honest, always-provisional stub result)
    // in any environment without a provider key -- local dev, CI,
    // content-ci.ts's own runs, none of which should require a live key
    // just to exercise the rest of the pipeline.
    {
      provide: AI_GRADER,
      useFactory: (
        config: ConfigService,
        real: ClaudeAiGrader,
        fake: FakeAiGrader,
      ) => {
        return config.get<string>('ANTHROPIC_API_KEY') ? real : fake;
      },
      inject: [ConfigService, ClaudeAiGrader, FakeAiGrader],
    },
    // Same swap-the-mock pattern as ORCHESTRATOR_CLIENT (attempt.module.ts):
    // real execution (6 of 18 validator types, see
    // orchestrator/internal/validation) is the default now;
    // USE_FAKE_ORCHESTRATOR=true keeps FakeValidatorExecutor available
    // for tests / local dev without a running orchestrator process.
    {
      provide: VALIDATOR_EXECUTOR,
      useFactory: (config: ConfigService, real: GrpcValidatorExecutor) => {
        const useFake = config.get<string>('USE_FAKE_ORCHESTRATOR') === 'true';
        return useFake ? new FakeValidatorExecutor() : real;
      },
      inject: [ConfigService, GrpcValidatorExecutor],
    },
  ],
  exports: [
    EvaluationService,
    ValidatorRunnerService,
    VALIDATOR_EXECUTOR,
    ArtifactService,
  ],
})
export class EvaluationModule {}
