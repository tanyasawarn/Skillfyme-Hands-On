import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { DatabaseModule } from '../../db/database.module';
import { EventStoreModule } from '../event-store/event-store.module';
import { EvaluationModule } from '../evaluation/evaluation.module';
import { AttemptRepository } from '../attempt/attempt.repository';
import { ProjectRepository } from './project.repository';
import { ForgejoClient } from './forgejo.client';
import { GitService } from './git.service';
import { ProjectService } from './project.service';
import { ProjectScoringService } from './project-scoring';
import { DefenceService } from './defence.service';
import { ProjectController } from './project.controller';
import { PROJECT_ORCHESTRATOR } from './project-orchestrator.port';
import { FakeProjectOrchestrator } from './fake-project-orchestrator';
import { GrpcProjectOrchestrator } from './grpc-project-orchestrator';
import { VIVA_MODEL } from './viva-model.port';
import { RealVivaModel, FakeVivaModel } from './viva-model.port';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md — Track B). Project mode: the
 * milestone state machine (1.6), per-learner Git hosting client (1.7),
 * the six T3 validator executors' client dispatch (1.8/3.7, in
 * EvaluationModule), the defence viva (3.8), and sp.project.default's
 * roll-up (3.9).
 *
 * Imports EvaluationModule for ValidatorRunnerService + AI_GRADER +
 * RubricRepository (its public seam), same way AttemptModule does.
 * AttemptRepository is registered directly (stateless Kysely wrapper) to
 * avoid an import cycle, matching EvaluationModule's own comment.
 *
 * PROJECT_ORCHESTRATOR: FakeProjectOrchestrator by default;
 * GrpcProjectOrchestrator (a real adapter over
 * contracts/orchestrator.proto Provision(T3)/Snapshot/Restore/Destroy)
 * when PROJECT_ORCHESTRATOR_GRPC=on — Stage 3.4's "swap the fake for the
 * real driver". VIVA_MODEL: FakeVivaModel unless ANTHROPIC_API_KEY is
 * set (same rule as AI_GRADER).
 */
@Module({
  imports: [DatabaseModule, EventStoreModule, EvaluationModule],
  controllers: [ProjectController],
  providers: [
    AttemptRepository,
    ProjectRepository,
    ForgejoClient,
    GitService,
    ProjectService,
    ProjectScoringService,
    DefenceService,
    RealVivaModel,
    FakeVivaModel,
    GrpcProjectOrchestrator,
    {
      provide: PROJECT_ORCHESTRATOR,
      useFactory: (config: ConfigService, real: GrpcProjectOrchestrator) => {
        return config.get<string>('PROJECT_ORCHESTRATOR_GRPC') === 'on'
          ? real
          : new FakeProjectOrchestrator();
      },
      inject: [ConfigService, GrpcProjectOrchestrator],
    },
    {
      provide: VIVA_MODEL,
      useFactory: (
        config: ConfigService,
        real: RealVivaModel,
        fake: FakeVivaModel,
      ) => (config.get<string>('ANTHROPIC_API_KEY') ? real : fake),
      inject: [ConfigService, RealVivaModel, FakeVivaModel],
    },
  ],
  exports: [
    ProjectService,
    GitService,
    ProjectRepository,
    ProjectScoringService,
  ],
})
export class ProjectModule {}
