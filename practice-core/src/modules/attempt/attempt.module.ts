import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { AttemptRepository } from './attempt.repository';
import { AttemptService } from './attempt.service';
import { EligibilityService } from './eligibility.service';
import { HintService } from './hint.service';
import { WorkspaceFileService } from './workspace-file.service';
import { AttemptController } from './attempt.controller';
import { FakeOrchestratorClient } from './fake-orchestrator.client';
import { GrpcOrchestratorClient } from './grpc-orchestrator.client';
import { ORCHESTRATOR_CLIENT } from './orchestrator-client.interface';
import { SkillModule } from '../skill/skill.module';
import { EventStoreModule } from '../event-store/event-store.module';
import { EvaluationModule } from '../evaluation/evaluation.module';

@Module({
  imports: [SkillModule, EventStoreModule, EvaluationModule],
  controllers: [AttemptController],
  providers: [
    AttemptRepository,
    AttemptService,
    EligibilityService,
    HintService,
    WorkspaceFileService,
    GrpcOrchestratorClient,
    // PLAN.md Phase 0's "swap the mock" promise, delivered: real gRPC
    // client against Dev A's live Environment Orchestrator by default.
    // USE_FAKE_ORCHESTRATOR=true keeps FakeOrchestratorClient available
    // for tests / local dev without a running orchestrator process --
    // AttemptService code itself never changes either way.
    {
      provide: ORCHESTRATOR_CLIENT,
      useFactory: (config: ConfigService, real: GrpcOrchestratorClient) => {
        const useFake = config.get<string>('USE_FAKE_ORCHESTRATOR') === 'true';
        return useFake ? new FakeOrchestratorClient() : real;
      },
      inject: [ConfigService, GrpcOrchestratorClient],
    },
  ],
  exports: [AttemptService, AttemptRepository, EligibilityService],
})
export class AttemptModule {}
