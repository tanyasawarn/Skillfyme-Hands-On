import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { ValidatorRunnerService } from './validator-runner.service';
import { ScoringEngineService } from './scoring-engine.service';
import { EvaluationService } from './evaluation.service';
import { FakeValidatorExecutor } from './fake-validator-executor';
import { GrpcValidatorExecutor } from './grpc-validator-executor';
import { VALIDATOR_EXECUTOR } from './validator-executor.interface';
import { EventStoreModule } from '../event-store/event-store.module';
import { SkillModule } from '../skill/skill.module';

@Module({
  imports: [EventStoreModule, SkillModule],
  providers: [
    ValidatorRunnerService,
    ScoringEngineService,
    EvaluationService,
    GrpcValidatorExecutor,
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
  exports: [EvaluationService, ValidatorRunnerService, VALIDATOR_EXECUTOR],
})
export class EvaluationModule {}
