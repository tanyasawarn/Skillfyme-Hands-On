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
import { T3ValidatorExecutor } from './t3/t3-validator.executor';
import { OrchestratorShellRunner } from './t3/orchestrator-shell-runner';
import { LocalShellRunner } from './t3/local-shell-runner';
import { T3_SHELL_RUNNER } from './t3/shell-runner';
import {
  GRADER_IDENTITY,
  SolutionStore,
  issueGraderIdentity,
} from './solution-store';
import * as path from 'node:path';
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
    // PLAN.md G3 / doc §7.4 IAM boundary: the grader identity that gates
    // SolutionStore. Provided ONLY here, inside the evaluation module --
    // the Mentor module cannot import SolutionStore (not on the seam,
    // eslint.boundaries.mjs) and has no way to obtain this token.
    { provide: GRADER_IDENTITY, useFactory: () => issueGraderIdentity() },
    SolutionStore,
    // Same circular-import constraint S5 (ActivitySpecReader) hit:
    // AttemptModule imports EvaluationModule, so EvaluationModule can't
    // import AttemptModule back for its AttemptRepository export.
    // Registered directly here instead -- AttemptRepository is stateless
    // (just a Kysely wrapper), so a second DI instance in this module is
    // harmless, same reasoning as ActivitySpecReader's own two instances.
    AttemptRepository,
    GrpcValidatorExecutor,
    OrchestratorShellRunner,
    T3ValidatorExecutor,
    // Phase 3 (1.8 / B3). The shell backend the T3 validator executors
    // run through. LocalShellRunner (rooted at T3_LOCAL_FIXTURE_DIR) is
    // the pre-driver de-risking path — a static Terraform repo + a
    // pre-made sandbox account's aws profile. When that var is unset the
    // OrchestratorShellRunner is used (production: exec inside the T3
    // workspace pod). Provider value can be undefined; T3ValidatorExecutor
    // injects it @Optional() and returns ERROR (never scored) if a T3
    // validator runs with nothing wired.
    {
      provide: T3_SHELL_RUNNER,
      useFactory: (config: ConfigService, orch: OrchestratorShellRunner) => {
        const fixtureDir = config.get<string>('T3_LOCAL_FIXTURE_DIR');
        if (fixtureDir) {
          return new LocalShellRunner(path.resolve(fixtureDir));
        }
        return orch;
      },
      inject: [ConfigService, OrchestratorShellRunner],
    },
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
      useFactory: (
        config: ConfigService,
        real: GrpcValidatorExecutor,
        t3: T3ValidatorExecutor,
      ) => {
        const useFake = config.get<string>('USE_FAKE_ORCHESTRATOR') === 'true';
        if (useFake) return new FakeValidatorExecutor();
        // Phase 3 (1.8): T3ValidatorExecutor handles IAC_STATE /
        // CLOUD_ASSERT / STATIC_ANALYSIS and delegates every other type
        // to GrpcValidatorExecutor verbatim, so this is a safe default
        // even for activities that use no T3 validator. Opt out with
        // T3_VALIDATORS=off if a deployment wants the pre-1.8 behaviour.
        return config.get<string>('T3_VALIDATORS') === 'off' ? real : t3;
      },
      inject: [ConfigService, GrpcValidatorExecutor, T3ValidatorExecutor],
    },
  ],
  exports: [
    EvaluationService,
    ValidatorRunnerService,
    VALIDATOR_EXECUTOR,
    ArtifactService,
    // Phase 3 (1.6 / 1.9): the project milestone state machine reuses the
    // AI-grader + rubric content (PLAN_PHASE3_PROJECTS.md B5). Exported
    // via the seam — see eslint.boundaries.mjs's SEAM list.
    AI_GRADER,
    RubricRepository,
  ],
})
export class EvaluationModule {}
